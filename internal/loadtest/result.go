package loadtest

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/namnguyen3019/pelt/internal/series"
)

// Result is the raw outcome of a run. Turn it into numbers with the stats
// package and into text or JSON with the report package.
type Result struct {
	Elapsed     time.Duration
	Interrupted bool // the run was cancelled (Ctrl-C) before finishing

	Total     int // requests that completed: Successes + errors
	Successes int // requests that got a full HTTP response (any status)
	Aborted   int // in-flight requests cut off by cancellation (not in Total)

	Latencies []time.Duration // one per successful request
	// Finish holds, beside each latency, when that request finished in
	// milliseconds since the start, for the per-second series.
	Finish      []uint32
	StatusCodes map[int]int
	Errors      map[string]int // error kind -> count
	BytesRead   int64

	ConnsNew    int64 // TCP connections dialled
	ConnsReused int64 // requests sent on a previously used connection

	// Series is the run second by second: counts and sampled levels.
	// report.New turns it into rows with latency percentiles.
	Series series.Raw
}

// ErrorCount is the number of requests that failed at the transport level
// (no HTTP response). HTTP 4xx/5xx responses are not errors here; they are
// reported in StatusCodes.
func (r *Result) ErrorCount() int { return r.Total - r.Successes }

// RequestsPerSecond is completed requests divided by wall-clock time.
func (r *Result) RequestsPerSecond() float64 {
	if r.Elapsed <= 0 {
		return 0
	}
	return float64(r.Total) / r.Elapsed.Seconds()
}

// recorder collects results for a single worker. It is only ever touched by
// that worker's goroutine, so it needs no locking.
type recorder struct {
	latencies []time.Duration
	finish    []uint32 // parallel to latencies
	status    map[int]int
	errors    map[string]int
	bytes     int64
	aborted   int
}

func newRecorder(capacity int) *recorder {
	return &recorder{
		latencies: make([]time.Duration, 0, capacity),
		finish:    make([]uint32, 0, capacity),
		status:    make(map[int]int),
		errors:    make(map[string]int),
	}
}

func (rec *recorder) recordSuccess(code int, latency time.Duration, finish uint32, n int64) {
	rec.latencies = append(rec.latencies, latency)
	rec.finish = append(rec.finish, finish)
	rec.status[code]++
	rec.bytes += n
}

func (rec *recorder) recordError(err error) {
	rec.errors[ClassifyError(err)]++
}

// merge combines the per-worker recorders once all workers have exited.
func merge(recs []*recorder) *Result {
	res := &Result{StatusCodes: make(map[int]int), Errors: make(map[string]int)}
	n := 0
	for _, rec := range recs {
		n += len(rec.latencies)
	}
	res.Latencies = make([]time.Duration, 0, n)
	res.Finish = make([]uint32, 0, n)

	for _, rec := range recs {
		res.Latencies = append(res.Latencies, rec.latencies...)
		res.Finish = append(res.Finish, rec.finish...)
		for code, c := range rec.status {
			res.StatusCodes[code] += c
			res.Successes += c
		}
		for kind, c := range rec.errors {
			res.Errors[kind] += c
			res.Total += c
		}
		res.BytesRead += rec.bytes
		res.Aborted += rec.aborted
	}
	res.Total += res.Successes
	return res
}

// ClassifyError maps a request error to a short, stable category so the
// report can group thousands of failures into a handful of lines.
func ClassifyError(err error) string {
	var netErr net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case isAny(err, errConnRefused):
		return "connection refused"
	case isAny(err, errConnReset),
		errors.Is(err, syscall.EPIPE),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF):
		return "connection reset"
	case isAny(err, errAddrNotAvail):
		return "out of local ports"
	case isAny(err, errTooManyOpen):
		return "too many open files"
	case errors.As(err, &dnsErr):
		return "dns lookup failed"
	case strings.Contains(err.Error(), "tls:"), strings.Contains(err.Error(), "x509:"):
		return "tls error"
	default:
		return "other: " + rootMessage(err)
	}
}

// isAny reports whether err matches any of targets.
func isAny(err error, targets []error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}

// rootMessage returns the innermost error message, trimmed, so that
// "other" errors stay readable and group together.
func rootMessage(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			break
		}
		err = next
	}
	msg := err.Error()
	if len(msg) > 80 {
		msg = msg[:77] + "..."
	}
	return msg
}
