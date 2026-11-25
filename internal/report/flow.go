package report

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/namnguyen3019/pelt/internal/flow"
	"github.com/namnguyen3019/pelt/internal/loadtest"
	"github.com/namnguyen3019/pelt/internal/series"
	"github.com/namnguyen3019/pelt/internal/stats"
	"github.com/namnguyen3019/pelt/internal/threshold"
)

// stepPercentiles and flowPercentiles are the columns of the two tables.
var (
	stepPercentiles = []float64{50, 90, 95, 99}
	flowPercentiles = []float64{50, 95, 99}
)

// FlowReport is the computed view of a flow run.
type FlowReport struct {
	res     *flow.Result
	totals  *loadtest.Result
	steps   []stats.Summary // by step id
	flows   []stats.Summary // flow time, by flow id
	walls   []stats.Summary // wall time, by flow id
	verdict Verdict
	ser     *series.Series
	tl      timeline

	// all holds every response latency, sorted, for run-level latency
	// thresholds. It is built on first use.
	all     []time.Duration
	allSum  stats.Summary
	allDone bool
}

// NewFlow computes statistics for res. Like New, it sorts the latency
// slices in place.
func NewFlow(res *flow.Result) *FlowReport {
	r := &FlowReport{res: res, totals: res.Totals()}
	// The series first: it needs each latency next to its finish time, and
	// Summarize sorts the latencies in place.
	sets := make([]series.Samples, len(res.Steps))
	for i := range res.Steps {
		sets[i] = series.Samples{Latencies: res.Steps[i].Latencies, Finish: res.Steps[i].Finish}
	}
	r.ser = series.Build(res.Series, sets...)
	r.tl = newTimeline(r.ser, nil)
	r.ser.Release()
	for i := range res.Steps {
		r.steps = append(r.steps, stats.Summarize(res.Steps[i].Latencies, stepPercentiles))
	}
	for i := range res.Flows {
		r.flows = append(r.flows, stats.Summarize(res.Flows[i].FlowTimes, flowPercentiles))
		r.walls = append(r.walls, stats.Summarize(res.Flows[i].WallTimes, flowPercentiles))
	}
	r.Check(nil)
	return r
}

// Check evaluates thresholds against the run, keeps the verdict for the
// text and JSON output, and returns it. With no thresholds the verdict
// still fails a run in which no iteration completed.
func (r *FlowReport) Check(ts []flow.Target) Verdict {
	outcomes := make([]threshold.Outcome, 0, len(ts))
	for _, t := range ts {
		outcomes = append(outcomes, threshold.Check(t.Threshold, r.measure(t)))
	}
	reason := ""
	if _, completed, _, _ := r.res.Iterations(); completed == 0 {
		reason = "no iteration completed"
	}
	r.verdict = newVerdict(outcomes, reason)
	return r.verdict
}

// Verdict returns the result of the last Check.
func (r *FlowReport) Verdict() Verdict { return r.verdict }

// measure finds a threshold's metric at its level: the whole run, a flow
// or a step. Rates are a share of completed requests, or of started
// iterations for aborted.
func (r *FlowReport) measure(t flow.Target) threshold.Measured {
	res, tot := r.res, r.totals
	secs := max(res.Elapsed.Seconds(), 1e-9)
	perSec := func(n int) threshold.Measured {
		if n == 0 {
			return threshold.Measured{NoSamples: true}
		}
		return threshold.Measured{Value: float64(n) / secs}
	}
	switch t.Level() {
	case "flow":
		f := &res.Flows[t.FlowID]
		switch {
		case strings.HasPrefix(t.Metric, "wall_p"):
			return latency(t.Threshold, f.WallTimes, r.walls[t.FlowID])
		case t.Kind == threshold.Latency:
			return latency(t.Threshold, f.FlowTimes, r.flows[t.FlowID])
		case t.Metric == "aborted":
			return rate(t.Threshold, f.Aborted, f.Started)
		case t.Metric == "ips":
			return perSec(f.Started)
		}
	case "step":
		s := &res.Steps[t.StepID]
		switch {
		case t.Kind == threshold.Latency:
			return latency(t.Threshold, s.Latencies, r.steps[t.StepID])
		case t.Metric == "errors":
			return rate(t.Threshold, s.ErrorCount(), s.Requests)
		case t.StatusLow > 0:
			return rate(t.Threshold, statusCount(s.StatusCodes, t.StatusLow, t.StatusHigh), s.Requests)
		case t.Metric == "checks":
			return rate(t.Threshold, s.CheckFails, s.Requests)
		case t.Metric == "rps":
			return perSec(s.Requests)
		case t.Metric == "requests":
			return threshold.Measured{Value: float64(s.Requests)}
		}
	default:
		started, _, aborted, _ := res.Iterations()
		switch {
		case t.Kind == threshold.Latency:
			r.allLatencies()
			return latency(t.Threshold, r.all, r.allSum)
		case t.Metric == "errors":
			return rate(t.Threshold, tot.ErrorCount(), tot.Total)
		case t.StatusLow > 0:
			return rate(t.Threshold, statusCount(tot.StatusCodes, t.StatusLow, t.StatusHigh), tot.Total)
		case t.Metric == "checks":
			return rate(t.Threshold, res.CheckFails(), tot.Total)
		case t.Metric == "rps":
			return perSec(tot.Total)
		case t.Metric == "requests":
			return threshold.Measured{Value: float64(tot.Total)}
		case t.Metric == "aborted":
			return rate(t.Threshold, aborted, started)
		case t.Metric == "ips":
			return perSec(started)
		case t.Metric == "setup_failed":
			return threshold.Measured{Value: float64(res.SetupFailedVUs)}
		}
	}
	// flow.ResolveThreshold rejects a metric that a level does not have.
	return threshold.Measured{NoSamples: true}
}

// allLatencies gathers every response latency across steps, once.
func (r *FlowReport) allLatencies() {
	if r.allDone {
		return
	}
	for i := range r.res.Steps {
		r.all = append(r.all, r.res.Steps[i].Latencies...)
	}
	r.allSum = stats.Summarize(r.all, nil) // sorts r.all
	r.allDone = true
}

func stepLabel(st *flow.Step) string { return st.Flow + " / " + st.Name }

// WriteText prints the report for a terminal.
func (r *FlowReport) WriteText(w io.Writer) error {
	var b strings.Builder
	res, tot := r.res, r.totals
	sc, opts := res.Scenario, res.Options

	if res.Interrupted {
		b.WriteString("Interrupted: partial results below.\n\n")
	}
	name := sc.Name
	if sc.Path != "" {
		name += " (" + sc.Path + ")"
	}
	fmt.Fprintf(&b, "Scenario:     %s\n", name)
	fmt.Fprintf(&b, "Load:         %s\n", describeFlowLoad(opts))
	fmt.Fprintf(&b, "Elapsed:      %s\n\n", res.Elapsed.Round(time.Millisecond))

	started, completed, aborted, cutN := res.Iterations()
	fmt.Fprintf(&b, "Iterations:   %d started: %d completed, %d aborted, %d cut by the end of the run (%.1f/sec)\n",
		started, completed, aborted, cutN, float64(started)/max(res.Elapsed.Seconds(), 1e-9))
	if res.SetupFailedVUs > 0 {
		fmt.Fprintf(&b, "Setup:        %d of %d users gave up after every setup attempt failed\n", res.SetupFailedVUs, opts.Users)
	}
	errRate := 0.0
	if tot.Total > 0 {
		errRate = 100 * float64(tot.ErrorCount()) / float64(tot.Total)
	}
	fmt.Fprintf(&b, "Requests:     %d completed, %d responses, %d errors (%.2f%%), %d failed checks\n",
		tot.Total, tot.Successes, tot.ErrorCount(), errRate, res.CheckFails())
	if tot.Aborted > 0 {
		fmt.Fprintf(&b, "              %d in-flight requests aborted by the interrupt\n", tot.Aborted)
	}
	fmt.Fprintf(&b, "Throughput:   %.1f requests/sec, %s/sec read\n",
		tot.RequestsPerSecond(), humanBytes(float64(tot.BytesRead)/max(res.Elapsed.Seconds(), 1e-9)))
	fmt.Fprintf(&b, "Connections:  %d opened, %d requests reused a connection\n", tot.ConnsNew, tot.ConnsReused)

	// Flows table. Flow time leaves out think time; wall time includes it.
	b.WriteString("\n")
	nameW := len("Flow")
	for i := range res.Flows {
		nameW = max(nameW, len(res.Flows[i].Flow.Name))
	}
	fmt.Fprintf(&b, "%-*s  %6s  %7s  %7s  %7s  %5s  %10s  %10s  %10s  %10s\n", nameW, "Flow",
		"weight", "started", "done", "aborted", "cut", "flow p50", "flow p95", "flow p99", "wall p50")
	for i := range res.Flows {
		f := &res.Flows[i]
		ft, wt := r.flows[i], r.walls[i]
		fmt.Fprintf(&b, "%-*s  %6d  %7d  %7d  %7d  %5d  %10s  %10s  %10s  %10s\n", nameW, f.Flow.Name,
			f.Flow.Weight, f.Started, f.Completed, f.Aborted, f.Cut,
			pct(ft, 0), pct(ft, 1), pct(ft, 2), pct(wt, 0))
	}
	b.WriteString("  (times in ms; flow time is request time only, wall time includes think time)\n")

	// Steps table.
	b.WriteString("\n")
	nameW = len("Step")
	for i := range res.Steps {
		nameW = max(nameW, len(stepLabel(res.Steps[i].Step)))
	}
	fmt.Fprintf(&b, "%-*s  %7s  %6s  %6s  %9s  %9s  %9s  %9s  %9s\n", nameW, "Step",
		"reqs", "errors", "failed", "p50", "p90", "p95", "p99", "max")
	for i := range res.Steps {
		s := &res.Steps[i]
		sm := r.steps[i]
		maxS := "-"
		if sm.Count > 0 {
			maxS = ms(sm.Max)
		}
		fmt.Fprintf(&b, "%-*s  %7d  %6d  %6d  %9s  %9s  %9s  %9s  %9s\n", nameW, stepLabel(s.Step),
			s.Requests, s.ErrorCount(), s.CheckFails,
			pct(sm, 0), pct(sm, 1), pct(sm, 2), pct(sm, 3), maxS)
	}
	b.WriteString("  (times in ms; failed = status check or capture failed)\n")

	var abortLines []string
	for i := range res.Steps {
		if n := res.Steps[i].AbortedIterations; n > 0 {
			abortLines = append(abortLines, fmt.Sprintf("  %6d  %s", n, stepLabel(res.Steps[i].Step)))
		}
	}
	if len(abortLines) > 0 {
		b.WriteString("\nAborted at step:\n")
		b.WriteString(strings.Join(abortLines, "\n"))
		b.WriteString("\n")
	}

	writeTimeline(&b, r.ser, r.tl)

	if len(tot.StatusCodes) > 0 {
		b.WriteString("\nStatus codes:\n")
		for _, code := range slices.Sorted(maps.Keys(tot.StatusCodes)) {
			fmt.Fprintf(&b, "  %d  %d\n", code, tot.StatusCodes[code])
		}
	}
	if len(tot.Errors) > 0 {
		b.WriteString("\nErrors:\n")
		kinds := slices.SortedFunc(maps.Keys(tot.Errors), func(a, c string) int {
			if tot.Errors[a] != tot.Errors[c] {
				return tot.Errors[c] - tot.Errors[a]
			}
			return strings.Compare(a, c)
		})
		for _, k := range kinds {
			fmt.Fprintf(&b, "  %6d  %s\n", tot.Errors[k], k)
		}
	}

	writeVerdict(&b, r.verdict)

	_, err := io.WriteString(w, b.String())
	return err
}

// WriteSeriesCSV writes the per-second series as CSV, for -series.
func (r *FlowReport) WriteSeriesCSV(w io.Writer) error { return writeSeriesCSV(w, r.ser) }

func describeFlowLoad(o flow.Options) string {
	parts := []string{fmt.Sprintf("%d users", o.Users)}
	var limits []string
	if o.Iterations > 0 {
		limits = append(limits, fmt.Sprintf("%d iterations", o.Iterations))
	}
	if o.Duration > 0 {
		limits = append(limits, "for "+o.Duration.String())
	}
	limit := strings.Join(limits, " or ")
	if len(limits) == 2 {
		limit += " (whichever comes first)"
	}
	parts = append(parts, limit, fmt.Sprintf("seed %d", o.Seed))
	return strings.Join(parts, ", ")
}

// ms formats a duration as milliseconds with three decimals.
func ms(d time.Duration) string { return fmt.Sprintf("%.3f", msf(d)) }

// pct returns the i-th percentile of s as ms, or "-" without samples.
func pct(s stats.Summary, i int) string {
	if s.Count == 0 || i >= len(s.Percentiles) {
		return "-"
	}
	return ms(s.Percentiles[i].Value)
}

// --- JSON ---

type jsonFlowReport struct {
	Mode           string          `json:"mode"`
	Scenario       string          `json:"scenario"`
	File           string          `json:"file,omitempty"`
	Users          int             `json:"users"`
	Duration       string          `json:"duration_configured,omitempty"`
	Iterations     int             `json:"iterations_configured,omitempty"`
	Seed           uint64          `json:"seed"`
	Interrupted    bool            `json:"interrupted"`
	Passed         bool            `json:"passed"`
	FailReason     string          `json:"fail_reason,omitempty"`
	ElapsedMS      float64         `json:"elapsed_ms"`
	Iters          jsonIterations  `json:"iterations"`
	SetupFailed    int             `json:"setup_failed_users"`
	Total          int             `json:"total"`
	Successes      int             `json:"responses"`
	Errors         int             `json:"errors"`
	CheckFails     int             `json:"failed_checks"`
	Aborted        int             `json:"aborted,omitempty"`
	RPS            float64         `json:"requests_per_sec"`
	BytesRead      int64           `json:"bytes_read"`
	ConnsNew       int64           `json:"connections_opened"`
	ConnsReused    int64           `json:"connections_reused"`
	StatusCodes    map[string]int  `json:"status_codes"`
	ErrorBreakdown map[string]int  `json:"error_breakdown"`
	Flows          []jsonFlow      `json:"flows"`
	Steps          []jsonStep      `json:"steps"`
	Thresholds     []jsonThreshold `json:"thresholds,omitempty"`
	Series         []jsonSeriesRow `json:"series"`
}

type jsonIterations struct {
	Started   int     `json:"started"`
	Completed int     `json:"completed"`
	Aborted   int     `json:"aborted"`
	Cut       int     `json:"cut"`
	PerSec    float64 `json:"per_sec"`
}

type jsonFlow struct {
	Name      string         `json:"name"`
	Weight    int            `json:"weight"`
	Started   int            `json:"started"`
	Completed int            `json:"completed"`
	Aborted   int            `json:"aborted"`
	Cut       int            `json:"cut"`
	AbortedAt map[string]int `json:"aborted_at"`
	FlowTime  *jsonLatency   `json:"flow_time_ms,omitempty"`
	WallTime  *jsonLatency   `json:"wall_time_ms,omitempty"`
}

type jsonStep struct {
	Flow           string         `json:"flow"`
	Name           string         `json:"name"`
	Requests       int            `json:"requests"`
	Responses      int            `json:"responses"`
	Errors         int            `json:"errors"`
	CheckFails     int            `json:"failed_checks"`
	AbortedIters   int            `json:"aborted_iterations"`
	RPS            float64        `json:"requests_per_sec"`
	Latency        *jsonLatency   `json:"latency_ms,omitempty"`
	StatusCodes    map[string]int `json:"status_codes"`
	ErrorBreakdown map[string]int `json:"error_breakdown"`
}

func toJSONLatency(s stats.Summary) *jsonLatency {
	if s.Count == 0 {
		return nil
	}
	l := &jsonLatency{Min: msf(s.Min), Mean: msf(s.Mean), StdDev: msf(s.StdDev), Max: msf(s.Max), P: map[string]float64{}}
	for _, p := range s.Percentiles {
		l.P["p"+trimFloat(p.P)] = msf(p.Value)
	}
	return l
}

func statusMap(m map[int]int) map[string]int {
	out := make(map[string]int, len(m))
	for code, n := range m {
		out[fmt.Sprint(code)] = n
	}
	return out
}

// WriteJSON prints the report as one indented JSON object.
func (r *FlowReport) WriteJSON(w io.Writer) error {
	res, tot, opts := r.res, r.totals, r.res.Options
	secs := max(res.Elapsed.Seconds(), 1e-9)
	started, completed, aborted, cutN := res.Iterations()
	out := jsonFlowReport{
		Mode:        "flow",
		Scenario:    res.Scenario.Name,
		File:        res.Scenario.Path,
		Users:       opts.Users,
		Iterations:  opts.Iterations,
		Seed:        opts.Seed,
		Interrupted: res.Interrupted,
		Passed:      r.verdict.Passed(),
		FailReason:  r.verdict.Reason,
		ElapsedMS:   msf(res.Elapsed),
		Iters: jsonIterations{Started: started, Completed: completed, Aborted: aborted, Cut: cutN,
			PerSec: float64(started) / secs},
		SetupFailed:    res.SetupFailedVUs,
		Total:          tot.Total,
		Successes:      tot.Successes,
		Errors:         tot.ErrorCount(),
		CheckFails:     res.CheckFails(),
		Aborted:        tot.Aborted,
		RPS:            tot.RequestsPerSecond(),
		BytesRead:      tot.BytesRead,
		ConnsNew:       tot.ConnsNew,
		ConnsReused:    tot.ConnsReused,
		StatusCodes:    statusMap(tot.StatusCodes),
		ErrorBreakdown: tot.Errors,
		Flows:          []jsonFlow{},
		Steps:          []jsonStep{},
		Thresholds:     toJSONThresholds(r.verdict),
		Series:         toJSONSeries(r.ser),
	}
	if opts.Duration > 0 {
		out.Duration = opts.Duration.String()
	}
	for i := range res.Flows {
		f := &res.Flows[i]
		jf := jsonFlow{Name: f.Flow.Name, Weight: f.Flow.Weight, Started: f.Started, Completed: f.Completed,
			Aborted: f.Aborted, Cut: f.Cut, AbortedAt: map[string]int{},
			FlowTime: toJSONLatency(r.flows[i]), WallTime: toJSONLatency(r.walls[i])}
		for _, st := range f.Flow.Steps {
			if n := res.Steps[st.ID].AbortedIterations; n > 0 {
				jf.AbortedAt[st.Name] = n
			}
		}
		out.Flows = append(out.Flows, jf)
	}
	for i := range res.Steps {
		s := &res.Steps[i]
		out.Steps = append(out.Steps, jsonStep{
			Flow: s.Step.Flow, Name: s.Step.Name, Requests: s.Requests, Responses: s.Responses,
			Errors: s.ErrorCount(), CheckFails: s.CheckFails, AbortedIters: s.AbortedIterations,
			RPS: float64(s.Requests) / secs, Latency: toJSONLatency(r.steps[i]),
			StatusCodes: statusMap(s.StatusCodes), ErrorBreakdown: s.Errors,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
