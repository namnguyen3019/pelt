package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/namnguyen3019/pelt/internal/loadtest"
	"github.com/namnguyen3019/pelt/internal/threshold"
)

func sampleResult() (loadtest.Config, *loadtest.Result) {
	cfg := loadtest.Config{URL: "http://127.0.0.1:8080/", Method: "GET", Requests: 4, Concurrency: 2}
	res := &loadtest.Result{
		Elapsed:     2 * time.Second,
		Total:       5,
		Successes:   4,
		Latencies:   []time.Duration{4 * time.Millisecond, 1 * time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond},
		StatusCodes: map[int]int{200: 3, 500: 1},
		Errors:      map[string]int{"timeout": 1},
		BytesRead:   2048,
		ConnsNew:    2,
		ConnsReused: 3,
	}
	return cfg, res
}

func TestWriteText(t *testing.T) {
	var buf bytes.Buffer
	if err := New(sampleResult()).WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Target:       GET http://127.0.0.1:8080/",
		"5 completed, 4 responses, 1 errors (20.00%)",
		"2.5 requests/sec",
		"2 opened, 3 requests reused a connection",
		"min     1.000 ms",
		"mean    2.500 ms",
		"p50     2.000 ms",
		"p99     4.000 ms",
		"200  3",
		"500  1",
		"1  timeout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Interrupted") {
		t.Error("report says Interrupted for a complete run")
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := New(sampleResult()).WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var got jsonReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if got.Total != 5 || got.Errors != 1 || got.RPS != 2.5 {
		t.Errorf("total/errors/rps = %d/%d/%v, want 5/1/2.5", got.Total, got.Errors, got.RPS)
	}
	if got.Latency == nil || got.Latency.P["p50"] != 2 || got.Latency.Max != 4 {
		t.Errorf("latency = %+v, want p50=2 max=4", got.Latency)
	}
	if got.StatusCodes["200"] != 3 || got.ErrorBreakdown["timeout"] != 1 {
		t.Errorf("status=%v errors=%v", got.StatusCodes, got.ErrorBreakdown)
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[float64]string{512: "512 B", 2048: "2.0 KiB", 3 * 1024 * 1024: "3.0 MiB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func mustParse(t *testing.T, exprs ...string) []threshold.Threshold {
	t.Helper()
	var ts []threshold.Threshold
	for _, e := range exprs {
		th, err := threshold.Parse(e)
		if err != nil {
			t.Fatal(err)
		}
		ts = append(ts, th)
	}
	return ts
}

func TestCheckThresholds(t *testing.T) {
	rep := New(sampleResult()) // latencies 1-4 ms, 1 of 5 requests failed, one 500, 2.5 req/s
	v := rep.Check(mustParse(t,
		"p50<=2ms", "p99<3ms", "errors<10%", "status:5xx<=1", "rps>2", "requests>=5", "max<5", "status:2xx>=60%"))
	if v.Passed() {
		t.Fatal("verdict passed, want p99 and errors to fail")
	}
	if got := v.Summary(); got != "2 of 8 thresholds missed" {
		t.Errorf("Summary = %q", got)
	}
	// Failures first, in the order given.
	if v.Outcomes[0].Metric != "p99" || v.Outcomes[1].Metric != "errors" || !v.Outcomes[2].Pass {
		t.Errorf("outcome order: %s, %s, %v", v.Outcomes[0].Metric, v.Outcomes[1].Metric, v.Outcomes[2].Pass)
	}

	var buf bytes.Buffer
	if err := rep.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Thresholds:\n",
		"FAIL  p99 < 3ms ",
		"4.000 ms",
		"FAIL  errors < 10% ",
		"20.00%  (1 of 5)",
		"PASS  status:5xx <= 1 ",
		"1 of 5",
		"PASS  rps > 2 ",
		"2.5/s",
		"PASS  status:2xx >= 60% ",
		"60.00%  (3 of 5)",
		"Result: FAIL (2 of 8 thresholds missed)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q\n%s", want, out)
		}
	}

	buf.Reset()
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var got jsonReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Passed || len(got.Thresholds) != 8 {
		t.Fatalf("passed=%v thresholds=%d, want false and 8", got.Passed, len(got.Thresholds))
	}
	e := got.Thresholds[1]
	if e.Metric != "errors" || e.Pass || e.Unit != "%" || e.Target != 10 || e.Actual == nil || *e.Actual != 20 ||
		e.Count == nil || *e.Count != 1 || *e.Of != 5 || e.Scope != "run" {
		t.Errorf("errors threshold JSON = %+v", e)
	}
}

func TestCheckPass(t *testing.T) {
	rep := New(sampleResult())
	if v := rep.Check(mustParse(t, "p95<1s", "errors<50%")); !v.Passed() || v.Summary() != "" {
		t.Fatalf("verdict %+v, want a pass", v)
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	if !strings.Contains(buf.String(), "Result: PASS (all 2 thresholds met)") {
		t.Errorf("missing PASS line:\n%s", buf.String())
	}
}

func TestNoThresholdsNoResultLine(t *testing.T) {
	var buf bytes.Buffer
	rep := New(sampleResult())
	rep.WriteText(&buf)
	if strings.Contains(buf.String(), "Result:") || strings.Contains(buf.String(), "Thresholds:") {
		t.Errorf("a plain passing run should print no verdict:\n%s", buf.String())
	}
	buf.Reset()
	rep.WriteJSON(&buf)
	var got jsonReport
	json.Unmarshal(buf.Bytes(), &got)
	if !got.Passed || got.Thresholds != nil || got.FailReason != "" {
		t.Errorf("passed=%v thresholds=%v reason=%q", got.Passed, got.Thresholds, got.FailReason)
	}
}

func TestNoResponseFails(t *testing.T) {
	cfg := loadtest.Config{URL: "http://127.0.0.1:1/", Method: "GET", Requests: 3, Concurrency: 1}
	res := &loadtest.Result{Elapsed: time.Second, Total: 3, StatusCodes: map[int]int{},
		Errors: map[string]int{"connection refused": 3}}
	rep := New(cfg, res)
	// A latency threshold has nothing to measure and fails as well.
	v := rep.Check(mustParse(t, "p95<1s"))
	if v.Passed() || v.Reason != "no request got a response" || !v.Outcomes[0].NoSamples {
		t.Fatalf("verdict %+v", v)
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	for _, want := range []string{"FAIL  p95 < 1s  no samples", "Result: FAIL (no request got a response; 1 of 1 threshold missed)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text missing %q:\n%s", want, buf.String())
		}
	}
	buf.Reset()
	rep.WriteJSON(&buf)
	var got jsonReport
	json.Unmarshal(buf.Bytes(), &got)
	if got.Passed || got.FailReason != "no request got a response" || got.Thresholds[0].Actual != nil || !got.Thresholds[0].NoSamples {
		t.Errorf("JSON passed=%v reason=%q thresholds=%+v", got.Passed, got.FailReason, got.Thresholds)
	}
}
