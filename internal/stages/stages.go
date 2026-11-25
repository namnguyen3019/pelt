// Package stages describes load that changes over a run: ramp up, hold,
// ramp down, or step from one level to another.
//
// A stage is a duration and a target. The level moves in a straight line
// from the previous stage's target to this one over the duration; the run
// starts at 0, and a stage of 0s jumps. On the command line a plan is a
// comma list such as 30s:200,2m:200,30s:0. A bare number is a level
// (concurrency in single-URL mode, users in flow mode); a number with /s
// is requests per second.
//
// The package is pure: no clocks or goroutines. Runners ask it what the
// target is at a given time, and for rate stages when the n-th request is
// due.
package stages

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Kind is what a plan's targets mean.
type Kind int

const (
	// Level targets are a count: requests in flight in single-URL mode,
	// users in flow mode. They are whole numbers.
	Level Kind = iota
	// Rate targets are requests per second, written with /s.
	Rate
)

// Stage is one leg of a plan.
type Stage struct {
	Duration time.Duration
	Target   float64
	Name     string // optional, from a flow file
}

// Plan is a checked list of stages of one kind.
type Plan struct {
	Kind   Kind
	Stages []Stage
}

// Parse reads a comma list such as "30s:200,2m:200,30s:0" or
// "1m:500/s,5m:500/s".
func Parse(s string) (Plan, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Plan{}, errors.New("no stages given; want a list such as 30s:100,1m:100,30s:0")
	}
	var list []Stage
	kind, kindSet := Level, false
	for i, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		durS, targetS, ok := strings.Cut(part, ":")
		if !ok || strings.TrimSpace(durS) == "" || strings.TrimSpace(targetS) == "" {
			return Plan{}, fmt.Errorf("stage %d %q: want duration:target, such as 30s:100 or 1m:500/s", i+1, part)
		}
		d, err := time.ParseDuration(strings.TrimSpace(durS))
		if err != nil {
			return Plan{}, fmt.Errorf("stage %d %q: bad duration %q (use units, such as 30s or 2m)", i+1, part, durS)
		}
		targetS = strings.TrimSpace(targetS)
		k := Level
		if t, ok := strings.CutSuffix(targetS, "/s"); ok {
			k, targetS = Rate, strings.TrimSpace(t)
		}
		if kindSet && k != kind {
			return Plan{}, fmt.Errorf("stage %d %q: every stage must be a level (100) or every stage a rate (500/s), not a mix", i+1, part)
		}
		kind, kindSet = k, true
		t, err := strconv.ParseFloat(targetS, 64)
		if err != nil || math.IsNaN(t) || math.IsInf(t, 0) {
			return Plan{}, fmt.Errorf("stage %d %q: bad target %q", i+1, part, targetS)
		}
		list = append(list, Stage{Duration: d, Target: t})
	}
	p, err := New(kind, list)
	if err != nil {
		return Plan{}, err
	}
	return p, nil
}

// New checks a list of stages. Targets must not be negative, level
// targets must be whole numbers, the stages must add up to more than 0s
// and at least one target must be above 0.
func New(kind Kind, list []Stage) (Plan, error) {
	if len(list) == 0 {
		return Plan{}, errors.New("no stages given")
	}
	var total time.Duration
	top := 0.0
	for i, st := range list {
		if st.Duration < 0 {
			return Plan{}, fmt.Errorf("stage %d: duration cannot be negative", i+1)
		}
		if st.Target < 0 {
			return Plan{}, fmt.Errorf("stage %d: target cannot be negative", i+1)
		}
		if kind == Level && st.Target != math.Trunc(st.Target) {
			return Plan{}, fmt.Errorf("stage %d: target %s must be a whole number (only rates, such as 12.5/s, take decimals)", i+1, trim(st.Target))
		}
		total += st.Duration
		top = max(top, st.Target)
	}
	if total <= 0 {
		return Plan{}, errors.New("the stages add up to 0s; give at least one stage a duration")
	}
	if top == 0 {
		return Plan{}, errors.New("every stage has a target of 0, so nothing would be sent")
	}
	return Plan{Kind: kind, Stages: list}, nil
}

// Ramp is the -ramp shorthand: from 0 up to target over `over`, then hold
// at target until `total`.
func Ramp(over, total time.Duration, target float64) (Plan, error) {
	switch {
	case over <= 0:
		return Plan{}, errors.New("-ramp must be longer than 0s")
	case total <= 0:
		return Plan{}, errors.New("-ramp needs a run length (-d): it ramps up over -ramp, then holds for the rest of -d")
	case over > total:
		return Plan{}, fmt.Errorf("-ramp %s is longer than the run (%s)", Dur(over), Dur(total))
	}
	list := []Stage{{Duration: over, Target: target}}
	if total > over {
		list = append(list, Stage{Duration: total - over, Target: target})
	}
	return New(Level, list)
}

// Total is the length of the run the plan describes.
func (p Plan) Total() time.Duration {
	var t time.Duration
	for _, st := range p.Stages {
		t += st.Duration
	}
	return t
}

// Max is the highest target.
func (p Plan) Max() float64 {
	m := 0.0
	for _, st := range p.Stages {
		m = max(m, st.Target)
	}
	return m
}

// Bounds returns when stage i (0-based) starts and ends.
func (p Plan) Bounds(i int) (start, end time.Duration) {
	for j := range i {
		start += p.Stages[j].Duration
	}
	return start, start + p.Stages[i].Duration
}

// From is the level stage i (0-based) starts from: the previous target,
// or 0 for the first.
func (p Plan) From(i int) float64 {
	if i == 0 {
		return 0
	}
	return p.Stages[i-1].Target
}

// Target is the level or rate the plan asks for at time at. Before the
// start it is 0; after the end it stays at the last target.
func (p Plan) Target(at time.Duration) float64 {
	if at < 0 {
		return 0
	}
	prev := 0.0
	var start time.Duration
	for _, st := range p.Stages {
		if at < start+st.Duration {
			frac := float64(at-start) / float64(st.Duration)
			return prev + (st.Target-prev)*frac
		}
		start += st.Duration
		prev = st.Target
	}
	return prev
}

// StageAt is the 1-based number of the stage in effect at time at. A
// moment on a boundary belongs to the stage that starts there; 0s stages
// are never in effect. After the end it is the last stage.
func (p Plan) StageAt(at time.Duration) int {
	var start time.Duration
	for i, st := range p.Stages {
		if at < start+st.Duration {
			return i + 1
		}
		start += st.Duration
	}
	return len(p.Stages)
}

// Area is how many requests a rate plan asks for from the start to at:
// the integral of the rate over time.
func (p Plan) Area(at time.Duration) float64 {
	area, prev := 0.0, 0.0
	var start time.Duration
	for _, st := range p.Stages {
		if at <= start {
			break
		}
		d := st.Duration.Seconds()
		if d > 0 {
			s := math.Min((at - start).Seconds(), d)
			end := prev + (st.Target-prev)*s/d
			area += (prev + end) / 2 * s
		}
		start += st.Duration
		prev = st.Target
	}
	return area
}

// TimeFor is when a rate plan's area reaches x: the moment the x-th
// request is due. It reports false when the plan ends first.
//
// Within a stage the rate is a + k*s, so the area after s seconds is
// a*s + k*s²/2; TimeFor solves that for s.
func (p Plan) TimeFor(x float64) (time.Duration, bool) {
	if x < 0 {
		return 0, true
	}
	cum, prev := 0.0, 0.0
	var start time.Duration
	for _, st := range p.Stages {
		d := st.Duration.Seconds()
		a, b := prev, st.Target
		prev = st.Target
		if d <= 0 {
			continue
		}
		stageArea := (a + b) / 2 * d
		if stageArea > 0 && x <= cum+stageArea {
			y := x - cum
			k := (b - a) / d
			var s float64
			if math.Abs(k) < 1e-12 {
				s = y / a
			} else {
				s = (-a + math.Sqrt(math.Max(0, a*a+2*k*y))) / k
			}
			s = math.Max(0, math.Min(s, d))
			return start + time.Duration(s*float64(time.Second)), true
		}
		cum += stageArea
		start += st.Duration
	}
	return 0, false
}

// String is the plan in -stages form, such as 30s:200,2m:200,30s:0.
func (p Plan) String() string {
	parts := make([]string, len(p.Stages))
	for i, st := range p.Stages {
		parts[i] = Dur(st.Duration) + ":" + p.FormatTarget(st.Target)
	}
	return strings.Join(parts, ",")
}

// FormatTarget writes a target with /s for a rate plan.
func (p Plan) FormatTarget(t float64) string {
	if p.Kind == Rate {
		return trim(t) + "/s"
	}
	return trim(t)
}

// Dur writes a duration without trailing zero units: 2m, not 2m0s.
func Dur(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func trim(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
