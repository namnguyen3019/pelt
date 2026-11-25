package report

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/namnguyen3019/pelt/internal/flow"
	"github.com/namnguyen3019/pelt/internal/threshold"
)

func sampleFlowResult(t *testing.T) *flow.Result {
	t.Helper()
	sc, err := flow.Parse([]byte(`{
	  "name": "shop",
	  "base_url": "http://127.0.0.1:8080",
	  "setup": [{"name": "login", "method": "POST", "url": "/login"}],
	  "flows": [
	    {"name": "browse", "weight": 7, "steps": [{"name": "list products", "url": "/products"}]},
	    {"name": "checkout", "weight": 3, "steps": [
	      {"name": "list products", "url": "/products"},
	      {"name": "pay", "method": "POST", "url": "/checkout"}]}
	  ]}`), ".", "")
	if err != nil {
		t.Fatal(err)
	}
	ms := time.Millisecond
	res := &flow.Result{
		Scenario: sc,
		Options:  flow.Options{Users: 2, Duration: 10 * time.Second, Seed: 42},
		Elapsed:  2 * time.Second,
		Steps: []flow.StepResult{
			{Step: sc.Steps[0], Requests: 2, Responses: 2, Latencies: []time.Duration{2 * ms, 4 * ms},
				StatusCodes: map[int]int{200: 2}, Errors: map[string]int{}},
			{Step: sc.Steps[1], Requests: 3, Responses: 3, Latencies: []time.Duration{1 * ms, 2 * ms, 3 * ms},
				StatusCodes: map[int]int{200: 3}, Errors: map[string]int{}},
			{Step: sc.Steps[2], Requests: 2, Responses: 2, Latencies: []time.Duration{1 * ms, 1 * ms},
				StatusCodes: map[int]int{200: 2}, Errors: map[string]int{}},
			{Step: sc.Steps[3], Requests: 2, Responses: 1, CheckFails: 1, AbortedIterations: 1,
				Latencies: []time.Duration{5 * ms}, StatusCodes: map[int]int{409: 1}, Errors: map[string]int{"timeout": 1}},
		},
		Flows: []flow.FlowResult{
			{Flow: sc.Flows[0], Started: 3, Completed: 3, FlowTimes: []time.Duration{1 * ms, 2 * ms, 3 * ms},
				WallTimes: []time.Duration{100 * ms, 200 * ms, 300 * ms}},
			{Flow: sc.Flows[1], Started: 2, Aborted: 1, Cut: 1},
		},
		ConnsNew:    2,
		ConnsReused: 7,
	}
	return res
}

func TestFlowText(t *testing.T) {
	var buf bytes.Buffer
	if err := NewFlow(sampleFlowResult(t)).WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Scenario:     shop",
		"Load:         2 users, for 10s, seed 42",
		"Iterations:   5 started: 3 completed, 1 aborted, 1 cut by the end of the run (2.5/sec)",
		"Requests:     9 completed, 8 responses, 1 errors (11.11%), 1 failed checks",
		"Connections:  2 opened, 7 requests reused a connection",
		"browse         7        3        3        0      0       2.000       3.000       3.000     200.000",
		"checkout       3        2        0        1      1           -",
		"setup / login",
		"checkout / pay",
		"Aborted at step:\n       1  checkout / pay",
		"409  1",
		"1  timeout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q\n%s", want, out)
		}
	}
}

func TestFlowJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := NewFlow(sampleFlowResult(t)).WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var got jsonFlowReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if got.Mode != "flow" || got.Total != 9 || got.Errors != 1 || got.CheckFails != 1 || got.Seed != 42 {
		t.Errorf("top level = %+v", got)
	}
	if got.Iters.Started != 5 || got.Iters.Completed != 3 || got.Iters.PerSec != 2.5 {
		t.Errorf("iterations = %+v", got.Iters)
	}
	if len(got.Flows) != 2 || got.Flows[1].AbortedAt["pay"] != 1 || got.Flows[0].FlowTime.P["p50"] != 2 {
		t.Errorf("flows = %+v", got.Flows)
	}
	if got.Flows[1].FlowTime != nil {
		t.Error("a flow with no completed iterations should have no flow_time_ms")
	}
	if len(got.Steps) != 4 || got.Steps[3].Name != "pay" || got.Steps[3].StatusCodes["409"] != 1 || got.Steps[0].Latency.Max != 4 {
		t.Errorf("steps = %+v", got.Steps)
	}
}

func TestFlowNoIterationCompletedFails(t *testing.T) {
	res := sampleFlowResult(t)
	if v := NewFlow(sampleFlowResult(t)).Verdict(); !v.Passed() {
		t.Fatalf("sample run with completed iterations failed: %+v", v)
	}
	res.Flows[0].Completed = 0
	res.Flows[0].Cut = 3
	rep := NewFlow(res)
	if v := rep.Verdict(); v.Passed() || v.Reason != "no iteration completed" {
		t.Fatalf("verdict %+v, want a failure", v)
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	if !strings.Contains(buf.String(), "Result: FAIL (no iteration completed)") {
		t.Errorf("missing FAIL line:\n%s", buf.String())
	}
	buf.Reset()
	rep.WriteJSON(&buf)
	var got struct {
		Passed     bool   `json:"passed"`
		FailReason string `json:"fail_reason"`
	}
	json.Unmarshal(buf.Bytes(), &got)
	if got.Passed || got.FailReason != "no iteration completed" {
		t.Errorf("JSON passed=%v reason=%q", got.Passed, got.FailReason)
	}
}

// targets resolves threshold expressions against the sample scenario.
func targets(t *testing.T, sc *flow.Scenario, exprs ...string) []flow.Target {
	t.Helper()
	var out []flow.Target
	for _, e := range exprs {
		th, err := threshold.Parse(e)
		if err != nil {
			t.Fatal(err)
		}
		tg, err := sc.ResolveThreshold(th)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tg)
	}
	return out
}

func TestFlowCheckThresholds(t *testing.T) {
	res := sampleFlowResult(t)
	rep := NewFlow(res)
	v := rep.Check(targets(t, res.Scenario,
		// whole run: responses are 1,1,1,2,2,3,4,5 ms
		"p95<5ms",        // 5.000 ms: FAIL
		"max<=5ms",       // PASS
		"errors<10%",     // 1 of 9: FAIL
		"checks<=1",      // 1 of 9: PASS
		"aborted<50%",    // 1 of 5 started: PASS
		"ips>=2.5",       // 5 started in 2s: PASS
		"setup_failed<1", // PASS
		// flows
		"browse: p99<3ms",         // flow time 3.000 ms: FAIL
		"browse: wall_p50<=200ms", // PASS
		"checkout: aborted<50%",   // 1 of 2: FAIL
		"checkout: p95<1s",        // no completed iteration: FAIL, no samples
		// steps
		"checkout/pay: status:4xx<=0", // 1 of 2: FAIL
		"checkout/pay: errors<1%",     // 1 of 2: FAIL
		"setup/login: p50<=2ms",       // PASS
		"browse/list products: rps>1", // 1.5/s: PASS
	))
	if v.Passed() {
		t.Fatal("verdict passed, want a failure")
	}
	if got := v.Summary(); got != "7 of 15 thresholds missed" {
		t.Errorf("summary %q", got)
	}

	var buf bytes.Buffer
	rep.WriteText(&buf)
	out := buf.String()
	for _, re := range []string{
		`FAIL  p95 < 5ms\s+5\.000 ms`,
		`FAIL  errors < 10%\s+11\.11%  \(1 of 9\)`,
		`FAIL  browse: p99 < 3ms\s+3\.000 ms`,
		`FAIL  checkout: aborted < 50%\s+50\.00%  \(1 of 2\)`,
		`FAIL  checkout: p95 < 1s\s+no samples`,
		`FAIL  checkout/pay: status:4xx <= 0\s+1 of 2`,
		`PASS  browse: wall_p50 <= 200ms\s+200\.000 ms`,
		`PASS  ips >= 2\.5\s+2\.5/s`,
		`PASS  setup/login: p50 <= 2ms\s+2\.000 ms`,
		`PASS  browse/list products: rps > 1\s+1\.5/s`,
		`Result: FAIL \(7 of 15 thresholds missed\)`,
	} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("text report missing %s\n%s", re, out)
		}
	}
	if strings.Index(out, "FAIL  p95") > strings.Index(out, "PASS  max") {
		t.Error("failures should be listed first")
	}

	buf.Reset()
	rep.WriteJSON(&buf)
	var got struct {
		Passed     bool            `json:"passed"`
		Thresholds []jsonThreshold `json:"thresholds"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Passed || len(got.Thresholds) != 15 {
		t.Fatalf("passed=%v, %d thresholds", got.Passed, len(got.Thresholds))
	}
	byExpr := map[string]jsonThreshold{}
	for _, jt := range got.Thresholds {
		byExpr[jt.Expr] = jt
	}
	if jt := byExpr["checkout/pay: errors<1%"]; jt.Scope != "step" || jt.Flow != "checkout" || jt.Step != "pay" ||
		*jt.Actual != 50 || *jt.Count != 1 || *jt.Of != 2 || jt.Pass {
		t.Errorf("step threshold JSON = %+v", jt)
	}
	if jt := byExpr["browse: wall_p50<=200ms"]; jt.Scope != "flow" || jt.Flow != "browse" || jt.Step != "" || *jt.Actual != 200 || !jt.Pass {
		t.Errorf("flow threshold JSON = %+v", jt)
	}
	if jt := byExpr["checkout: p95<1s"]; jt.Actual != nil || !jt.NoSamples || jt.Pass {
		t.Errorf("no-samples threshold JSON = %+v", jt)
	}
	if jt := byExpr["p95<5ms"]; jt.Scope != "run" || jt.Flow != "" || *jt.Actual != 5 {
		t.Errorf("run threshold JSON = %+v", jt)
	}
}

func TestFlowCheckPass(t *testing.T) {
	res := sampleFlowResult(t)
	rep := NewFlow(res)
	if v := rep.Check(targets(t, res.Scenario, "mean<3ms", "browse: p95<10ms")); !v.Passed() {
		t.Fatalf("verdict %+v, want a pass", v)
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	if !strings.Contains(buf.String(), "Result: PASS (all 2 thresholds met)") {
		t.Errorf("missing PASS line:\n%s", buf.String())
	}
}
