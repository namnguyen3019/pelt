// Command pelt fires concurrent HTTP requests at a URL and reports
// throughput and latency.
//
//	pelt -n 10000 -c 200 http://127.0.0.1:8080/
//	pelt -d 30s -c 500 -rate 2000 http://127.0.0.1:8080/
//	pelt -d 30s -threshold 'p95<300ms' -threshold 'errors<1%' http://127.0.0.1:8080/
//	pelt run -u 50 -d 2m shop.json      # flow mode, see docs/flows.md
//
// Exit codes: 0 pass, 1 runtime error, 2 bad flags, 3 a threshold was missed
// or no request got a response, 130 interrupted.
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
	"github.com/namnguyen3019/pelt/internal/threshold"
)

// Exit codes. 130 (interrupted) is the shell convention for Ctrl-C.
const (
	exitOK          = 0
	exitError       = 1 // runtime failure, e.g. an unreadable body file
	exitUsage       = 2 // bad flags, file or threshold; nothing was sent
	exitFailed      = 3 // a threshold was missed, or nothing got a response
	exitInterrupted = 130
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

// thresholdFlag implements flag.Value so -threshold can be repeated. Each
// value is parsed as it is set, so a typo fails before any load is sent.
type thresholdFlag []threshold.Threshold

func (f *thresholdFlag) String() string { return "" }

func (f *thresholdFlag) Set(v string) error {
	t, err := threshold.Parse(v)
	if err != nil {
		return err
	}
	*f = append(*f, t)
	return nil
}

// checkSingleURL rejects thresholds that only make sense in flow mode.
func (f thresholdFlag) checkSingleURL() error {
	for _, t := range f {
		if t.Scope != "" {
			return fmt.Errorf("threshold %q: scopes like %q are for flow mode; single-URL mode measures the whole run", t.Expr, t.Scope+":")
		}
		if t.FlowOnly() {
			return fmt.Errorf("threshold %q: %s only exists in flow mode", t.Expr, t.Metric)
		}
	}
	return nil
}

// finish turns a verdict into the exit code, and with JSON output prints
// a one-line failure note to stderr so it shows in CI logs.
func finish(v report.Verdict, interrupted, jsonOut bool, stderr io.Writer) int {
	if !v.Passed() && jsonOut {
		fmt.Fprintf(stderr, "pelt: FAIL: %s\n", v.Summary())
	}
	switch {
	case interrupted:
		return exitInterrupted
	case !v.Passed():
		return exitFailed
	}
	return exitOK
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run holds the whole program so it can return an exit code (and be tested)
// instead of calling os.Exit from deep inside.
func run(args []string, stdout, stderr io.Writer) int {
	// Flow mode is only ever the first argument. A URL has to start with
	// http:// or https://, so nothing that worked before changes meaning.
	if len(args) > 0 && args[0] == "run" {
		return runFlow(args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("pelt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: pelt [flags] <url>\n       pelt run [flags] <scenario.json>   (flow mode; pelt run -h for its flags)\n\nFlags:\n")
		fs.PrintDefaults()
	}

	var (
		headers    headerFlag
		thresholds thresholdFlag
		n          = fs.Int("n", 200, "total number of requests (0 = no limit, needs -d)")
		c          = fs.Int("c", 50, "concurrency: number of worker goroutines")
		d          = fs.Duration("d", 0, "run for this long, e.g. 30s (overrides the default -n)")
		rate       = fs.Float64("rate", 0, "max requests per second across all workers (0 = unlimited)")
		method     = fs.String("m", "GET", "HTTP method")
		body       = fs.String("body", "", "request body")
		bodyFile   = fs.String("body-file", "", "read the request body from this file")
		timeout    = fs.Duration("t", 10*time.Second, "per-request timeout")
		http2      = fs.Bool("http2", false, "allow HTTP/2 (default forces HTTP/1.1)")
		untuned    = fs.Bool("untuned", false, "use Go's stock connection pool limits (for comparison)")
		output     = fs.String("o", "text", "output format: text or json")
		seriesOut  = fs.String("series", "", "also write one row per second of the run to this CSV file")
		quiet      = fs.Bool("quiet", false, "hide the live progress line")
	)
	fs.Var(&headers, "H", "request header 'Name: value' (repeatable)")
	fs.Var(&thresholds, "threshold", "pass/fail target such as 'p95<300ms', 'errors<1%' or 'rps>500' (repeatable); exit 3 if missed")

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
	if err := thresholds.checkSingleURL(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitUsage
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

	seriesFile, err := createSeries(*seriesOut)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	defer seriesFile.Close()

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
	verdict := rep.Check(thresholds)
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
		fmt.Fprintf(stderr, "error writing %s: %v\n", *seriesOut, err)
		return exitError
	}
	return finish(verdict, res.Interrupted, *output == "json", stderr)
}

// createSeries opens the -series file before the run, so a bad path fails
// before any load is sent. It returns nil for an empty path.
func createSeries(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	return os.Create(path)
}

// writeSeries writes the CSV to f with write and closes f. A nil f is a
// run without -series.
func writeSeries(f *os.File, write func(io.Writer) error) error {
	if f == nil {
		return nil
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// startProgress redraws a one-line counter on stderr every half second and
// returns a function that stops it and clears the line.
func startProgress(r *loadtest.Runner, w io.Writer) (stop func()) {
	return redraw(w, func() string { return progressLine(r.Progress()) })
}

// progressLine is the live line for single-URL mode: requests so far, the
// rate in the last whole second, and errors so far.
func progressLine(p loadtest.Progress) string {
	return fmt.Sprintf("%s requests, %s req/s last second, %s errors",
		report.GroupInt(p.Completed), report.GroupInt(int64(p.LastSecond.Requests)), report.GroupInt(p.Errors))
}

// redraw prints line() over itself on w every half second until stopped,
// then clears it.
func redraw(w io.Writer, line func() string) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				fmt.Fprint(w, "\r\033[K") // clear the progress line
				return
			case <-t.C:
				fmt.Fprintf(w, "\r\033[K  %s", line())
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
