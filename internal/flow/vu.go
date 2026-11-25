package flow

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strings"
	"time"
)

// maxCaptureBody caps how much of a response body is kept for captures.
// The rest is still read (and counted) so the connection can be reused.
const maxCaptureBody = 16 << 20

// vu is one virtual user. Only its own goroutine touches it.
type vu struct {
	r      *Runner
	sc     *Scenario
	id     int // 1-based
	rec    *recorder
	client *http.Client
	rng    *rand.Rand

	row       map[string]string // this user's data row
	setupVars map[string]string // captured in setup; live for the whole run
	iterVars  map[string]string // captured in the current iteration
	saveTo    map[string]string // where captures go right now
	iteration int               // 1-based count of this user's iterations
	running   bool              // counted in the runner's active users

	trace func(*StepTrace) // dry run only
}

func (r *Runner) newVU(id int, rec *recorder) *vu {
	v := &vu{
		r:         r,
		sc:        r.sc,
		id:        id,
		rec:       rec,
		rng:       rand.New(rand.NewPCG(r.opts.Seed, uint64(id))),
		row:       r.sc.Data.row(id),
		setupVars: map[string]string{},
		iterVars:  map[string]string{},
	}
	v.client = &http.Client{Transport: r.transport, Timeout: r.opts.Timeout}
	if r.sc.Cookies {
		jar, _ := cookiejar.New(nil) // never fails with nil options
		v.client.Jar = jar
	}
	return v
}

// Values implementation for templates. Lookup order: values captured in
// this iteration, then in setup, then the data row, then vars.
func (v *vu) Lookup(name string) (string, bool) {
	if s, ok := v.iterVars[name]; ok {
		return s, true
	}
	if s, ok := v.setupVars[name]; ok {
		return s, true
	}
	if s, ok := v.row[name]; ok {
		return s, true
	}
	s, ok := v.sc.Vars[name]
	return s, ok
}

func (v *vu) VU() int            { return v.id }
func (v *vu) Iteration() int     { return v.iteration }
func (v *vu) RandIntN(n int) int { return v.rng.IntN(n) }
func (v *vu) UUID() string {
	var b [16]byte
	for i := 0; i < 16; i += 8 {
		u := v.rng.Uint64()
		for j := range 8 {
			b[i+j] = byte(u >> (8 * j))
		}
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// outcome is how a run through a list of steps ended.
type outcome int

const (
	finished outcome = iota // every step ran
	failed                  // a failure aborted the rest
	cut                     // the run ended (deadline or Ctrl-C) part way
)

// run is the user's life: setup once, then one flow iteration per job.
// With stages, a user waits for its turn before setup and before every
// iteration: when the target drops below its number it parks, keeping its
// cookies and setup values, and when the target rises again it carries on
// without running setup again.
func (v *vu) run(reqCtx, stopCtx context.Context, jobs <-chan struct{}) {
	defer v.setRunning(false)
	if !v.waitTurn(stopCtx) || !v.setup(reqCtx, stopCtx) {
		return
	}
	for v.waitTurn(stopCtx) {
		if _, ok := <-jobs; !ok {
			return
		}
		v.iteration++
		fl := v.r.pick(v.rng.IntN(v.r.cumWeight[len(v.r.cumWeight)-1]))
		out := v.runFlow(reqCtx, stopCtx, fl, true)
		// The series counts iterations that ended on their own, completed
		// or aborted; one cut by the end of the run did not finish.
		if c := v.r.series.Load(); c != nil && out != cut {
			c.Iteration(c.Now(), out == failed)
		}
		v.r.iterations.Add(1)
	}
}

// over reports whether the stages want fewer users than this one's number.
func (v *vu) over() bool {
	return v.r.opts.Stages != nil && int64(v.id) > v.r.target.Load()
}

// setRunning keeps the runner's count of active users.
func (v *vu) setRunning(b bool) {
	if v.running != b {
		v.running = b
		if b {
			v.r.active.Add(1)
		} else {
			v.r.active.Add(-1)
		}
	}
}

// waitTurn returns at once without stages. With stages it parks the user
// while the target is below its number. It reports false when the run
// stops or no more iterations will be handed out.
func (v *vu) waitTurn(stop context.Context) bool {
	for {
		// Load the channel before the target, so a change between the two
		// still wakes this user.
		ch := v.r.changed.Load()
		if !v.over() {
			v.setRunning(true)
			return true
		}
		v.setRunning(false)
		select {
		case <-stop.Done():
			return false
		case <-v.r.noMoreJobs:
			return false
		case <-*ch:
		}
	}
}

// think waits d, as sleepCtx does, but with stages it stops waiting as
// soon as this user is over the target: the iteration finishes without
// its remaining think time and the user parks. It reports false only when
// the run stops.
func (v *vu) think(stop context.Context, d time.Duration) bool {
	if v.r.opts.Stages == nil {
		return sleepCtx(stop, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		ch := v.r.changed.Load()
		if v.over() {
			return stop.Err() == nil
		}
		select {
		case <-stop.Done():
			return false
		case <-t.C:
			return true
		case <-*ch:
		}
	}
}

// setup runs the setup steps, retrying a failure with a growing backoff.
// It reports whether the user may go on to run flows.
func (v *vu) setup(reqCtx, stopCtx context.Context) bool {
	if len(v.sc.Setup) == 0 {
		return true
	}
	for attempt := range setupAttempts {
		if attempt > 0 && !sleepCtx(stopCtx, time.Duration(attempt)*v.r.setupBackoff) {
			return false
		}
		clear(v.setupVars)
		v.saveTo = v.setupVars
		out, _ := v.runSteps(reqCtx, stopCtx, v.sc.Setup, true)
		switch out {
		case finished:
			return true
		case cut:
			return false
		}
	}
	v.rec.setupFail = true
	return false
}

// runFlow runs one iteration of fl and records how it went.
func (v *vu) runFlow(reqCtx, stopCtx context.Context, fl *Flow, think bool) outcome {
	fr := &v.rec.flows[fl.ID]
	fr.Started++
	clear(v.iterVars)
	v.saveTo = v.iterVars
	start := time.Now()
	out, flowTime := v.runSteps(reqCtx, stopCtx, fl.Steps, think)
	switch out {
	case finished:
		fr.Completed++
		fr.FlowTimes = append(fr.FlowTimes, flowTime)
		fr.WallTimes = append(fr.WallTimes, time.Since(start))
	case failed:
		fr.Aborted++
	case cut:
		fr.Cut++
	}
	return out
}

// runSteps runs steps in order. flowTime sums the request times.
func (v *vu) runSteps(reqCtx, stopCtx context.Context, steps []*Step, think bool) (out outcome, flowTime time.Duration) {
	for i, st := range steps {
		if stopCtx.Err() != nil {
			return cut, flowTime
		}
		res := v.do(reqCtx, st)
		flowTime += res.Latency
		switch {
		case res.Aborted:
			return cut, flowTime
		case res.Err != nil || (res.Failed() && !st.ContinueOnFail):
			v.rec.steps[st.ID].AbortedIterations++
			return failed, flowTime
		}
		if think && st.Think.Max > 0 {
			d := st.Think.Min
			if st.Think.Max > st.Think.Min {
				d += time.Duration(v.rng.Int64N(int64(st.Think.Max - st.Think.Min + 1)))
			}
			if !v.think(stopCtx, d) && i < len(steps)-1 {
				return cut, flowTime
			}
		}
	}
	return finished, flowTime
}

// StepTrace describes one request in detail, for the dry run.
type StepTrace struct {
	Step     *Step
	Method   string
	URL      string
	Header   http.Header
	Body     string
	Status   int
	Latency  time.Duration
	Bytes    int64
	RespBody []byte
	Err      error // transport error, no response
	Aborted  bool  // cut off by Ctrl-C
	CheckErr error // status check failed
	Captures []CaptureTrace
}

// CaptureTrace is one capture's result.
type CaptureTrace struct {
	As    string
	Value string
	Err   error
}

// Failed reports whether the step's check or one of its captures failed.
func (t *StepTrace) Failed() bool {
	if t.CheckErr != nil {
		return true
	}
	for _, c := range t.Captures {
		if c.Err != nil {
			return true
		}
	}
	return false
}

// do sends one step's request, records the result and saves captures.
func (v *vu) do(ctx context.Context, st *Step) *StepTrace {
	sr := &v.rec.steps[st.ID]
	t := &StepTrace{Step: st, Method: st.Method}

	u := st.URL.Render(v)
	if !isAbsolute(u) {
		if !strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "?") {
			u = "/" + u
		}
		u = v.sc.BaseURL + u
	}
	t.URL = u
	var body io.Reader
	if st.Body != nil {
		t.Body = st.Body.Render(v)
		body = strings.NewReader(t.Body)
	}
	req, err := http.NewRequestWithContext(ctx, st.Method, u, body)
	if err != nil {
		t.Err = err
		sr.recordError(err)
		v.countError()
		v.emit(t)
		return t
	}
	for _, h := range st.Header {
		val := h.Value.Render(v)
		req.Header.Set(h.Name, val)
		if h.Name == "Host" {
			req.Host = val
		}
	}
	t.Header = req.Header

	keepBody := st.needsBody || v.trace != nil
	start := time.Now()
	resp, err := v.client.Do(req)
	if err == nil {
		if keepBody {
			t.RespBody, err = io.ReadAll(io.LimitReader(resp.Body, maxCaptureBody))
			t.Bytes = int64(len(t.RespBody))
			if err == nil {
				var rest int64
				rest, err = io.Copy(io.Discard, resp.Body)
				t.Bytes += rest
			}
		} else {
			t.Bytes, err = io.Copy(io.Discard, resp.Body)
		}
		resp.Body.Close()
	}
	t.Latency = time.Since(start)

	if err != nil {
		if ctx.Err() != nil {
			t.Aborted = true // Ctrl-C, not a failure of the target
			sr.Aborted++
		} else {
			t.Err = err
			sr.recordError(err)
			v.countError()
		}
		v.emit(t)
		return t
	}

	t.Status = resp.StatusCode
	finish := uint32(0)
	c := v.r.series.Load()
	if c != nil {
		finish = c.Now()
		c.Response(finish, resp.StatusCode)
	}
	sr.recordResponse(resp.StatusCode, t.Latency, finish, t.Bytes)

	if len(st.Status) > 0 && !slices.Contains(st.Status, resp.StatusCode) {
		t.CheckErr = fmt.Errorf("status %d, want %s", resp.StatusCode, statusWant(st.Status))
	}
	if len(st.Capt) > 0 {
		r := &response{status: resp.StatusCode, header: resp.Header, body: t.RespBody}
		for _, c := range st.Capt {
			val, err := c.extract(r)
			if err == nil {
				v.saveTo[c.As] = val
			}
			t.Captures = append(t.Captures, CaptureTrace{As: c.As, Value: val, Err: err})
		}
	}
	if t.Failed() {
		sr.CheckFails++
		v.r.checkFails.Add(1)
		if c != nil {
			c.CheckFail(finish)
		}
	}
	v.emit(t)
	return t
}

// countError adds a transport error to the per-second counts.
func (v *vu) countError() {
	if c := v.r.series.Load(); c != nil {
		c.Error(c.Now())
	}
}

func (v *vu) emit(t *StepTrace) {
	if v.trace != nil {
		v.trace(t)
	}
}

func statusWant(codes []int) string {
	if len(codes) == 1 {
		return fmt.Sprint(codes[0])
	}
	s := make([]string, len(codes))
	for i, c := range codes {
		s[i] = fmt.Sprint(c)
	}
	return strings.Join(s, " or ")
}
