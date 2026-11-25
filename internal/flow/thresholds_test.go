package flow

import (
	"strings"
	"testing"

	"github.com/namnguyen3019/pelt/internal/threshold"
)

const thresholdScenario = `{
  "base_url": "http://127.0.0.1:1",
  "thresholds": ["p95<500ms", "checks<0.5%", "checkout/pay: errors<1%"],
  "setup": [{"name": "login", "url": "/login", "thresholds": ["p99<1s"]}],
  "flows": [
    {"name": "browse", "steps": [{"name": "list", "url": "/products"}]},
    {"name": "checkout", "thresholds": ["p95<800ms", "aborted<2%", "wall_p99<5s"],
     "steps": [
       {"name": "list", "url": "/products"},
       {"name": "pay", "method": "POST", "url": "/checkout",
        "thresholds": ["p99<300ms", "status:5xx<0.1%"]}
     ]}
  ]
}`

func TestFileThresholds(t *testing.T) {
	sc, err := Parse([]byte(thresholdScenario), ".", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		str, level string
		flowID     int
		stepID     int
	}{
		// top level first, then setup, then each flow and its steps
		{"p95 < 500ms", "run", -1, -1},
		{"checks < 0.5%", "run", -1, -1},
		{"checkout/pay: errors < 1%", "step", -1, 3},
		{"setup/login: p99 < 1s", "step", -1, 0},
		{"checkout: p95 < 800ms", "flow", 1, -1},
		{"checkout: aborted < 2%", "flow", 1, -1},
		{"checkout: wall_p99 < 5s", "flow", 1, -1},
		{"checkout/pay: p99 < 300ms", "step", -1, 3},
		{"checkout/pay: status:5xx < 0.1%", "step", -1, 3},
	}
	if len(sc.Thresholds) != len(want) {
		t.Fatalf("got %d thresholds, want %d", len(sc.Thresholds), len(want))
	}
	for i, w := range want {
		got := sc.Thresholds[i]
		if got.String() != w.str || got.Level() != w.level || got.FlowID != w.flowID || got.StepID != w.stepID {
			t.Errorf("threshold %d = %q %s flow %d step %d, want %q %s flow %d step %d",
				i, got.String(), got.Level(), got.FlowID, got.StepID, w.str, w.level, w.flowID, w.stepID)
		}
	}
	pay := sc.Thresholds[7]
	if pay.Flow != "checkout" || pay.Step != "pay" || pay.Expr != "p99<300ms" {
		t.Errorf("step threshold = flow %q step %q expr %q", pay.Flow, pay.Step, pay.Expr)
	}
}

func TestFileThresholdErrors(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{`{"base_url": "http://x", "flows": [{"name": "a", "steps": [
  {"url": "/x", "thresholds": ["p95<1%"]}]}]}`,
			[]string{"line 2, column 32", "flows[0].steps[0] (GET /x): thresholds[0]", "not a percentage"}},
		{`{"base_url": "http://x", "flows": [{"name": "a", "thresholds": ["a: p95<1s"], "steps": [{"url": "/x"}]}]}`,
			[]string{"line 1, column 65", "flows[0] (a): thresholds[0]", `leave out "a:"`}},
		{`{"base_url": "http://x", "flows": [{"name": "a", "thresholds": ["errors<1%"], "steps": [{"url": "/x"}]}]}`,
			[]string{"errors is measured for the whole run or a step, not for a flow"}},
		{`{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/x", "thresholds": ["aborted<1%"]}]}]}`,
			[]string{"aborted is measured for the whole run or a flow, not for a step"}},
		{`{"base_url": "http://x", "thresholds": ["wall_p95<1s"], "flows": [{"name": "a", "steps": [{"url": "/x"}]}]}`,
			[]string{"top level: thresholds[0]", "wall_p95 is measured for a flow, not for the whole run"}},
		{`{"base_url": "http://x", "thresholds": ["nope: p95<1s"], "flows": [{"name": "a", "steps": [{"url": "/x"}]}]}`,
			[]string{`no flow or step named "nope" (flows: a; name a step as flow/step)`}},
		{`{"base_url": "http://x", "flows": [{"name": "a", "thresholds": "p95<1s", "steps": [{"url": "/x"}]}]}`,
			[]string{"line 1, column", "thresholds"}},
	} {
		_, err := Parse([]byte(c.file), ".", "")
		if err == nil {
			t.Errorf("Parse(%s) succeeded, want an error", c.file)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("error %q, want it to contain %q", err, w)
			}
		}
	}
}

func TestResolveThreshold(t *testing.T) {
	sc, err := Parse([]byte(thresholdScenario), ".", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		expr, level, scope string
	}{
		{"p95<1s", "run", ""},
		{"setup_failed<1", "run", ""},
		{"browse: p95<1s", "flow", "browse"},
		{"browse: ips>1", "flow", "browse"},
		{"browse/list: rps>1", "step", "browse/list"},
		{"checkout / pay: checks<1%", "step", "checkout/pay"}, // the report's spelling works too
		{"setup/login: errors<1", "step", "setup/login"},
	} {
		th, err := threshold.Parse(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		tg, err := sc.ResolveThreshold(th)
		if err != nil {
			t.Errorf("ResolveThreshold(%q): %v", c.expr, err)
			continue
		}
		if tg.Level() != c.level || tg.Scope != c.scope {
			t.Errorf("ResolveThreshold(%q) = %s %q, want %s %q", c.expr, tg.Level(), tg.Scope, c.level, c.scope)
		}
	}

	for expr, want := range map[string]string{
		"shop: p95<1s":            `no flow or step named "shop"`,
		"browse/pay: p95<1s":      `no flow or step named "browse/pay"`,
		"setup: p95<1s":           `no flow or step named "setup"`,
		"browse: requests>1":      "requests is measured for the whole run or a step, not for a flow",
		"browse/list: ips>1":      "ips is measured for the whole run or a flow, not for a step",
		"browse: setup_failed<1":  "setup_failed is measured for the whole run, not for a flow",
		"browse/list: wall_p99<1": "wall_p99 is measured for a flow, not for a step",
	} {
		th, err := threshold.Parse(expr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sc.ResolveThreshold(th); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ResolveThreshold(%q) error %v, want %q", expr, err, want)
		}
	}
}

func TestResolveThresholdAmbiguous(t *testing.T) {
	sc, err := Parse([]byte(`{"base_url": "http://x", "flows": [
	  {"name": "a/b", "steps": [{"url": "/x"}]},
	  {"name": "a", "steps": [{"name": "b", "url": "/y"}]}]}`), ".", "")
	if err != nil {
		t.Fatal(err)
	}
	th, _ := threshold.Parse("a/b: p95<1s")
	if _, err := sc.ResolveThreshold(th); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("ambiguous scope error = %v", err)
	}
}
