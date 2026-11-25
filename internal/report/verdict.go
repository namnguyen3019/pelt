package report

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/namnguyen3019/pelt/internal/stats"
	"github.com/namnguyen3019/pelt/internal/threshold"
)

// Verdict is whether a run passed: its thresholds, plus the built-in rule
// that a run with no responses at all never passes.
type Verdict struct {
	// Outcomes holds one entry per threshold, failures first.
	Outcomes []threshold.Outcome
	// Reason is set when the built-in rule failed the run, e.g. "no
	// request got a response".
	Reason string
}

// Passed reports whether every threshold was met and the built-in rule held.
func (v Verdict) Passed() bool {
	if v.Reason != "" {
		return false
	}
	for _, o := range v.Outcomes {
		if !o.Pass {
			return false
		}
	}
	return true
}

// Summary is a one-line account of a failure, for stderr: "2 of 4
// thresholds missed" or "no request got a response". It is empty on a pass.
func (v Verdict) Summary() string {
	var parts []string
	if v.Reason != "" {
		parts = append(parts, v.Reason)
	}
	if total, failed := threshold.Summary(v.Outcomes); failed > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d %s missed", failed, total, plural(total, "threshold")))
	}
	return strings.Join(parts, "; ")
}

func newVerdict(outcomes []threshold.Outcome, reason string) Verdict {
	// Failures first, otherwise in the order given.
	slices.SortStableFunc(outcomes, func(a, b threshold.Outcome) int {
		switch {
		case a.Pass == b.Pass:
			return 0
		case !a.Pass:
			return -1
		}
		return 1
	})
	return Verdict{Outcomes: outcomes, Reason: reason}
}

// writeVerdict prints the Thresholds block and the Result line. With no
// thresholds and a passing run it prints nothing, so plain runs look the
// same as before thresholds existed.
func writeVerdict(b *strings.Builder, v Verdict) {
	if len(v.Outcomes) > 0 {
		b.WriteString("\nThresholds:\n")
		w := 0
		for _, o := range v.Outcomes {
			w = max(w, len(o.Threshold.String()))
		}
		for _, o := range v.Outcomes {
			mark := "PASS"
			if !o.Pass {
				mark = "FAIL"
			}
			fmt.Fprintf(b, "  %s  %-*s  %s\n", mark, w, o.Threshold.String(), describeMeasured(o))
		}
	}
	switch {
	case !v.Passed():
		fmt.Fprintf(b, "\nResult: FAIL (%s)\n", v.Summary())
	case len(v.Outcomes) > 0:
		fmt.Fprintf(b, "\nResult: PASS (%s met)\n", countWord(len(v.Outcomes), "threshold"))
	}
}

func countWord(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("all %d %ss", n, word)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// describeMeasured renders the measured side of a threshold line.
func describeMeasured(o threshold.Outcome) string {
	if o.NoSamples {
		return "no samples"
	}
	switch o.Unit {
	case threshold.UnitMS:
		return fmt.Sprintf("%.3f ms", o.Measured.Value)
	case threshold.UnitPercent:
		return fmt.Sprintf("%.2f%%  (%d of %d)", o.Measured.Value, o.Count, o.Of)
	case threshold.UnitNumber:
		return fmt.Sprintf("%.1f/s", o.Measured.Value)
	}
	if o.Kind == threshold.Rate {
		return fmt.Sprintf("%d of %d", o.Count, o.Of)
	}
	return fmt.Sprintf("%.0f", o.Measured.Value)
}

// rate builds the Measured value for a share or count: count events out
// of `of`, expressed in the threshold's unit.
func rate(t threshold.Threshold, count, of int) threshold.Measured {
	m := threshold.Measured{Count: count, Of: of}
	if of == 0 {
		m.NoSamples = true
		return m
	}
	if t.Unit == threshold.UnitPercent {
		m.Value = 100 * float64(count) / float64(of)
	} else {
		m.Value = float64(count)
	}
	return m
}

// statusCount counts responses whose status falls in [lo, hi].
func statusCount(codes map[int]int, lo, hi int) int {
	n := 0
	for code, c := range codes {
		if code >= lo && code <= hi {
			n += c
		}
	}
	return n
}

// latency measures a latency metric over sorted samples and their summary.
func latency(t threshold.Threshold, sorted []time.Duration, s stats.Summary) threshold.Measured {
	if s.Count == 0 {
		return threshold.Measured{NoSamples: true}
	}
	switch t.Metric {
	case "mean":
		return threshold.Measured{Value: msf(s.Mean)}
	case "max":
		return threshold.Measured{Value: msf(s.Max)}
	}
	return threshold.Measured{Value: msf(stats.PercentileOf(sorted, t.Percentile))}
}

// JSON form of a threshold outcome.
type jsonThreshold struct {
	Scope     string   `json:"scope"` // run, flow or step
	Flow      string   `json:"flow,omitempty"`
	Step      string   `json:"step,omitempty"`
	Expr      string   `json:"expr"`
	Metric    string   `json:"metric"`
	Op        string   `json:"op"`
	Target    float64  `json:"target"`
	Actual    *float64 `json:"actual"` // null when there were no samples
	Unit      string   `json:"unit"`
	Count     *int     `json:"count,omitempty"`
	Of        *int     `json:"of,omitempty"`
	NoSamples bool     `json:"no_samples,omitempty"`
	Pass      bool     `json:"pass"`
}

func toJSONThresholds(v Verdict) []jsonThreshold {
	var out []jsonThreshold
	for _, o := range v.Outcomes {
		jt := jsonThreshold{
			Scope: o.Level(), Flow: o.Threshold.Flow, Step: o.Threshold.Step,
			Expr: o.Expr, Metric: o.Metric, Op: string(o.Op),
			Target: o.Threshold.Value, NoSamples: o.NoSamples, Pass: o.Pass,
		}
		switch o.Unit {
		case threshold.UnitMS:
			jt.Unit = "ms"
		case threshold.UnitPercent:
			jt.Unit = "%"
		case threshold.UnitNumber:
			jt.Unit = "per_sec"
		default:
			jt.Unit = "count"
		}
		if !o.NoSamples {
			a := o.Measured.Value
			jt.Actual = &a
		}
		if o.Kind == threshold.Rate {
			c, of := o.Count, o.Of
			jt.Count, jt.Of = &c, &of
		}
		out = append(out, jt)
	}
	return out
}
