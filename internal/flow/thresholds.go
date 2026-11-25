package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/namnguyen3019/pelt/internal/threshold"
)

// Target is a threshold tied to what it measures in a scenario: the whole
// run, one flow or one step. Threshold.Level says which; FlowID or StepID
// points at it.
type Target struct {
	threshold.Threshold
	FlowID int // set for a flow-level target
	StepID int // set for a step-level target
}

// ResolveThreshold ties a threshold from the command line (or the file's
// top-level list) to sc. No scope means the whole run; "checkout" names a
// flow and "checkout/pay" a step, with "setup/login" for a setup step.
func (sc *Scenario) ResolveThreshold(t threshold.Threshold) (Target, error) {
	if t.Scope == "" {
		return bind(t, "run", nil, nil)
	}
	var (
		matchFlow *Flow
		matchStep *Step
		n         int
	)
	for _, fl := range sc.Flows {
		if fl.Name == t.Scope {
			matchFlow, n = fl, n+1
		}
	}
	for _, st := range sc.Steps {
		if st.Flow+"/"+st.Name == t.Scope || st.Flow+" / "+st.Name == t.Scope {
			matchStep, n = st, n+1
		}
	}
	switch {
	case n > 1:
		return Target{}, fmt.Errorf("%q names more than one flow or step; rename one of them", t.Scope)
	case matchFlow != nil:
		return bind(t, "flow", matchFlow, nil)
	case matchStep != nil:
		return bind(t, "step", nil, matchStep)
	}
	names := make([]string, len(sc.Flows))
	for i, fl := range sc.Flows {
		names[i] = fl.Name
	}
	return Target{}, fmt.Errorf("no flow or step named %q (flows: %s; name a step as flow/step)",
		t.Scope, strings.Join(names, ", "))
}

// bind fills in the target for a level and checks that the metric exists
// there.
func bind(t threshold.Threshold, level string, fl *Flow, st *Step) (Target, error) {
	tg := Target{FlowID: -1, StepID: -1}
	switch level {
	case "flow":
		t.Scope, t.Flow, t.Step = fl.Name, fl.Name, ""
		tg.FlowID = fl.ID
	case "step":
		t.Scope, t.Flow, t.Step = st.Flow+"/"+st.Name, st.Flow, st.Name
		tg.StepID = st.ID
	default:
		t.Scope, t.Flow, t.Step = "", "", ""
	}
	if err := checkLevel(t, level); err != nil {
		return Target{}, err
	}
	tg.Threshold = t
	return tg, nil
}

// checkLevel rejects a metric that is not measured at a level, e.g. errors
// on a flow or wall_p95 on the whole run.
func checkLevel(t threshold.Threshold, level string) error {
	m := t.Metric
	var where string // the levels the metric is measured at
	switch {
	case strings.HasPrefix(m, "wall_p"):
		where = "flow"
	case m == "aborted" || m == "ips":
		where = "run flow"
	case m == "setup_failed":
		where = "run"
	case t.Kind == threshold.Latency:
		where = "run flow step"
	default: // errors, status:..., rps, requests, checks
		where = "run step"
	}
	if strings.Contains(where, level) {
		return nil
	}
	names := map[string]string{"run": "the whole run", "flow": "a flow", "step": "a step"}
	var ok []string
	for _, l := range strings.Fields(where) {
		ok = append(ok, names[l])
	}
	return fmt.Errorf("%s is measured for %s, not for %s", m, strings.Join(ok, " or "), names[level])
}

// fileThresholds parses a thresholds list from the file. Entries in a flow
// or step list take their scope from where they sit, so they may not name
// one; top-level entries may, as on the command line.
func (c *compiler) fileThresholds(exprs []string, where string, fl *Flow, st *Step) ([]Target, error) {
	var out []Target
	for i, expr := range exprs {
		fail := func(err error) error {
			return fmt.Errorf("%s%s: thresholds[%d]: %w", locate(c.raw, expr), where, i, err)
		}
		t, err := threshold.Parse(expr)
		if err != nil {
			return nil, fail(err)
		}
		var tg Target
		switch {
		case st != nil || fl != nil:
			if t.Scope != "" {
				return nil, fail(fmt.Errorf("%q: leave out %q; a threshold here applies to this %s",
					expr, t.Scope+":", map[bool]string{true: "step", false: "flow"}[st != nil]))
			}
			if st != nil {
				tg, err = bind(t, "step", nil, st)
			} else {
				tg, err = bind(t, "flow", fl, nil)
			}
		default:
			tg, err = c.sc.ResolveThreshold(t)
		}
		if err != nil {
			return nil, fail(fmt.Errorf("%q: %w", expr, err))
		}
		out = append(out, tg)
	}
	return out, nil
}

// locate returns "line N, column M: " for the first place s appears as a
// JSON string in raw, or "" if it cannot be found.
func locate(raw []byte, s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(s) != nil {
		return ""
	}
	off := bytes.Index(raw, bytes.TrimSpace(buf.Bytes()))
	if off < 0 {
		return ""
	}
	line := 1 + bytes.Count(raw[:off], []byte("\n"))
	col := off - bytes.LastIndexByte(raw[:off], '\n')
	return fmt.Sprintf("line %d, column %d: ", line, col)
}
