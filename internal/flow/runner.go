package flow

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/namnguyen3019/pelt/internal/loadtest"
	"github.com/namnguyen3019/pelt/internal/series"
	"github.com/namnguyen3019/pelt/internal/stages"
)

// Options are the run settings after the file's load block and the
// command-line flags have been combined.
type Options struct {
	Users      int           // virtual users, each looping flows (closed model)
	Duration   time.Duration // stop starting iterations after this long; 0 = no limit
	Iterations int           // total iterations across all users; 0 = no limit
	Timeout    time.Duration // per request, including reading the body
	Seed       uint64        // seeds each user's random source: flow picks, think, $rand
	HTTP2      bool
	// Stages move the number of users over the run and set its length, so
	// Duration must be 0. Users becomes their highest target.
	Stages *stages.Plan
}

// stageTick is how often the controller reads the stage target.
const stageTick = 100 * time.Millisecond

// DefaultTimeout applies when neither the file nor a flag sets one.
const DefaultTimeout = 10 * time.Second

// setupAttempts is how many times a user tries setup before giving up.
const setupAttempts = 4 // the first try plus 3 retries

// Options returns the run settings from the file's load block, with
// defaults filled in. Callers override fields from flags.
func (sc *Scenario) Options() Options {
	return Options{
		Users:      sc.Load.Users,
		Duration:   sc.Load.Duration,
		Iterations: sc.Load.Iterations,
		Timeout:    sc.Load.Timeout,
		Stages:     sc.Load.Stages,
	}
}

func (o *Options) validate(sc *Scenario) error {
	if o.Stages != nil {
		if o.Stages.Kind != stages.Level {
			return errors.New("flow stages move users, such as 1m:100; rate stages (100/s) are for single-URL mode only for now")
		}
		if o.Duration > 0 {
			return errors.New("stages set the run length, so a duration cannot be set too")
		}
		o.Users = int(math.Ceil(o.Stages.Max()))
	}
	if o.Users == 0 {
		o.Users = 1
	}
	if o.Users < 0 {
		return errors.New("users cannot be negative")
	}
	if o.Duration < 0 || o.Iterations < 0 {
		return errors.New("duration and iterations cannot be negative")
	}
	if o.Duration == 0 && o.Iterations == 0 && o.Stages == nil {
		return errors.New("set a duration or an iteration count (load.duration or load.iterations in the file, or -d or -i)")
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Timeout < 0 {
		return errors.New("timeout must be positive")
	}
	if sc.Data != nil && len(sc.Data.Rows) < o.Users {
		if o.Stages != nil {
			return fmt.Errorf("data file has %d rows but the stages reach %d users, who need one each (mode unique)", len(sc.Data.Rows), o.Users)
		}
		return fmt.Errorf("data file has %d rows but %d users need one each (mode unique)", len(sc.Data.Rows), o.Users)
	}
	return nil
}

// Runner runs a scenario. Create one with New and call Run once.
//
// The shape follows the single-URL runner: a dispatcher hands out work on
// an unbuffered channel and every worker records into its own recorder.
// Here a job is one flow iteration and a worker is a virtual user that
// keeps its cookies and setup values for the whole run. A user that is
// thinking is simply not asking for work.
type Runner struct {
	sc        *Scenario
	opts      Options
	transport *http.Transport
	trace     *httptrace.ClientTrace
	cumWeight []int // running total of flow weights, for picking

	// setupBackoff is the wait before setup retry n (times n).
	setupBackoff time.Duration

	iterations  atomic.Int64 // iterations finished, whatever the outcome
	checkFails  atomic.Int64
	active      atomic.Int64 // users running: started and not gone
	connsNew    atomic.Int64
	connsReused atomic.Int64

	// series counts per second; nil in a dry run.
	series atomic.Pointer[series.Counters]

	// With stages, target is how many users the stages want now: users
	// 1 to target run and the rest wait. Each change closes the channel
	// in changed and puts a new one there, waking every waiting user.
	// noMoreJobs is closed once the dispatcher stops, so waiting users
	// leave when an iteration count ends the run early.
	target     atomic.Int64
	changed    atomic.Pointer[chan struct{}]
	noMoreJobs chan struct{}
}

// New validates the options and prepares a Runner.
func New(sc *Scenario, opts Options) (*Runner, error) {
	if err := opts.validate(sc); err != nil {
		return nil, err
	}
	r := &Runner{sc: sc, opts: opts, setupBackoff: time.Second, noMoreJobs: make(chan struct{})}
	ch := make(chan struct{})
	r.changed.Store(&ch)
	// One tuned transport shared by every user: the pool is sized to the
	// user count so each user keeps a warm connection, as in single-URL
	// mode. Each user gets its own http.Client and cookie jar on top.
	r.transport = loadtest.NewTransport(loadtest.Config{
		Concurrency: opts.Users,
		Timeout:     opts.Timeout,
		HTTP2:       opts.HTTP2,
	})
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
	total := 0
	for _, fl := range sc.Flows {
		total += fl.Weight
		r.cumWeight = append(r.cumWeight, total)
	}
	return r, nil
}

// Options returns the validated options.
func (r *Runner) Options() Options { return r.opts }

// Progress is a snapshot of a run in progress, for the live line.
type Progress struct {
	Iterations int64
	CheckFails int64
	Active     int64
	// LastSecond holds the counts of the last whole second.
	LastSecond series.Count
	Elapsed    time.Duration
	// With stages, Stage is the stage in effect (1-based) and Target the
	// users it asks for now.
	Stage  int
	Target int64
}

// Progress reads the run so far. Safe to call while Run is in progress.
func (r *Runner) Progress() Progress {
	p := Progress{Iterations: r.iterations.Load(), CheckFails: r.checkFails.Load(), Active: r.active.Load()}
	if c := r.series.Load(); c != nil {
		p.Elapsed = time.Since(c.Start())
		p.LastSecond = c.Get(int(p.Elapsed/time.Second) - 1)
		if plan := r.opts.Stages; plan != nil {
			p.Stage, p.Target = plan.StageAt(p.Elapsed), r.target.Load()
		}
	}
	return p
}

// Run executes the scenario and blocks until it ends.
//
// The -d deadline stops users from starting new steps: a request in flight
// finishes, remaining think time is skipped, and the iteration counts as
// cut by the end of the run. Cancelling ctx (Ctrl-C) also aborts requests
// in flight; Run still returns what was recorded, with Interrupted set.
func (r *Runner) Run(ctx context.Context) *Result {
	stopCtx := ctx
	length := r.opts.Duration
	if r.opts.Stages != nil {
		length = r.opts.Stages.Total()
	}
	if length > 0 {
		var cancel context.CancelFunc
		stopCtx, cancel = context.WithTimeout(ctx, length)
		defer cancel()
	}
	reqCtx := httptrace.WithClientTrace(ctx, r.trace)

	jobs := make(chan struct{}) // unbuffered: a job goes only to a free user
	recs := make([]*recorder, r.opts.Users)
	var wg sync.WaitGroup
	start := time.Now()
	counters := series.NewCounters(start)
	r.series.Store(counters)
	sampler := series.StartSampler(start, r.level)
	if plan := r.opts.Stages; plan != nil {
		r.setTarget(plan.Target(0))
		go r.control(stopCtx, start)
	}
	for i := range recs {
		recs[i] = newRecorder(r.sc)
		v := r.newVU(i+1, recs[i])
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.run(reqCtx, stopCtx, jobs)
		}()
	}
	allGone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allGone)
	}()

	r.dispatch(stopCtx, jobs, allGone)
	close(r.noMoreJobs)
	<-allGone
	elapsed := time.Since(start)

	res := merge(r.sc, recs)
	res.Options = r.opts
	res.Elapsed = elapsed
	res.Series = series.Collect(counters, sampler, elapsed)
	res.Series.Flow = true
	res.Series.Staged = r.opts.Stages != nil
	res.Interrupted = ctx.Err() != nil
	res.ConnsNew = r.connsNew.Load()
	res.ConnsReused = r.connsReused.Load()
	return res
}

// dispatch hands out one job per iteration until the iteration count is
// reached, the run stops, or every user has gone (for example all failed
// setup), then closes jobs.
func (r *Runner) dispatch(stop context.Context, jobs chan<- struct{}, allGone <-chan struct{}) {
	defer close(jobs)
	for i := 0; r.opts.Iterations == 0 || i < r.opts.Iterations; i++ {
		select {
		case <-stop.Done():
			return
		case <-allGone:
			return
		case jobs <- struct{}{}:
		}
	}
}

// level is what the sampler notes at the end of each second: users
// running and, with stages, the target and stage at the second's midpoint
// (see loadtest's Runner.level).
func (r *Runner) level(at time.Duration) series.Level {
	lv := series.Level{Active: int(r.active.Load())}
	if plan := r.opts.Stages; plan != nil {
		mid := (at - 1).Truncate(time.Second) + time.Second/2
		lv.Target, lv.Stage = plan.Target(mid), plan.StageAt(mid)
	}
	return lv
}

// control reads the stage target every stageTick until the run stops.
func (r *Runner) control(stop context.Context, start time.Time) {
	t := time.NewTicker(stageTick)
	defer t.Stop()
	for {
		select {
		case <-stop.Done():
			return
		case <-t.C:
			r.setTarget(r.opts.Stages.Target(time.Since(start)))
		}
	}
}

// setTarget publishes a new user target and wakes every waiting user if
// it changed.
func (r *Runner) setTarget(f float64) {
	n := int64(math.Round(f))
	if r.target.Swap(n) == n {
		return
	}
	ch := make(chan struct{})
	close(*r.changed.Swap(&ch))
}

// pick chooses a flow by weight.
func (r *Runner) pick(n int) *Flow {
	for i, c := range r.cumWeight {
		if n < c {
			return r.sc.Flows[i]
		}
	}
	return r.sc.Flows[len(r.sc.Flows)-1]
}

// sleepCtx waits for d or until ctx is done. It reports whether the full
// wait happened.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
