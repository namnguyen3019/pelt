package flow

import (
	"time"

	"github.com/namnguyen3019/pelt/internal/loadtest"
	"github.com/namnguyen3019/pelt/internal/series"
)

// Result is the raw outcome of a flow run. internal/report turns it into
// text or JSON.
type Result struct {
	Scenario *Scenario
	Options  Options // as run, with defaults filled in

	Elapsed     time.Duration
	Interrupted bool // Ctrl-C or SIGTERM

	Steps []StepResult // indexed by Step.ID
	Flows []FlowResult // indexed by Flow.ID

	// SetupFailedVUs counts users that gave up after every setup attempt
	// failed. They sent no flow iterations.
	SetupFailedVUs int

	ConnsNew    int64
	ConnsReused int64

	// Series is the run second by second: counts and sampled levels.
	// report.NewFlow turns it into rows with latency percentiles.
	Series series.Raw
}

// StepResult holds the numbers for one step.
type StepResult struct {
	Step *Step

	Requests   int // completed: responses + errors
	Responses  int // got an HTTP response (any status)
	CheckFails int // responses whose check or a capture failed
	Aborted    int // in flight when Ctrl-C hit; not in Requests

	// AbortedIterations counts iterations (or setup attempts) that stopped
	// at this step because of a failure here.
	AbortedIterations int

	Latencies []time.Duration // one per response
	// Finish holds, beside each latency, when that response finished in
	// milliseconds since the start, for the per-second series.
	Finish      []uint32
	StatusCodes map[int]int
	Errors      map[string]int // transport error kind -> count
	BytesRead   int64
}

// ErrorCount is the number of transport errors (no HTTP response).
func (s *StepResult) ErrorCount() int { return s.Requests - s.Responses }

// FlowResult holds the numbers for one flow.
type FlowResult struct {
	Flow *Flow

	Started   int
	Completed int // every step ran (failed checks with on_fail continue still count)
	Aborted   int // stopped by a failed check, capture or transport error
	Cut       int // stopped by the end of the run (-d or Ctrl-C)

	// FlowTimes sums request time over the steps of each completed
	// iteration, leaving out think time. WallTimes is the same iterations
	// measured end to end, think time included.
	FlowTimes []time.Duration
	WallTimes []time.Duration
}

// Totals adds up every step into the shape of a single-URL result, for the
// overall block of the report.
func (r *Result) Totals() *loadtest.Result {
	t := &loadtest.Result{
		Elapsed:     r.Elapsed,
		Interrupted: r.Interrupted,
		StatusCodes: map[int]int{},
		Errors:      map[string]int{},
		ConnsNew:    r.ConnsNew,
		ConnsReused: r.ConnsReused,
	}
	for i := range r.Steps {
		s := &r.Steps[i]
		t.Total += s.Requests
		t.Successes += s.Responses
		t.Aborted += s.Aborted
		t.BytesRead += s.BytesRead
		for c, n := range s.StatusCodes {
			t.StatusCodes[c] += n
		}
		for k, n := range s.Errors {
			t.Errors[k] += n
		}
	}
	return t
}

// CheckFails is the total of failed checks over every step.
func (r *Result) CheckFails() int {
	n := 0
	for i := range r.Steps {
		n += r.Steps[i].CheckFails
	}
	return n
}

// Iterations sums the flow counters.
func (r *Result) Iterations() (started, completed, aborted, cut int) {
	for i := range r.Flows {
		f := &r.Flows[i]
		started += f.Started
		completed += f.Completed
		aborted += f.Aborted
		cut += f.Cut
	}
	return
}

// recorder collects one VU's results. Only that VU's goroutine touches it,
// so the hot path takes no locks; slices are indexed by step and flow id.
type recorder struct {
	steps     []StepResult
	flows     []FlowResult
	setupFail bool
}

func newRecorder(sc *Scenario) *recorder {
	rec := &recorder{
		steps: make([]StepResult, len(sc.Steps)),
		flows: make([]FlowResult, len(sc.Flows)),
	}
	for i, st := range sc.Steps {
		rec.steps[i].Step = st
	}
	for i, fl := range sc.Flows {
		rec.flows[i].Flow = fl
	}
	return rec
}

func (s *StepResult) recordResponse(code int, lat time.Duration, finish uint32, n int64) {
	s.Requests++
	s.Responses++
	s.Latencies = append(s.Latencies, lat)
	s.Finish = append(s.Finish, finish)
	if s.StatusCodes == nil {
		s.StatusCodes = make(map[int]int, 2)
	}
	s.StatusCodes[code]++
	s.BytesRead += n
}

func (s *StepResult) recordError(err error) {
	s.Requests++
	if s.Errors == nil {
		s.Errors = make(map[string]int, 1)
	}
	s.Errors[loadtest.ClassifyError(err)]++
}

// merge combines the per-VU recorders after the run.
func merge(sc *Scenario, recs []*recorder) *Result {
	res := &Result{
		Scenario: sc,
		Steps:    make([]StepResult, len(sc.Steps)),
		Flows:    make([]FlowResult, len(sc.Flows)),
	}
	for i, st := range sc.Steps {
		res.Steps[i] = StepResult{Step: st, StatusCodes: map[int]int{}, Errors: map[string]int{}}
	}
	for i, fl := range sc.Flows {
		res.Flows[i].Flow = fl
	}
	for _, rec := range recs {
		if rec.setupFail {
			res.SetupFailedVUs++
		}
		for i := range rec.steps {
			src, dst := &rec.steps[i], &res.Steps[i]
			dst.Requests += src.Requests
			dst.Responses += src.Responses
			dst.CheckFails += src.CheckFails
			dst.Aborted += src.Aborted
			dst.AbortedIterations += src.AbortedIterations
			dst.Latencies = append(dst.Latencies, src.Latencies...)
			dst.Finish = append(dst.Finish, src.Finish...)
			dst.BytesRead += src.BytesRead
			for c, n := range src.StatusCodes {
				dst.StatusCodes[c] += n
			}
			for k, n := range src.Errors {
				dst.Errors[k] += n
			}
		}
		for i := range rec.flows {
			src, dst := &rec.flows[i], &res.Flows[i]
			dst.Started += src.Started
			dst.Completed += src.Completed
			dst.Aborted += src.Aborted
			dst.Cut += src.Cut
			dst.FlowTimes = append(dst.FlowTimes, src.FlowTimes...)
			dst.WallTimes = append(dst.WallTimes, src.WallTimes...)
		}
	}
	return res
}
