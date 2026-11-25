package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/namnguyen3019/pelt/internal/series"
	"github.com/namnguyen3019/pelt/internal/stages"
)

// stagedResult is seriesResult with stages 2s:4,0s:2,1s:2 behind it, plus
// half a second of requests finishing after the stages end.
func stagedResult(t *testing.T) *Report {
	t.Helper()
	cfg, res := sampleResult()
	plan, err := stages.Parse("2s:4,0s:2,1s:2")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Stages, cfg.Duration, cfg.Requests, cfg.Concurrency = &plan, 0, 0, 4
	rep, _ := seriesResult()
	res.Elapsed, res.Finish = rep.res.Elapsed, []uint32{100, 1200, 1300, 3400}
	res.Series = rep.res.Series
	res.Series.Staged = true
	res.Series.Levels = []series.Level{
		{Active: 1, Target: 1, Stage: 1}, {Active: 3, Target: 3, Stage: 1},
		{Active: 2, Target: 2, Stage: 3}, {Active: 0, Target: 2, Stage: 3},
	}
	return New(cfg, res)
}

func TestStagesText(t *testing.T) {
	var buf bytes.Buffer
	if err := stagedResult(t).WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Load:         concurrency stages 2s:4,0s:2,1s:2 over 3s",
		"Stages:\n  stage   time  target  requests  req/s  p95 ms  errors\n",
		"  1      0s-2s   0 → 4         4      2   4.000       1\n",
		"  2      2s-2s   4 → 2         -      -       -       -\n",
		"  3      2s-3s   2 → 2         1      1   2.000       0\n",
		"  time  target  req/s",
		"  0-1s       1      1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report lacks %q:\n%s", want, out)
		}
	}
}

func TestStagesJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := stagedResult(t).WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Spec   string           `json:"stages_configured"`
		Stages []map[string]any `json:"stages"`
		Series []map[string]any `json:"series"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Spec != "2s:4,0s:2,1s:2" || len(out.Stages) != 3 {
		t.Fatalf("stages = %q %v", out.Spec, out.Stages)
	}
	s1, s2 := out.Stages[0], out.Stages[1]
	if s1["requests"] != 4.0 || s1["from"] != 0.0 || s1["target"] != 4.0 || s1["end_s"] != 2.0 || s1["p95_ms"] == nil {
		t.Errorf("stage 1 = %v", s1)
	}
	if s2["requests"] != 0.0 || s2["p95_ms"] != nil || s2["start_s"] != 2.0 || s2["end_s"] != 2.0 {
		t.Errorf("the 0s stage = %v", s2)
	}
	if r := out.Series[1]; r["stage"] != 1.0 || r["target"] != 3.0 {
		t.Errorf("series row 1 = %v, want stage 1, target 3", r)
	}
}

func TestNoStagesNoStagesKeys(t *testing.T) {
	rep, buf := seriesResult()
	if err := rep.WriteJSON(buf); err != nil {
		t.Fatal(err)
	}
	if s := buf.String(); strings.Contains(s, `"stages`) {
		t.Errorf("a run without stages should have no stages keys:\n%s", s)
	}
	buf.Reset()
	rep.WriteText(buf)
	if strings.Contains(buf.String(), "Stages:") {
		t.Error("a run without stages should have no Stages block")
	}
}
