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
	"github.com/namnguyen3019/pelt/internal/stages"
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
		seriesTo = fs.String("series", "", "also write one row per second of the run to this CSV file")
		quiet    = fs.Bool("quiet", false, "hide the live progress line")
		http2    = fs.Bool("http2", false, "allow HTTP/2 (default forces HTTP/1.1)")
		dryRun   = fs.Bool("dry-run", false, "run setup and each flow once as one user, print every request and response, and exit")
		noFile   = fs.Bool("no-thresholds", false, "ignore the file's thresholds (-threshold flags still apply)")
		stageArg = fs.String("stages", "", "move the number of users over the run, e.g. 1m:100,5m:100,1m:0 (replaces the file's load.stages, users and duration)")
		ramp     = fs.Duration("ramp", 0, "ramp users up from 0 over this long, then hold until the end of the run (short for -stages RAMP:U,REST:U)")
		showVer  = fs.Bool("version", false, "print the version and exit")
		cliTs    thresholdFlag
	)
	network := networkFlags(fs)
	fs.Var(&cliTs, "threshold", "pass/fail target, e.g. 'p95<500ms' or 'checkout/pay: errors<1%' (repeatable; added to the file's)")

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
	if *showVer {
		fmt.Fprintln(stdout, versionString())
		return exitOK
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

	// The file's thresholds, then the command line's. A scope that names
	// no flow or step fails here, before any load is sent.
	var targets []flow.Target
	if !*noFile {
		targets = append(targets, sc.Thresholds...)
	}
	for _, t := range cliTs {
		tg, err := sc.ResolveThreshold(t)
		if err != nil {
			fmt.Fprintf(stderr, "error: threshold %q: %v\n", t.Expr, err)
			return exitUsage
		}
		targets = append(targets, tg)
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
	if err := flowStages(&opts, *stageArg, *ramp, set); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	opts.HTTP2 = *http2
	if opts.TLS, opts.Proxy, err = network.Build(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitUsage
	}
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
	seriesFile, err := createSeries(*seriesTo)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	defer seriesFile.Close()

	stopProgress := func() {}
	if !*quiet && isTerminal(os.Stderr) {
		stopProgress = startFlowProgress(runner, stderr)
	}
	res := runner.Run(ctx)
	stopProgress()

	rep := report.NewFlow(res)
	rep.Check(targets)
	if *output == "json" {
		err = rep.WriteJSON(stdout)
	} else {
		err = rep.WriteText(stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error writing report: %v\n", err)
		return exitError
	}
	if err := writeSeries(seriesFile, rep.WriteSeriesCSV); err != nil {
		fmt.Fprintf(stderr, "error writing %s: %v\n", *seriesTo, err)
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
	plan := r.Options().Stages
	return redraw(w, func() string { return flowProgressLine(r.Progress(), plan) })
}

// flowStages applies -stages and -ramp to opts and rejects flags that
// clash with stages, from the file or the command line.
func flowStages(opts *flow.Options, spec string, ramp time.Duration, set map[string]bool) error {
	switch {
	case set["stages"] && set["ramp"]:
		return errors.New("use -stages or -ramp, not both (-ramp 30s is short for -stages 30s:U,REST:U)")
	case set["stages"]:
		p, err := stages.Parse(spec)
		if err != nil {
			return fmt.Errorf("-stages: %v", err)
		}
		switch {
		case p.Kind == stages.Rate:
			return errors.New("-stages: flow stages move users, such as 1m:100; rate stages (500/s) are for single-URL mode only for now")
		case set["d"]:
			return fmt.Errorf("-stages already set the run length (%s); drop -d, or add a stage", stages.Dur(p.Total()))
		case set["u"]:
			return errors.New("-stages moves the number of users, so -u cannot be set too; the highest stage target is the most users")
		}
		opts.Stages, opts.Users, opts.Duration = &p, 0, 0
	case set["ramp"]:
		if opts.Stages != nil {
			return errors.New("the file already has load.stages; drop -ramp, or replace them with -stages")
		}
		u := opts.Users
		if u == 0 {
			u = 1
		}
		p, err := stages.Ramp(ramp, opts.Duration, float64(u))
		if err != nil {
			return err
		}
		opts.Stages, opts.Users, opts.Duration = &p, 0, 0
	case opts.Stages != nil && set["d"]:
		return fmt.Errorf("the file's load.stages set the run length (%s); drop -d, or use -stages to replace them", stages.Dur(opts.Stages.Total()))
	case opts.Stages != nil && set["u"]:
		return errors.New("the file's load.stages move the number of users, so -u cannot be set too; use -stages to replace them")
	}
	return nil
}

// flowProgressLine is the live line for flow mode: iterations so far, and
// iterations and requests in the last whole second, and failed checks.
// With stages it leads with the stage, its target now and active users.
func flowProgressLine(p flow.Progress, plan *stages.Plan) string {
	last := p.LastSecond
	if plan != nil {
		return fmt.Sprintf("stage %d/%d  target %d  active %s  %s it/s  %s req/s  %s failed checks",
			p.Stage, len(plan.Stages), p.Target, report.GroupInt(p.Active),
			report.GroupInt(int64(last.Iterations)), report.GroupInt(int64(last.Requests)), report.GroupInt(p.CheckFails))
	}
	return fmt.Sprintf("%s iterations, %s/s and %s req/s last second, %s failed checks",
		report.GroupInt(p.Iterations), report.GroupInt(int64(last.Iterations)),
		report.GroupInt(int64(last.Requests)), report.GroupInt(p.CheckFails))
}
