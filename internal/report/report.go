// Package report renders a load test result as human-readable text or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/namnguyen3019/pelt/internal/loadtest"
	"github.com/namnguyen3019/pelt/internal/series"
	"github.com/namnguyen3019/pelt/internal/stages"
	"github.com/namnguyen3019/pelt/internal/stats"
	"github.com/namnguyen3019/pelt/internal/threshold"
)

// histogramBuckets is how many bars the text histogram shows.
const histogramBuckets = 10

// Report is the computed view of a run, shared by the text and JSON output.
type Report struct {
	cfg     loadtest.Config
	res     *loadtest.Result
	latency stats.Summary
	hist    []stats.Bucket
	verdict Verdict
	ser     *series.Series
	tl      timeline
	stages  []stageResult
}

// New computes statistics for res. It takes ownership of res.Latencies
// (the slice gets sorted in place).
func New(cfg loadtest.Config, res *loadtest.Result) *Report {
	r := &Report{cfg: cfg, res: res}
	// The series and histogram first: the series needs each latency next
	// to its finish time, and Summarize sorts the latencies in place.
	r.ser = series.Build(res.Series, series.Samples{Latencies: res.Latencies, Finish: res.Finish})
	r.tl = newTimeline(r.ser, cfg.Stages)
	r.stages = newStageResults(r.ser, cfg.Stages)
	r.ser.Release()
	r.hist = stats.Histogram(res.Latencies, histogramBuckets)
	r.latency = stats.Summarize(res.Latencies, stats.DefaultPercentiles)
	r.Check(nil)
	return r
}

// Check evaluates thresholds against the run, keeps the verdict for the
// text and JSON output, and returns it. Thresholds must be run-wide and not
// flow-only; the caller validates that when parsing flags.
//
// With no thresholds the verdict still fails a run in which no request got
// a response.
func (r *Report) Check(ts []threshold.Threshold) Verdict {
	res := r.res
	outcomes := make([]threshold.Outcome, 0, len(ts))
	for _, t := range ts {
		outcomes = append(outcomes, threshold.Check(t, r.measure(t)))
	}
	reason := ""
	if res.Successes == 0 {
		reason = "no request got a response"
	}
	r.verdict = newVerdict(outcomes, reason)
	return r.verdict
}

// Verdict returns the result of the last Check.
func (r *Report) Verdict() Verdict { return r.verdict }

// measure looks up a threshold's metric in the run. res.Latencies is
// sorted by the time it is called, because New ran stats.Summarize.
func (r *Report) measure(t threshold.Threshold) threshold.Measured {
	res := r.res
	switch {
	case t.Kind == threshold.Latency:
		return latency(t, res.Latencies, r.latency)
	case t.Metric == "errors":
		return rate(t, res.ErrorCount(), res.Total)
	case t.StatusLow > 0:
		return rate(t, statusCount(res.StatusCodes, t.StatusLow, t.StatusHigh), res.Total)
	case t.Metric == "rps":
		if res.Total == 0 {
			return threshold.Measured{NoSamples: true}
		}
		return threshold.Measured{Value: res.RequestsPerSecond()}
	case t.Metric == "requests":
		return threshold.Measured{Value: float64(res.Total)}
	}
	return threshold.Measured{NoSamples: true} // flow-only metric
}

// WriteText prints the report in a format meant for a terminal.
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder
	res, cfg := r.res, r.cfg

	if res.Interrupted {
		b.WriteString("Interrupted: partial results below.\n\n")
	}

	fmt.Fprintf(&b, "Target:       %s %s\n", cfg.Method, cfg.URL)
	fmt.Fprintf(&b, "Load:         %s\n", describeLoad(cfg))
	fmt.Fprintf(&b, "Elapsed:      %s\n\n", res.Elapsed.Round(time.Millisecond))

	errRate := 0.0
	if res.Total > 0 {
		errRate = 100 * float64(res.ErrorCount()) / float64(res.Total)
	}
	fmt.Fprintf(&b, "Requests:     %d completed, %d responses, %d errors (%.2f%%)\n",
		res.Total, res.Successes, res.ErrorCount(), errRate)
	if res.Aborted > 0 {
		fmt.Fprintf(&b, "              %d in-flight requests aborted by the interrupt\n", res.Aborted)
	}
	fmt.Fprintf(&b, "Throughput:   %.1f requests/sec, %s/sec read\n",
		res.RequestsPerSecond(), humanBytes(float64(res.BytesRead)/max(res.Elapsed.Seconds(), 1e-9)))
	fmt.Fprintf(&b, "Connections:  %d opened, %d requests reused a connection\n", res.ConnsNew, res.ConnsReused)

	if r.latency.Count > 0 {
		s := r.latency
		b.WriteString("\nLatency (successful requests):\n")
		fmt.Fprintf(&b, "  min     %s\n", fmtMS(s.Min))
		fmt.Fprintf(&b, "  mean    %s\n", fmtMS(s.Mean))
		fmt.Fprintf(&b, "  stddev  %s\n", fmtMS(s.StdDev))
		fmt.Fprintf(&b, "  max     %s\n", fmtMS(s.Max))
		for _, p := range s.Percentiles {
			fmt.Fprintf(&b, "  p%-6s %s\n", trimFloat(p.P), fmtMS(p.Value))
		}

		b.WriteString("\nHistogram:\n")
		writeHistogram(&b, r.hist)
	}

	writeStages(&b, cfg.Stages, r.stages, false)
	writeTimeline(&b, r.ser, r.tl)

	if len(res.StatusCodes) > 0 {
		b.WriteString("\nStatus codes:\n")
		for _, code := range slices.Sorted(maps.Keys(res.StatusCodes)) {
			fmt.Fprintf(&b, "  %d  %d\n", code, res.StatusCodes[code])
		}
	}

	if len(res.Errors) > 0 {
		b.WriteString("\nErrors:\n")
		// Most frequent first, ties broken alphabetically for stable output.
		kinds := slices.SortedFunc(maps.Keys(res.Errors), func(a, c string) int {
			if res.Errors[a] != res.Errors[c] {
				return res.Errors[c] - res.Errors[a]
			}
			return strings.Compare(a, c)
		})
		for _, k := range kinds {
			fmt.Fprintf(&b, "  %6d  %s\n", res.Errors[k], k)
		}
	}

	writeVerdict(&b, r.verdict)

	_, err := io.WriteString(w, b.String())
	return err
}

func describeLoad(cfg loadtest.Config) string {
	if plan := cfg.Stages; plan != nil {
		return describeStages(cfg, plan)
	}
	var parts []string
	if cfg.Requests > 0 {
		parts = append(parts, fmt.Sprintf("%d requests", cfg.Requests))
	}
	if cfg.Duration > 0 {
		parts = append(parts, "for "+cfg.Duration.String())
	}
	if cfg.Requests > 0 && cfg.Duration > 0 {
		parts = []string{strings.Join(parts, " or ") + " (whichever comes first)"}
	}
	parts = append(parts, fmt.Sprintf("%d workers", cfg.Concurrency))
	if cfg.RateLimit > 0 {
		parts = append(parts, fmt.Sprintf("rate limit %s req/s", trimFloat(cfg.RateLimit)))
	} else {
		parts = append(parts, "no rate limit")
	}
	if cfg.Untuned {
		parts = append(parts, "untuned transport")
	}
	return strings.Join(parts, ", ")
}

// describeStages is the Load line for a staged run: what the stages move,
// the stages themselves, and the knob that stays a cap.
func describeStages(cfg loadtest.Config, plan *stages.Plan) string {
	var parts []string
	if plan.Kind == stages.Rate {
		parts = append(parts, "rate stages "+plan.String()+" over "+stages.Dur(plan.Total()),
			fmt.Sprintf("at most %d in flight", cfg.Concurrency))
	} else {
		parts = append(parts, "concurrency stages "+plan.String()+" over "+stages.Dur(plan.Total()))
		if cfg.RateLimit > 0 {
			parts = append(parts, fmt.Sprintf("rate limit %s req/s", trimFloat(cfg.RateLimit)))
		}
	}
	if cfg.Requests > 0 {
		parts = append(parts, fmt.Sprintf("stop at %d requests", cfg.Requests))
	}
	if cfg.Untuned {
		parts = append(parts, "untuned transport")
	}
	return strings.Join(parts, ", ")
}

func writeHistogram(b *strings.Builder, hist []stats.Bucket) {
	const barWidth = 40
	maxCount := 0
	for _, bk := range hist {
		maxCount = max(maxCount, bk.Count)
	}
	for _, bk := range hist {
		bar := 0
		if maxCount > 0 {
			bar = bk.Count * barWidth / maxCount
			if bk.Count > 0 && bar == 0 {
				bar = 1 // show that the bucket is not empty
			}
		}
		fmt.Fprintf(b, "  %10s  [%7d]  %s\n", fmtMS(bk.High), bk.Count, strings.Repeat("■", bar))
	}
}

// fmtMS formats a duration as milliseconds with three decimals, so every
// latency in the report uses the same unit and lines up.
func fmtMS(d time.Duration) string {
	return fmt.Sprintf("%.3f ms", float64(d)/float64(time.Millisecond))
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}

func humanBytes(n float64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%.0f B", n)
	}
	div, exp := float64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", n/div, "KMGTP"[exp])
}

// JSON output types. Durations are in milliseconds as floats, which is
// what most dashboards and scripts expect.

type jsonReport struct {
	Target         string          `json:"target"`
	Method         string          `json:"method"`
	Concurrency    int             `json:"concurrency"`
	Requests       int             `json:"requests_configured,omitempty"`
	Duration       string          `json:"duration_configured,omitempty"`
	RateLimit      float64         `json:"rate_limit,omitempty"`
	Interrupted    bool            `json:"interrupted"`
	Passed         bool            `json:"passed"`
	FailReason     string          `json:"fail_reason,omitempty"`
	ElapsedMS      float64         `json:"elapsed_ms"`
	Total          int             `json:"total"`
	Successes      int             `json:"responses"`
	Errors         int             `json:"errors"`
	Aborted        int             `json:"aborted,omitempty"`
	RPS            float64         `json:"requests_per_sec"`
	BytesRead      int64           `json:"bytes_read"`
	ConnsNew       int64           `json:"connections_opened"`
	ConnsReused    int64           `json:"connections_reused"`
	Latency        *jsonLatency    `json:"latency_ms,omitempty"`
	Histogram      []jsonBucket    `json:"histogram,omitempty"`
	StatusCodes    map[string]int  `json:"status_codes"`
	ErrorBreakdown map[string]int  `json:"error_breakdown"`
	Thresholds     []jsonThreshold `json:"thresholds,omitempty"`
	StagesSpec     string          `json:"stages_configured,omitempty"`
	Stages         []jsonStage     `json:"stages,omitempty"`
	Series         []jsonSeriesRow `json:"series"`
}

type jsonLatency struct {
	Min    float64            `json:"min"`
	Mean   float64            `json:"mean"`
	StdDev float64            `json:"stddev"`
	Max    float64            `json:"max"`
	P      map[string]float64 `json:"percentiles"`
}

type jsonBucket struct {
	UpToMS float64 `json:"up_to_ms"`
	Count  int     `json:"count"`
}

// WriteJSON prints the report as one indented JSON object.
func (r *Report) WriteJSON(w io.Writer) error {
	res, cfg := r.res, r.cfg
	out := jsonReport{
		Target:         cfg.URL,
		Method:         cfg.Method,
		Concurrency:    cfg.Concurrency,
		Requests:       cfg.Requests,
		RateLimit:      cfg.RateLimit,
		Interrupted:    res.Interrupted,
		Passed:         r.verdict.Passed(),
		FailReason:     r.verdict.Reason,
		ElapsedMS:      msf(res.Elapsed),
		Total:          res.Total,
		Successes:      res.Successes,
		Errors:         res.ErrorCount(),
		Aborted:        res.Aborted,
		RPS:            res.RequestsPerSecond(),
		BytesRead:      res.BytesRead,
		ConnsNew:       res.ConnsNew,
		ConnsReused:    res.ConnsReused,
		StatusCodes:    map[string]int{},
		ErrorBreakdown: res.Errors,
		Thresholds:     toJSONThresholds(r.verdict),
		Stages:         toJSONStages(r.stages, false),
		Series:         toJSONSeries(r.ser),
	}
	if cfg.Stages != nil {
		out.StagesSpec = cfg.Stages.String()
	}
	if cfg.Duration > 0 {
		out.Duration = cfg.Duration.String()
	}
	for code, n := range res.StatusCodes {
		out.StatusCodes[fmt.Sprint(code)] = n
	}
	if s := r.latency; s.Count > 0 {
		l := &jsonLatency{Min: msf(s.Min), Mean: msf(s.Mean), StdDev: msf(s.StdDev), Max: msf(s.Max), P: map[string]float64{}}
		for _, p := range s.Percentiles {
			l.P["p"+trimFloat(p.P)] = msf(p.Value)
		}
		out.Latency = l
	}
	for _, bk := range r.hist {
		out.Histogram = append(out.Histogram, jsonBucket{UpToMS: msf(bk.High), Count: bk.Count})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// WriteSeriesCSV writes the per-second series as CSV, for -series.
func (r *Report) WriteSeriesCSV(w io.Writer) error { return writeSeriesCSV(w, r.ser) }

func msf(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
