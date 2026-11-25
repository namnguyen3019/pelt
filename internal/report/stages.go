package report

import (
	"fmt"
	"math"
	"strings"

	"github.com/namnguyen3019/pelt/internal/series"
	"github.com/namnguyen3019/pelt/internal/stages"
)

// stageResult is one stage of a run, added up over the seconds that belong
// to it. A 0s stage owns no seconds.
type stageResult struct {
	num    int // 1-based
	stage  stages.Stage
	from   float64
	startS float64
	endS   float64
	r      series.Range
}

// newStageResults adds up the series by stage. A second belongs to the
// stage in effect at its midpoint, as the series' stage column says, and
// the seconds after the stages end (requests still finishing) belong to
// the last stage. It needs the grouped latencies, so it runs before
// s.Release.
func newStageResults(s *series.Series, plan *stages.Plan) []stageResult {
	if plan == nil {
		return nil
	}
	out := make([]stageResult, len(plan.Stages))
	for i, st := range plan.Stages {
		start, end := plan.Bounds(i)
		out[i] = stageResult{num: i + 1, stage: st, from: plan.From(i), startS: start.Seconds(), endS: end.Seconds()}
		from, to := -1, -1
		for t, row := range s.Rows {
			if row.Stage == i+1 {
				if from < 0 {
					from = t
				}
				to = t + 1
			}
		}
		if from >= 0 {
			out[i].r = s.Range(from, to)
		}
	}
	return out
}

// writeStages prints the Stages block: one row per stage. Flow mode adds
// iterations per second.
func writeStages(b *strings.Builder, plan *stages.Plan, res []stageResult, flow bool) {
	if len(res) == 0 {
		return
	}
	named := false
	for _, sr := range res {
		named = named || sr.stage.Name != ""
	}
	b.WriteString("\nStages:\n")
	head := []string{"stage"}
	if named {
		head = append(head, "name")
	}
	head = append(head, "time", "target", "requests", "req/s")
	if flow {
		head = append(head, "iter/s")
	}
	head = append(head, "p95 ms", "errors")
	rows := [][]string{head}
	for _, sr := range res {
		row := []string{fmt.Sprint(sr.num)}
		if named {
			row = append(row, sr.stage.Name)
		}
		row = append(row,
			fmt.Sprintf("%s-%s", secs(sr.startS), secs(sr.endS)),
			fmt.Sprintf("%s → %s", plan.FormatTarget(sr.from), plan.FormatTarget(sr.stage.Target)))
		switch {
		case sr.r.Seconds == 0:
			for range len(head) - len(row) {
				row = append(row, "-")
			}
		default:
			p95 := "-"
			if sr.r.Latency.Count > 0 {
				p95 = ms(sr.r.Latency.P95)
			}
			row = append(row, groupInt(int64(sr.r.Requests)), groupInt(int64(math.Round(sr.r.RPS()))))
			if flow {
				row = append(row, fmt.Sprintf("%.1f", sr.r.IPS()))
			}
			row = append(row, p95, groupInt(int64(sr.r.Errors)))
		}
		rows = append(rows, row)
	}
	writeTable(b, rows)
}

// secs writes a stage boundary in seconds: 30s, 150s, 1.5s.
func secs(f float64) string { return fmtFloat(round3(f)) + "s" }

// targetCell writes a sampled target for the Timeline: a whole number for
// a level, one decimal and /s for a rate.
func targetCell(plan *stages.Plan, t float64) string {
	if plan == nil || plan.Kind == stages.Level {
		return fmt.Sprint(int64(math.Round(t)))
	}
	return fmtFloat(math.Round(t*10)/10) + "/s"
}

// jsonStage is one stage in the JSON stages array.
type jsonStage struct {
	Stage     int      `json:"stage"`
	Name      string   `json:"name"`
	From      float64  `json:"from"`
	Target    float64  `json:"target"`
	StartS    float64  `json:"start_s"`
	EndS      float64  `json:"end_s"`
	Requests  int      `json:"requests"`
	Responses int      `json:"responses"`
	Errors    int      `json:"errors"`
	RPS       float64  `json:"rps"`
	P50       *float64 `json:"p50_ms"`
	P95       *float64 `json:"p95_ms"`
	P99       *float64 `json:"p99_ms"`
	// Flow mode only.
	Iterations *int     `json:"iterations,omitempty"`
	IPS        *float64 `json:"iterations_per_sec,omitempty"`
	CheckFails *int     `json:"failed_checks,omitempty"`
}

func toJSONStages(res []stageResult, flow bool) []jsonStage {
	if len(res) == 0 {
		return nil
	}
	out := make([]jsonStage, 0, len(res))
	for _, sr := range res {
		j := jsonStage{
			Stage: sr.num, Name: sr.stage.Name, From: sr.from, Target: sr.stage.Target,
			StartS: round3(sr.startS), EndS: round3(sr.endS),
			Requests: sr.r.Requests, Responses: sr.r.Responses, Errors: sr.r.Errors,
			RPS: round2(sr.r.RPS()),
		}
		if l := sr.r.Latency; l.Count > 0 {
			j.P50, j.P95, j.P99 = msPtr(l.P50), msPtr(l.P95), msPtr(l.P99)
		}
		if flow {
			it, ips, cf := sr.r.Iterations, round2(sr.r.IPS()), sr.r.CheckFails
			j.Iterations, j.IPS, j.CheckFails = &it, &ips, &cf
		}
		out = append(out, j)
	}
	return out
}
