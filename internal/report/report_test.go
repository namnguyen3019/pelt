package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/namnguyen3019/pelt/internal/loadtest"
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
