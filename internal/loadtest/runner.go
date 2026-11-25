package loadtest

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/namnguyen3019/pelt/internal/series"
	"github.com/namnguyen3019/pelt/internal/stages"
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
	inFlight    atomic.Int64 // requests sent and not yet finished
	connsNew    atomic.Int64 // TCP connections dialled
	connsReused atomic.Int64 // requests sent on an already-used connection

	// counters is set when Run starts; until then progress reads nothing.
	counters atomic.Pointer[series.Counters]

	// freed wakes the dispatcher when a request finishes, so a level stage's
	// gate opens as soon as there is room. Buffered by one; workers never
	// block on it. Nil without level stages.
	freed chan struct{}
}

// gateTick is how often the dispatcher looks at the stage target again
// while it waits, so a rising target is picked up without a request
// having to finish first.
const gateTick = 100 * time.Millisecond

// New validates cfg and prepares a Runner.
func New(cfg Config) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	r := &Runner{cfg: cfg}
	if cfg.Stages != nil && cfg.Stages.Kind == stages.Level {
		r.freed = make(chan struct{}, 1)
	}
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

// Progress is a snapshot of a run in progress, for the live line.
type Progress struct {
	Completed int64
	Errors    int64
	// LastSecond holds the counts of the last whole second, and Elapsed
	// the time since the start.
	LastSecond series.Count
	Elapsed    time.Duration
	// Active is requests in flight now. With stages, Stage is the stage in
	// effect (1-based) and Target what it asks for now.
	Active int64
	Stage  int
	Target float64
}

// Progress reads the run so far. It is safe to call while Run is going.
func (r *Runner) Progress() Progress {
	p := Progress{Completed: r.completed.Load(), Active: r.inFlight.Load()}
	c := r.counters.Load()
	if c == nil {
		return p
	}
	p.Elapsed = time.Since(c.Start())
	if plan := r.cfg.Stages; plan != nil {
		p.Stage, p.Target = plan.StageAt(p.Elapsed), plan.Target(p.Elapsed)
	}
	now := int(p.Elapsed / time.Second)
	for t := 0; t <= now; t++ {
		p.Errors += int64(c.Get(t).Errors)
	}
	p.LastSecond = c.Get(now - 1)
	return p
}

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
	length := r.cfg.Duration
	if r.cfg.Stages != nil {
		length = r.cfg.Stages.Total()
	}
	if length > 0 {
		var cancel context.CancelFunc
		dispatchCtx, cancel = context.WithTimeout(ctx, length)
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
	counters := series.NewCounters(start)
	r.counters.Store(counters)
	sampler := series.StartSampler(start, r.level)

	for i := range recorders {
		rec := newRecorder(perWorker)
		recorders[i] = rec
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs { // exits when the dispatcher closes jobs
				r.inFlight.Add(1)
				r.do(reqCtx, rec, counters)
				r.inFlight.Add(-1)
				if r.freed != nil {
					select {
					case r.freed <- struct{}{}:
					default:
					}
				}
			}
		}()
	}

	r.dispatch(dispatchCtx, start, jobs) // returns after closing jobs
	wg.Wait()
	elapsed := time.Since(start)

	res := merge(recorders)
	res.Elapsed = elapsed
	res.Series = series.Collect(counters, sampler, elapsed)
	res.Series.Staged = r.cfg.Stages != nil
	res.Interrupted = ctx.Err() != nil
	res.ConnsNew = r.connsNew.Load()
	res.ConnsReused = r.connsReused.Load()
	return res
}

// level is what the sampler notes at the end of each second: requests in
// flight and, with stages, the target and the stage at the second's
// midpoint. The midpoint is the average target of a second on a ramp, and
// it gives a second to one stage: a stage that starts at 10s owns second
// 10, and the stage before it owns second 9.
func (r *Runner) level(at time.Duration) series.Level {
	lv := series.Level{Active: int(r.inFlight.Load())}
	if plan := r.cfg.Stages; plan != nil {
		mid := (at - 1).Truncate(time.Second) + time.Second/2
		lv.Target, lv.Stage = plan.Target(mid), plan.StageAt(mid)
	}
	return lv
}

// dispatch hands out one job per request until the request count is reached
// or ctx is done, then closes jobs so the workers drain and exit.
//
// With a rate limit, request i is scheduled at start + i*interval. Pacing
// against an absolute schedule (instead of sleeping a fixed interval after
// each send) keeps the long-run rate exact even when the timer fires late,
// and it lets high rates work despite the OS timer's coarse resolution: if
// we fall behind, the next few jobs go out back-to-back to catch up.
//
// Rate stages keep the absolute schedule, with a moving rate: request i is
// due when the stages have asked for i+0.5 requests in all (the area under
// the rate curve). Level stages add a gate: a job goes out only while
// fewer requests are in flight than the stage asks for right now.
func (r *Runner) dispatch(ctx context.Context, start time.Time, jobs chan<- struct{}) {
	defer close(jobs)

	var interval time.Duration
	if r.cfg.RateLimit > 0 {
		interval = time.Duration(float64(time.Second) / r.cfg.RateLimit)
	}
	plan := r.cfg.Stages
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	waitUntil := func(at time.Time) bool {
		wait := time.Until(at)
		if wait <= 0 {
			return true
		}
		timer.Reset(wait) // safe since Go 1.23: no stale tick to drain
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}

	sent := int64(0)
	for i := 0; r.cfg.Requests == 0 || i < r.cfg.Requests; i++ {
		if interval > 0 && !waitUntil(start.Add(time.Duration(i)*interval)) {
			return
		}
		if plan != nil && plan.Kind == stages.Rate {
			due, ok := plan.TimeFor(float64(i) + 0.5)
			if !ok || !waitUntil(start.Add(due)) {
				return
			}
		}
		if plan != nil && plan.Kind == stages.Level && !r.gate(ctx, start, sent) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case jobs <- struct{}{}:
			sent++
		}
	}
}

// gate waits until fewer requests are in flight than the level stage asks
// for now. In flight is counted as jobs sent minus requests completed,
// both known exactly here: a worker counts its own request as in flight
// only after it picks the job up, which would let the dispatcher overshoot.
// It reports false if ctx ends first.
func (r *Runner) gate(ctx context.Context, start time.Time, sent int64) bool {
	var tick *time.Ticker
	for {
		target := int64(math.Round(r.cfg.Stages.Target(time.Since(start))))
		if sent-r.completed.Load() < target {
			if tick != nil {
				tick.Stop()
			}
			return true
		}
		if tick == nil {
			tick = time.NewTicker(gateTick)
			defer tick.Stop()
		}
		select {
		case <-ctx.Done():
			return false
		case <-r.freed:
		case <-tick.C:
		}
	}
}

// do sends one request and records the outcome in rec. Latency is measured
// from just before the request is sent until the whole body has been read,
// which is what a real client experiences.
func (r *Runner) do(ctx context.Context, rec *recorder, counters *series.Counters) {
	req, err := r.newRequest(ctx)
	if err != nil {
		rec.recordError(err)
		counters.Error(counters.Now())
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
			latency := time.Since(start)
			finish := counters.Now()
			rec.recordSuccess(resp.StatusCode, latency, finish, n)
			counters.Response(finish, resp.StatusCode)
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
	counters.Error(counters.Now())
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
