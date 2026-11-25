package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
