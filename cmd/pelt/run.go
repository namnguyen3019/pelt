package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/namnguyen3019/pelt/internal/flow"
	"github.com/namnguyen3019/pelt/internal/report"
)

// runFlow is `pelt run [flags] <scenario.json>`: flow mode.
func runFlow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pelt run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: pelt run [flags] <scenario.json>\n\n"+
			"Runs the user flows in a scenario file. Flags override the file's load block\n"+
			"and may go before or after the file name. See docs/flows.md.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	var (
		users    = fs.Int("u", 0, "virtual users, each looping flows (default: file's load.users, else 1)")
		duration = fs.Duration("d", 0, "run for this long, e.g. 2m (default: file's load.duration)")
		iters    = fs.Int("i", 0, "stop after this many iterations in total (default: file's load.iterations)")
		timeout  = fs.Duration("t", 0, "per-request timeout (default: file's load.timeout, else 10s)")
		base     = fs.String("base", "", "base URL for relative step URLs, replacing the file's base_url")
		seed     = fs.Uint64("seed", 0, "random seed for flow picks, think time and $rand (default: random, printed in the report)")
		output   = fs.String("o", "text", "output format: text or json")
		quiet    = fs.Bool("quiet", false, "hide the live progress line")
		http2    = fs.Bool("http2", false, "allow HTTP/2 (default forces HTTP/1.1)")
		dryRun   = fs.Bool("dry-run", false, "run setup and each flow once as one user, print every request and response, and exit")
	)

	// Accept flags on either side of the file name: pelt run shop.json -u 50.
	var file string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		if file != "" {
			fmt.Fprintf(stderr, "error: one scenario file at a time (got %s and %s)\n", file, fs.Arg(0))
			return 2
		}
		file, args = fs.Arg(0), fs.Args()[1:]
	}
	if file == "" {
		fs.Usage()
		return 2
	}
	if *output != "text" && *output != "json" {
		fmt.Fprintln(stderr, "error: -o must be text or json")
		return 2
	}

	sc, err := flow.LoadFile(file, *base)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	opts := sc.Options()
	if set["u"] {
		opts.Users = *users
	}
	if set["d"] {
		opts.Duration = *duration
	}
	if set["i"] {
		opts.Iterations = *iters
	}
	if set["t"] {
		opts.Timeout = *timeout
	}
	if set["u"] && *users < 1 {
		fmt.Fprintln(stderr, "error: -u must be at least 1")
		return 2
	}
	opts.HTTP2 = *http2
	opts.Seed = *seed
	if !set["seed"] {
		opts.Seed = randomSeed()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C kills the process
	}()

	if *dryRun {
		ok := flow.DryRun(ctx, sc, opts, stdout)
		switch {
		case ctx.Err() != nil:
			return 130
		case !ok:
			return 1
		}
		return 0
	}

	runner, err := flow.New(sc, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	stopProgress := func() {}
	if !*quiet && isTerminal(os.Stderr) {
		stopProgress = startFlowProgress(runner, stderr)
	}
	res := runner.Run(ctx)
	stopProgress()

	rep := report.NewFlow(res)
	if *output == "json" {
		err = rep.WriteJSON(stdout)
	} else {
		err = rep.WriteText(stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error writing report: %v\n", err)
		return exitError
	}
	return finish(rep.Verdict(), res.Interrupted, *output == "json", stderr)
}

func randomSeed() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	// Keep it short enough to retype: up to 6 digits.
	return binary.LittleEndian.Uint64(b[:]) % 1_000_000
}

func startFlowProgress(r *flow.Runner, w io.Writer) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-done:
				fmt.Fprint(w, "\r\033[K")
				return
			case <-t.C:
				n, fails := r.Progress()
				fmt.Fprintf(w, "\r\033[K  %d iterations done, %.1f/s, %d failed checks",
					n, float64(n)/time.Since(start).Seconds(), fails)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
