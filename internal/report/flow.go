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
	"github.com/namnguyen3019/pelt/internal/stats"
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
}

// NewFlow computes statistics for res. Like New, it sorts the latency
// slices in place.
func NewFlow(res *flow.Result) *FlowReport {
	r := &FlowReport{res: res, totals: res.Totals()}
	for i := range res.Steps {
		r.steps = append(r.steps, stats.Summarize(res.Steps[i].Latencies, stepPercentiles))
	}
	for i := range res.Flows {
		r.flows = append(r.flows, stats.Summarize(res.Flows[i].FlowTimes, flowPercentiles))
		r.walls = append(r.walls, stats.Summarize(res.Flows[i].WallTimes, flowPercentiles))
	}
	// Thresholds in flow mode come in a later version; for now only the
	// built-in rule applies: a run in which no iteration completed fails.
	reason := ""
	if _, completed, _, _ := res.Iterations(); completed == 0 {
		reason = "no iteration completed"
	}
	r.verdict = newVerdict(nil, reason)
	return r
}

// Verdict says whether the run passed.
func (r *FlowReport) Verdict() Verdict { return r.verdict }

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
	Mode           string         `json:"mode"`
	Scenario       string         `json:"scenario"`
	File           string         `json:"file,omitempty"`
	Users          int            `json:"users"`
	Duration       string         `json:"duration_configured,omitempty"`
	Iterations     int            `json:"iterations_configured,omitempty"`
	Seed           uint64         `json:"seed"`
	Interrupted    bool           `json:"interrupted"`
	Passed         bool           `json:"passed"`
	FailReason     string         `json:"fail_reason,omitempty"`
	ElapsedMS      float64        `json:"elapsed_ms"`
	Iters          jsonIterations `json:"iterations"`
	SetupFailed    int            `json:"setup_failed_users"`
	Total          int            `json:"total"`
	Successes      int            `json:"responses"`
	Errors         int            `json:"errors"`
	CheckFails     int            `json:"failed_checks"`
	Aborted        int            `json:"aborted,omitempty"`
	RPS            float64        `json:"requests_per_sec"`
	BytesRead      int64          `json:"bytes_read"`
	ConnsNew       int64          `json:"connections_opened"`
	ConnsReused    int64          `json:"connections_reused"`
	StatusCodes    map[string]int `json:"status_codes"`
	ErrorBreakdown map[string]int `json:"error_breakdown"`
	Flows          []jsonFlow     `json:"flows"`
	Steps          []jsonStep     `json:"steps"`
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
