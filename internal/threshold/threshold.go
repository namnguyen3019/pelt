// Package threshold parses pass/fail targets such as "p95<300ms" or
// "errors<1%" and checks measured values against them.
//
// A threshold is written as
//
//	[scope:] metric op value
//
// where op is <, <=, > or >=. The package knows nothing about how a run is
// measured: the caller looks up each metric and hands over a Measured value,
// which keeps this package pure and easy to test.
package threshold

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Kind says what sort of value a metric takes.
type Kind int

const (
	// Latency metrics take a duration: 300ms, 1.5s, or a bare number of ms.
	Latency Kind = iota
	// Rate metrics take a share (1%) or a whole count (5).
	Rate
	// Number metrics take a plain number, such as requests per second.
	Number
	// Count metrics take a whole number.
	Count
)

// Unit is how a threshold's Value is expressed.
type Unit string

const (
	UnitMS      Unit = "ms" // latency, in milliseconds
	UnitPercent Unit = "%"  // a share, 0 to 100
	UnitCount   Unit = ""   // a count of requests or responses
	UnitNumber  Unit = "/s" // a rate per second
)

// Op is a comparison operator.
type Op string

const (
	LT Op = "<"
	LE Op = "<="
	GT Op = ">"
	GE Op = ">="
)

// Threshold is one parsed target.
type Threshold struct {
	Expr   string // as the user wrote it, trimmed
	Scope  string // "" for the whole run; a flow or flow/step name otherwise
	Metric string // normalized: "p95", "mean", "errors", "status:5xx", ...
	Kind   Kind
	Op     Op
	Value  float64 // in Unit
	Unit   Unit

	// Percentile is set for pN and wall_pN metrics, e.g. 95 or 99.9.
	Percentile float64
	// StatusLow and StatusHigh are set for status:... metrics: the
	// inclusive range of status codes counted (500-599 for status:5xx).
	StatusLow, StatusHigh int

	valueText string
}

// FlowOnly reports whether the metric only exists in flow mode.
func (t Threshold) FlowOnly() bool {
	if strings.HasPrefix(t.Metric, "wall_p") {
		return true
	}
	switch t.Metric {
	case "checks", "aborted", "ips", "setup_failed":
		return true
	}
	return false
}

// String renders the threshold in a normalized form for reports:
// "p95 < 300ms", "checkout/pay: errors < 1%".
func (t Threshold) String() string {
	s := t.Metric + " " + string(t.Op) + " " + t.valueText
	if t.Scope != "" {
		s = t.Scope + ": " + s
	}
	return s
}

// Measured is what the caller found for a threshold's metric.
type Measured struct {
	// Value is in the threshold's Unit: milliseconds for latency, a
	// percentage for a rate given in %, a count for a rate given as a count.
	Value float64
	// Count and Of are set for rate metrics: Count events out of Of
	// requests (or iterations). Reports show them next to a percentage.
	Count, Of int
	// NoSamples means there was nothing to measure (no responses for a
	// latency, no requests for a rate). The threshold then fails.
	NoSamples bool
}

// Outcome is a threshold checked against what was measured.
type Outcome struct {
	Threshold
	Measured
	Pass bool
}

// Check compares m against t. A metric with no samples fails, because a
// target that was never exercised has not been met.
func Check(t Threshold, m Measured) Outcome {
	o := Outcome{Threshold: t, Measured: m}
	if m.NoSamples {
		return o
	}
	switch t.Op {
	case LT:
		o.Pass = m.Value < t.Value
	case LE:
		o.Pass = m.Value <= t.Value
	case GT:
		o.Pass = m.Value > t.Value
	case GE:
		o.Pass = m.Value >= t.Value
	}
	return o
}

// Parse reads one threshold expression.
func Parse(expr string) (Threshold, error) {
	expr = strings.TrimSpace(expr)
	t := Threshold{Expr: expr}
	if expr == "" {
		return t, errors.New("empty threshold")
	}

	i := strings.IndexAny(expr, "<>")
	if i < 0 {
		if strings.Contains(expr, "=") {
			return t, fmt.Errorf("%q: use <, <=, > or >= (= alone is not supported)", expr)
		}
		return t, fmt.Errorf("%q: no comparison; write it like p95<300ms or errors<1%%", expr)
	}
	t.Op = Op(expr[i : i+1])
	rest := expr[i+1:]
	if strings.HasPrefix(rest, "=") {
		t.Op += "="
		rest = rest[1:]
	}
	lhs, valueText := strings.TrimSpace(expr[:i]), strings.TrimSpace(rest)
	if strings.ContainsAny(valueText, "<>=") {
		return t, fmt.Errorf("%q: one comparison per threshold", expr)
	}
	if lhs == "" {
		return t, fmt.Errorf("%q: no metric before %s", expr, t.Op)
	}
	if valueText == "" {
		return t, fmt.Errorf("%q: no value after %s", expr, t.Op)
	}

	// The metric may itself hold a colon (status:5xx), so try the whole
	// left side first, then each colon from the left as the scope break.
	if err := t.setMetric(lhs); err != nil {
		found := false
		for j := 0; j < len(lhs); j++ {
			if lhs[j] != ':' {
				continue
			}
			if t.setMetric(strings.TrimSpace(lhs[j+1:])) == nil {
				t.Scope = strings.TrimSpace(lhs[:j])
				found = true
				break
			}
		}
		if !found {
			return t, fmt.Errorf("%q: %v", expr, err)
		}
		if t.Scope == "" {
			return t, fmt.Errorf("%q: empty scope before ':'", expr)
		}
	}

	if err := t.setValue(valueText); err != nil {
		return t, fmt.Errorf("%q: %v", expr, err)
	}
	return t, nil
}

// setMetric validates and normalizes a metric name.
func (t *Threshold) setMetric(name string) error {
	m := strings.ToLower(strings.TrimSpace(name))
	t.Percentile, t.StatusLow, t.StatusHigh = 0, 0, 0
	switch m {
	case "mean", "avg":
		t.Metric, t.Kind = "mean", Latency
		return nil
	case "max":
		t.Metric, t.Kind = "max", Latency
		return nil
	case "errors", "checks", "aborted":
		t.Metric, t.Kind = m, Rate
		return nil
	case "rps", "ips":
		t.Metric, t.Kind = m, Number
		return nil
	case "requests", "setup_failed":
		t.Metric, t.Kind = m, Count
		return nil
	}

	if code, ok := strings.CutPrefix(m, "status:"); ok {
		lo, hi, err := parseStatus(code)
		if err != nil {
			return err
		}
		t.Metric, t.Kind, t.StatusLow, t.StatusHigh = "status:"+code, Rate, lo, hi
		return nil
	}

	for _, prefix := range []string{"wall_p", "p"} {
		num, ok := strings.CutPrefix(m, prefix)
		if !ok || num == "" {
			continue
		}
		p, err := strconv.ParseFloat(num, 64)
		if err != nil || math.IsNaN(p) {
			break
		}
		if p <= 0 || p > 100 {
			return fmt.Errorf("percentile %s is out of range (use p1 to p100)", m)
		}
		t.Metric, t.Kind, t.Percentile = prefix+trimFloat(p), Latency, p
		return nil
	}
	return fmt.Errorf("unknown metric %q (try p95, mean, max, errors, status:5xx, rps or requests)", name)
}

func parseStatus(code string) (lo, hi int, err error) {
	if len(code) == 3 && (strings.HasSuffix(code, "xx")) && code[0] >= '1' && code[0] <= '5' {
		base := int(code[0]-'0') * 100
		return base, base + 99, nil
	}
	n, convErr := strconv.Atoi(code)
	if convErr != nil || len(code) != 3 || n < 100 || n > 599 {
		return 0, 0, fmt.Errorf("status %q: use a class like 5xx or a code like 503", code)
	}
	return n, n, nil
}

// setValue parses the right side according to the metric's kind.
func (t *Threshold) setValue(v string) error {
	t.valueText = v
	switch t.Kind {
	case Latency:
		if strings.HasSuffix(v, "%") {
			return fmt.Errorf("%s is a latency; give a duration like 300ms, not a percentage", t.Metric)
		}
		ms, err := parseLatency(v)
		if err != nil {
			return err
		}
		t.Value, t.Unit = ms, UnitMS
		// Show bare numbers with their unit so reports are unambiguous.
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			t.valueText = v + "ms"
		}
	case Rate:
		if num, ok := strings.CutSuffix(v, "%"); ok {
			p, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil || math.IsNaN(p) {
				return fmt.Errorf("bad percentage %q", v)
			}
			if p < 0 || p > 100 {
				return fmt.Errorf("percentage %q must be between 0%% and 100%%", v)
			}
			t.Value, t.Unit, t.valueText = p, UnitPercent, trimFloat(p)+"%"
			return nil
		}
		n, err := parseCount(v)
		if err != nil {
			return fmt.Errorf("%v; add %% for a share of requests, like 1%%", err)
		}
		t.Value, t.Unit = n, UnitCount
	case Number:
		if strings.HasSuffix(v, "%") || hasLetter(v) {
			return fmt.Errorf("%s takes a plain number, like %s>500", t.Metric, t.Metric)
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return fmt.Errorf("bad number %q", v)
		}
		t.Value, t.Unit = f, UnitNumber
	case Count:
		n, err := parseCount(v)
		if err != nil {
			return err
		}
		t.Value, t.Unit = n, UnitCount
	}
	return nil
}

func parseLatency(v string) (float64, error) {
	if !hasLetter(v) {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("bad duration %q", v)
		}
		if f < 0 {
			return 0, fmt.Errorf("duration %q cannot be negative", v)
		}
		return f, nil // bare number: milliseconds
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q (use units like 300ms or 1.5s)", v)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q cannot be negative", v)
	}
	return float64(d) / float64(time.Millisecond), nil
}

func parseCount(v string) (float64, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("bad count %q (a whole number)", v)
	}
	if n < 0 {
		return 0, fmt.Errorf("count %q cannot be negative", v)
	}
	return float64(n), nil
}

func hasLetter(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == 'µ'
	}) >= 0
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Summary counts outcomes: how many were checked and how many failed.
func Summary(outcomes []Outcome) (total, failed int) {
	for _, o := range outcomes {
		if !o.Pass {
			failed++
		}
	}
	return len(outcomes), failed
}
