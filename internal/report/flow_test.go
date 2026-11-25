package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/namnguyen3019/pelt/internal/flow"
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
