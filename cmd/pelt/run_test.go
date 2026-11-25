package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/namnguyen3019/pelt/internal/testshop"
)

// writeShopScenario copies examples/shop.json and its CSV into a temp dir,
// with think time removed, and returns the scenario path.
func writeShopScenario(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile("../../examples/shop.json")
	if err != nil {
		t.Fatal(err)
	}
	csv, err := os.ReadFile("../../examples/users.csv")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(src), `"think": "200ms-1s"`, `"think": "0s"`, 1)
	path := filepath.Join(dir, "shop.json")
	os.WriteFile(path, []byte(s), 0o644)
	os.WriteFile(filepath.Join(dir, "users.csv"), csv, 0o644)
	return path
}

// TestRunFlowEndToEnd runs the shipped example scenario against the test
// shop, with flags after the file name overriding its load block.
func TestRunFlowEndToEnd(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	file := writeShopScenario(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", file, "-u", "8", "-i", "200", "-d", "0", "-base", srv.URL, "-seed", "3", "-o", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	var rep struct {
		Mode       string `json:"mode"`
		Users      int    `json:"users"`
		Seed       uint64 `json:"seed"`
		Errors     int    `json:"errors"`
		CheckFails int    `json:"failed_checks"`
		Iterations struct {
			Started, Completed int
		} `json:"iterations"`
		Flows []struct {
			Name      string `json:"name"`
			Completed int    `json:"completed"`
		} `json:"flows"`
		Steps []struct {
			Flow, Name string
			Requests   int
		} `json:"steps"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, stdout.String())
	}
	if rep.Mode != "flow" || rep.Users != 8 || rep.Seed != 3 {
		t.Errorf("mode/users/seed = %s/%d/%d", rep.Mode, rep.Users, rep.Seed)
	}
	if rep.Iterations.Started != 200 || rep.Iterations.Completed != 200 || rep.Errors != 0 || rep.CheckFails != 0 {
		t.Errorf("iterations %+v, errors %d, failed checks %d", rep.Iterations, rep.Errors, rep.CheckFails)
	}
	if len(rep.Flows) != 2 || len(rep.Steps) != 7 || rep.Steps[0].Name != "login" || rep.Steps[0].Requests != 8 {
		t.Errorf("flows %+v steps %+v", rep.Flows, rep.Steps)
	}
}

func TestRunFlowText(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-u", "2", "-i", "10", "-base", srv.URL, writeShopScenario(t)}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	for _, want := range []string{"Scenario:     shop", "Iterations:   10 started: 10 completed", "checkout / order status"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("missing %q in:\n%s", want, stdout.String())
		}
	}
}

func TestRunFlowDryRun(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-dry-run", "-base", srv.URL, writeShopScenario(t)}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Dry run of") || !strings.Contains(out, "capture order_id = ") || strings.Contains(out, "Iterations:") {
		t.Errorf("unexpected dry run output:\n%s", out)
	}

	// Nothing listening: the dry run reports the error and exits 1.
	stdout.Reset()
	if code := run([]string{"run", "-dry-run", "-base", "http://127.0.0.1:1", writeShopScenario(t)}, &stdout, &stderr); code != 1 {
		t.Errorf("dry run against nothing exited %d, want 1\n%s", code, stdout.String())
	}
}

func TestRunFlowBadArgs(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"flows": [{"name": "a", "steps": [{"url": "/x"}]}], "typo": 1}`), 0o644)
	noLimit := filepath.Join(dir, "nolimit.json")
	os.WriteFile(noLimit, []byte(`{"base_url": "http://127.0.0.1:1", "flows": [{"name": "a", "steps": [{"url": "/x"}]}]}`), 0o644)
	good := writeShopScenario(t)

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run"}, "Usage: pelt run"},
		{[]string{"run", filepath.Join(dir, "missing.json")}, "no such file"},
		{[]string{"run", bad}, `unknown field "typo"`},
		{[]string{"run", noLimit}, "set a duration or an iteration count"},
		{[]string{"run", good, "-u", "0"}, "-u must be at least 1"},
		{[]string{"run", good, "-o", "xml"}, "-o must be"},
		{[]string{"run", good, good}, "one scenario file"},
		{[]string{"run", good, "-u", "500"}, "200 rows but 500 users"},
	} {
		var out, errOut bytes.Buffer
		if code := run(c.args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), c.want) {
			t.Errorf("run(%q) = %d, stderr %q; want 2 and %q", strings.Join(c.args, " "), code, errOut.String(), c.want)
		}
	}
}

// TestRunFlowThresholds checks exit codes for thresholds from the command
// line and the file, against the test shop.
func TestRunFlowThresholds(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	file := writeShopScenario(t)
	base := []string{"run", file, "-u", "2", "-i", "20", "-d", "0", "-base", srv.URL, "-seed", "3"}

	// Targets the shop meets: exit 0.
	var stdout, stderr bytes.Buffer
	args := append(base, "-threshold", "checks<=0", "-threshold", "checkout: p95<10s",
		"-threshold", "checkout/add to cart: errors<1%", "-threshold", "browse: aborted<=0")
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("passing thresholds exited %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Result: PASS (all 4 thresholds met)") {
		t.Errorf("missing PASS line:\n%s", stdout.String())
	}

	// A target it cannot meet: exit 3, JSON on stdout, one line on stderr.
	stdout.Reset()
	stderr.Reset()
	args = append(base, "-o", "json", "-threshold", "checkout/checkout: requests>=1000000")
	if code := run(args, &stdout, &stderr); code != 3 {
		t.Fatalf("failing threshold exited %d\n%s", code, stderr.String())
	}
	var rep struct {
		Passed     bool `json:"passed"`
		Thresholds []struct {
			Scope, Flow, Step string
			Pass              bool
		} `json:"thresholds"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if rep.Passed || len(rep.Thresholds) != 1 || rep.Thresholds[0].Scope != "step" ||
		rep.Thresholds[0].Flow != "checkout" || rep.Thresholds[0].Step != "checkout" {
		t.Errorf("JSON = %+v", rep)
	}
	if got := stderr.String(); got != "pelt: FAIL: 1 of 1 threshold missed\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestRunFlowFileThresholds(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(path, []byte(`{
	  "load": {"users": 1, "iterations": 5},
	  "thresholds": ["requests>=1000"],
	  "flows": [{"name": "browse", "thresholds": ["p95<10s"],
	    "steps": [{"name": "list", "url": "/products", "check": {"status": 200}, "thresholds": ["checks<=0"]}]}]
	}`), 0o644)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", path, "-base", srv.URL}, &stdout, &stderr); code != 3 {
		t.Fatalf("file thresholds exited %d, want 3\n%s%s", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"FAIL  requests >= 1000", "PASS  browse: p95 < 10s", "PASS  browse/list: checks <= 0",
		"Result: FAIL (1 of 3 thresholds missed)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("missing %q in:\n%s", want, stdout.String())
		}
	}

	// -no-thresholds drops the file's list; -threshold still counts.
	stdout.Reset()
	if code := run([]string{"run", path, "-base", srv.URL, "-no-thresholds", "-threshold", "browse/list: requests>=5"}, &stdout, &stderr); code != 0 {
		t.Fatalf("-no-thresholds exited %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if out := stdout.String(); strings.Contains(out, "requests >= 1000") || !strings.Contains(out, "Result: PASS (1 threshold met)") {
		t.Errorf("-no-thresholds output:\n%s", out)
	}
}

func TestRunFlowBadThresholds(t *testing.T) {
	good := writeShopScenario(t)
	for _, c := range []struct {
		expr, want string
	}{
		{"p95<1%", "not a percentage"},
		{"cart: p95<1s", `no flow or step named "cart"`},
		{"checkout/pay: p95<1s", `no flow or step named "checkout/pay"`},
		{"browse: errors<1%", "errors is measured for the whole run or a step, not for a flow"},
		{"wall_p95<1s", "wall_p95 is measured for a flow, not for the whole run"},
	} {
		var out, errOut bytes.Buffer
		code := run([]string{"run", good, "-base", "http://127.0.0.1:1", "-threshold", c.expr}, &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), c.want) {
			t.Errorf("-threshold %q = %d, stderr %q; want 2 and %q", c.expr, code, errOut.String(), c.want)
		}
	}
}
