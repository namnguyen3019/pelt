package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/namnguyen3019/pelt/internal/loadtest"
	"github.com/namnguyen3019/pelt/internal/report"
	"github.com/namnguyen3019/pelt/internal/series"
)

func TestHeaderFlag(t *testing.T) {
	var h headerFlag
	for _, v := range []string{"Authorization: Bearer abc", "X-Multi: 1", "X-Multi:2"} {
		if err := h.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if got := h.h.Get("Authorization"); got != "Bearer abc" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.h.Values("X-Multi"); len(got) != 2 || got[1] != "2" {
		t.Errorf("X-Multi = %v, want [1 2]", got)
	}
	if err := h.Set("no-colon"); err == nil {
		t.Error("Set without a colon should fail")
	}
}

// TestRunEndToEnd drives the CLI exactly as a user would, against a local
// httptest server, and checks the JSON report.
func TestRunEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "t" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{"-n", "300", "-c", "30", "-H", "X-Token: t", "-o", "json", srv.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}

	var rep struct {
		Total       int            `json:"total"`
		Errors      int            `json:"errors"`
		StatusCodes map[string]int `json:"status_codes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, stdout.String())
	}
	if rep.Total != 300 || rep.Errors != 0 || rep.StatusCodes["200"] != 300 {
		t.Errorf("report = %+v, want 300 x 200 and no errors", rep)
	}
}

func TestRunBadArgs(t *testing.T) {
	for _, args := range [][]string{
		{},                                   // no URL
		{"-o", "xml", "http://127.0.0.1:1/"}, // bad output format
		{"-c", "0", "http://127.0.0.1:1/"},   // bad concurrency
		{"-body", "a", "-body-file", "f", "http://127.0.0.1:1/"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("run(%q) = %d, want 2 (stderr: %s)", strings.Join(args, " "), code, errOut.String())
		}
	}
}

// flakyServer answers every tenth request with a 500.
func flakyServer(t *testing.T) *httptest.Server {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%10 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestThresholdExitCodes(t *testing.T) {
	srv := flakyServer(t)
	for _, tc := range []struct {
		name       string
		thresholds []string
		output     string
		want       int
	}{
		{"missed", []string{"status:5xx<1%", "p95<10s"}, "text", 3},
		{"missed json", []string{"status:5xx<1%"}, "json", 3},
		{"met", []string{"status:5xx<=10%", "errors<1%", "p99<10s", "requests>=200"}, "text", 0},
		{"none", nil, "text", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-n", "200", "-c", "1", "-quiet", "-o", tc.output}
			for _, th := range tc.thresholds {
				args = append(args, "-threshold", th)
			}
			var stdout, stderr bytes.Buffer
			code := run(append(args, srv.URL), &stdout, &stderr)
			if code != tc.want {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.want, stdout.String(), stderr.String())
			}
			if tc.output == "json" {
				var rep struct {
					Passed     bool `json:"passed"`
					Thresholds []struct {
						Expr string `json:"expr"`
						Pass bool   `json:"pass"`
					} `json:"thresholds"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
					t.Fatalf("stdout is not pure JSON: %v\n%s", err, stdout.String())
				}
				if rep.Passed || len(rep.Thresholds) != 1 || rep.Thresholds[0].Pass {
					t.Errorf("JSON %+v", rep)
				}
				if !strings.Contains(stderr.String(), "pelt: FAIL: 1 of 1 threshold missed") {
					t.Errorf("stderr = %q, want the failure line", stderr.String())
				}
			}
			if tc.want == 3 && tc.output == "text" && !strings.Contains(stdout.String(), "Result: FAIL") {
				t.Errorf("text report has no FAIL line:\n%s", stdout.String())
			}
		})
	}
}

// TestNoResponseExits3 runs against a port nothing listens on: every
// request is refused, so the run fails without any threshold set.
func TestNoResponseExits3(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-n", "5", "-c", "1", "-quiet", url}, &stdout, &stderr); code != 3 {
		t.Fatalf("exit %d, want 3\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Result: FAIL (no request got a response)") {
		t.Errorf("missing FAIL line:\n%s", stdout.String())
	}
}

func TestBadThresholds(t *testing.T) {
	for _, th := range []string{"p95", "p95<", "latency<1s", "errors<200%", "checkout: p95<1s", "checks<1%", "wall_p95<1s"} {
		var out, errOut bytes.Buffer
		if code := run([]string{"-threshold", th, "http://127.0.0.1:1/"}, &out, &errOut); code != 2 {
			t.Errorf("-threshold %q: exit %d, want 2 (stderr: %s)", th, code, errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("-threshold %q printed a report; nothing should run", th)
		}
	}
}

func TestFinishExitCodes(t *testing.T) {
	failed := report.Verdict{Reason: "no request got a response"}
	for _, tc := range []struct {
		v           report.Verdict
		interrupted bool
		want        int
	}{
		{report.Verdict{}, false, 0},
		{failed, false, 3},
		{report.Verdict{}, true, 130},
		{failed, true, 130}, // Ctrl-C wins: the person stopped the run
	} {
		var stderr bytes.Buffer
		if got := finish(tc.v, tc.interrupted, false, &stderr); got != tc.want {
			t.Errorf("finish(passed=%v, interrupted=%v) = %d, want %d", tc.v.Passed(), tc.interrupted, got, tc.want)
		}
	}
}

func TestRunSeriesCSV(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "s.csv")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-n", "100", "-c", "5", "-series", path, srv.URL}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if !strings.HasPrefix(lines[0], "t,stage,target,active,requests,") || len(lines) < 2 {
		t.Fatalf("CSV = %q", b)
	}
	total := 0
	for _, l := range lines[1:] {
		n, _ := strconv.Atoi(strings.Split(l, ",")[4])
		total += n
	}
	if total != 100 {
		t.Errorf("CSV rows add up to %d requests, want 100", total)
	}

	// A path that cannot be created fails before any load is sent.
	hits.Store(0)
	bad := filepath.Join(t.TempDir(), "missing", "s.csv")
	if code := run([]string{"-n", "10", "-series", bad, srv.URL}, &stdout, &stderr); code != exitError {
		t.Errorf("bad -series path: exit %d, want %d", code, exitError)
	}
	if hits.Load() != 0 {
		t.Errorf("server got %d requests after a bad -series path", hits.Load())
	}
}

func TestProgressLine(t *testing.T) {
	got := progressLine(loadtest.Progress{Completed: 12345, Errors: 3, LastSecond: series.Count{Requests: 1402}})
	if want := "12,345 requests, 1,402 req/s last second, 3 errors"; got != want {
		t.Errorf("progress line = %q, want %q", got, want)
	}
}
