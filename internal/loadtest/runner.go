package loadtest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

// Runner executes a load test. Create one with New and call Run once.
//
// Design in short:
//
//	dispatcher ──jobs chan──▶ N workers ──▶ per-worker recorder
//	(paces by -n/-d/-rate)    (http.Client)   (merged after the run)
//
// The dispatcher decides *when* a request may start; the workers decide
// *how many* run at once. Each worker records into its own recorder, so the
// hot path never takes a lock or sends on a shared results channel. The only
// shared writes are a few atomic counters used for live progress.
type Runner struct {
	cfg    Config
	client *http.Client
	trace  *httptrace.ClientTrace

	completed   atomic.Int64 // requests that finished (success or error)
	connsNew    atomic.Int64 // TCP connections dialled
	connsReused atomic.Int64 // requests sent on an already-used connection
}

// New validates cfg and prepares a Runner.
func New(cfg Config) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	r := &Runner{cfg: cfg}
	r.client = &http.Client{
		Transport: newTransport(cfg),
		Timeout:   cfg.Timeout,
	}
	// httptrace hooks show what the transport is doing underneath: how many
	// TCP connections were actually dialled, and how many requests rode on
	// an existing keep-alive connection. This is how the report proves the
	// transport tuning is doing its job.
	//
	// Dials are counted at ConnectDone rather than from GotConn's Reused
	// flag, because the transport races a new dial against a connection
	// coming back to the idle pool; a dial that loses the race still opens
	// a socket, which then sits in the pool for a later request.
	r.trace = &httptrace.ClientTrace{
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				r.connsNew.Add(1)
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				r.connsReused.Add(1)
			}
		},
	}
	return r, nil
}

// Config returns the validated configuration (with defaults filled in).
func (r *Runner) Config() Config { return r.cfg }

// Completed reports how many requests have finished so far. It is safe to
// call from another goroutine while Run is in progress.
func (r *Runner) Completed() int64 { return r.completed.Load() }

// Run executes the test and blocks until it finishes or ctx is cancelled.
//
// Two kinds of stop exist:
//   - The -d deadline only stops *dispatching*. Requests already in flight
//     are allowed to finish (bounded by the timeout) so their latency counts.
//   - Cancelling ctx (Ctrl-C) stops dispatching and aborts in-flight
//     requests. Run still returns everything recorded so far, with
//     Interrupted set.
func (r *Runner) Run(ctx context.Context) *Result {
	dispatchCtx := ctx
	if r.cfg.Duration > 0 {
		var cancel context.CancelFunc
		dispatchCtx, cancel = context.WithTimeout(ctx, r.cfg.Duration)
		defer cancel()
	}

	// Requests are bound to ctx (not dispatchCtx) and carry the trace hooks.
	reqCtx := httptrace.WithClientTrace(ctx, r.trace)

	// Unbuffered on purpose: a job is only handed over when a worker is free
	// to take it. Nothing piles up in a queue, so the -d deadline and Ctrl-C
	// take effect immediately, and -rate pacing is not smoothed by a buffer.
	jobs := make(chan struct{})

	perWorker := 0
	if r.cfg.Requests > 0 {
		perWorker = r.cfg.Requests/r.cfg.Concurrency + 1
	}

	recorders := make([]*recorder, r.cfg.Concurrency)
	var wg sync.WaitGroup
	start := time.Now()

	for i := range recorders {
		rec := newRecorder(perWorker)
		recorders[i] = rec
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs { // exits when the dispatcher closes jobs
				r.do(reqCtx, rec)
			}
		}()
	}

	r.dispatch(dispatchCtx, jobs) // returns after closing jobs
	wg.Wait()
	elapsed := time.Since(start)

	res := merge(recorders)
	res.Elapsed = elapsed
	res.Interrupted = ctx.Err() != nil
	res.ConnsNew = r.connsNew.Load()
	res.ConnsReused = r.connsReused.Load()
	return res
}

// dispatch hands out one job per request until the request count is reached
// or ctx is done, then closes jobs so the workers drain and exit.
//
// With a rate limit, request i is scheduled at start + i*interval. Pacing
// against an absolute schedule (instead of sleeping a fixed interval after
// each send) keeps the long-run rate exact even when the timer fires late,
// and it lets high rates work despite the OS timer's coarse resolution: if
// we fall behind, the next few jobs go out back-to-back to catch up.
func (r *Runner) dispatch(ctx context.Context, jobs chan<- struct{}) {
	defer close(jobs)

	var interval time.Duration
	if r.cfg.RateLimit > 0 {
		interval = time.Duration(float64(time.Second) / r.cfg.RateLimit)
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	start := time.Now()

	for i := 0; r.cfg.Requests == 0 || i < r.cfg.Requests; i++ {
		if interval > 0 {
			if wait := time.Until(start.Add(time.Duration(i) * interval)); wait > 0 {
				timer.Reset(wait) // safe since Go 1.23: no stale tick to drain
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case jobs <- struct{}{}:
		}
	}
}

// do sends one request and records the outcome in rec. Latency is measured
// from just before the request is sent until the whole body has been read,
// which is what a real client experiences.
func (r *Runner) do(ctx context.Context, rec *recorder) {
	req, err := r.newRequest(ctx)
	if err != nil {
		rec.recordError(err)
		r.completed.Add(1)
		return
	}

	start := time.Now()
	resp, err := r.client.Do(req)
	if err == nil {
		// Read the body to EOF and close it. Skipping either step stops the
		// connection from going back to the pool, silently defeating
		// keep-alive.
		var n int64
		n, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err == nil {
			rec.recordSuccess(resp.StatusCode, time.Since(start), n)
			r.completed.Add(1)
			return
		}
	}

	if ctx.Err() != nil {
		// Aborted by Ctrl-C, not a failure of the target. Kept out of the
		// error rate and latency numbers.
		rec.aborted++
		return
	}
	rec.recordError(err)
	r.completed.Add(1)
}

func (r *Runner) newRequest(ctx context.Context) (*http.Request, error) {
	var body io.Reader
	if len(r.cfg.Body) > 0 {
		// A fresh reader per request: the byte slice is shared read-only.
		body = bytes.NewReader(r.cfg.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.cfg.Method, r.cfg.URL, body)
	if err != nil {
		return nil, err
	}
	if r.cfg.Header != nil {
		req.Header = r.cfg.Header.Clone()
		if host := req.Header.Get("Host"); host != "" {
			req.Host = host // Go ignores a Host header; it must go here.
		}
	}
	return req, nil
}
