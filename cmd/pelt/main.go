// Command pelt fires concurrent HTTP requests at a URL and reports
// throughput and latency.
//
//	pelt -n 10000 -c 200 http://127.0.0.1:8080/
//	pelt -d 30s -c 500 -rate 2000 http://127.0.0.1:8080/
//
// Press Ctrl-C once to stop early and still get a report. Press it again to
// quit immediately.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/namnguyen3019/pelt/internal/loadtest"
	"github.com/namnguyen3019/pelt/internal/report"
)

// headerFlag implements flag.Value so -H can be repeated.
type headerFlag struct{ h http.Header }

func (f *headerFlag) String() string { return "" }

func (f *headerFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return fmt.Errorf("header must look like 'Name: value', got %q", v)
	}
	if f.h == nil {
		f.h = make(http.Header)
	}
	f.h.Add(name, strings.TrimSpace(value))
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the whole program so it can return an exit code (and be tested)
// instead of calling os.Exit from deep inside.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pelt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: pelt [flags] <url>\n\nFlags:\n")
		fs.PrintDefaults()
	}

	var (
		headers  headerFlag
		n        = fs.Int("n", 200, "total number of requests (0 = no limit, needs -d)")
		c        = fs.Int("c", 50, "concurrency: number of worker goroutines")
		d        = fs.Duration("d", 0, "run for this long, e.g. 30s (overrides the default -n)")
		rate     = fs.Float64("rate", 0, "max requests per second across all workers (0 = unlimited)")
		method   = fs.String("m", "GET", "HTTP method")
		body     = fs.String("body", "", "request body")
		bodyFile = fs.String("body-file", "", "read the request body from this file")
		timeout  = fs.Duration("t", 10*time.Second, "per-request timeout")
		http2    = fs.Bool("http2", false, "allow HTTP/2 (default forces HTTP/1.1)")
		untuned  = fs.Bool("untuned", false, "use Go's stock connection pool limits (for comparison)")
		output   = fs.String("o", "text", "output format: text or json")
		quiet    = fs.Bool("quiet", false, "hide the live progress line")
	)
	fs.Var(&headers, "H", "request header 'Name: value' (repeatable)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	// With -d and no explicit -n, run for the full duration instead of
	// stopping at the default 200 requests.
	nSet := false
	fs.Visit(func(f *flag.Flag) { nSet = nSet || f.Name == "n" })
	if *d > 0 && !nSet {
		*n = 0
	}

	if *output != "text" && *output != "json" {
		fmt.Fprintf(stderr, "error: -o must be text or json\n")
		return 2
	}

	var payload []byte
	switch {
	case *body != "" && *bodyFile != "":
		fmt.Fprintln(stderr, "error: use -body or -body-file, not both")
		return 2
	case *bodyFile != "":
		b, err := os.ReadFile(*bodyFile)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		payload = b
	case *body != "":
		payload = []byte(*body)
	}

	runner, err := loadtest.New(loadtest.Config{
		URL:         fs.Arg(0),
		Method:      strings.ToUpper(*method),
		Header:      headers.h,
		Body:        payload,
		Requests:    *n,
		Duration:    *d,
		Concurrency: *c,
		Timeout:     *timeout,
		RateLimit:   *rate,
		HTTP2:       *http2,
		Untuned:     *untuned,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	// The first Ctrl-C cancels ctx: the run stops and we print what we have.
	// stop() then restores default signal handling, so a second Ctrl-C kills
	// the process right away if the report is somehow stuck.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	stopProgress := func() {}
	if !*quiet && isTerminal(os.Stderr) {
		stopProgress = startProgress(runner, stderr)
	}

	res := runner.Run(ctx)
	stopProgress()

	rep := report.New(runner.Config(), res)
	if *output == "json" {
		err = rep.WriteJSON(stdout)
	} else {
		err = rep.WriteText(stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error writing report: %v\n", err)
		return 1
	}
	if res.Interrupted {
		return 130 // conventional exit code for "terminated by Ctrl-C"
	}
	return 0
}

// startProgress redraws a one-line counter on stderr every half second and
// returns a function that stops it and clears the line.
func startProgress(r *loadtest.Runner, w io.Writer) (stop func()) {
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
				fmt.Fprint(w, "\r\033[K") // clear the progress line
				return
			case <-t.C:
				n := r.Completed()
				fmt.Fprintf(w, "\r\033[K  %d requests done, %.0f req/s so far",
					n, float64(n)/time.Since(start).Seconds())
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// isTerminal reports whether f is an interactive terminal, using only the
// standard library.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
