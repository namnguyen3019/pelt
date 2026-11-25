package report

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/namnguyen3019/pelt/internal/series"
)

// seriesResult is sampleResult with a four-second series behind it.
func seriesResult() (*Report, *bytes.Buffer) {
	cfg, res := sampleResult()
	res.Elapsed = 3500 * time.Millisecond
	res.Finish = []uint32{100, 1200, 1300, 3400} // beside 4, 1, 3, 2 ms
	res.Series = series.Raw{
		Elapsed: res.Elapsed,
		Counts: []series.Count{
			{Requests: 1, Responses: 1, S2xx: 1},
			{Requests: 3, Responses: 2, Errors: 1, S2xx: 1, S5xx: 1},
			{},
			{Requests: 1, Responses: 1, S2xx: 1},
		},
		Levels: []series.Level{{Active: 2}, {Active: 2}, {Active: 1}, {Active: 0}},
	}
	return New(cfg, res), &bytes.Buffer{}
}

func TestSeriesJSON(t *testing.T) {
	rep, buf := seriesResult()
	if err := rep.WriteJSON(buf); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Series []map[string]any `json:"series"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Series) != 4 {
		t.Fatalf("series has %d rows, want 4", len(out.Series))
	}
	// Keys are stable: the same set in every row, flow keys left out.
	want := []string{"active", "errors", "max", "p50", "p95", "p99", "requests", "responses",
		"rps", "s2xx", "s3xx", "s4xx", "s5xx", "stage", "t", "target"}
	for i, row := range out.Series {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, want) {
			t.Errorf("row %d keys = %v, want %v", i, keys, want)
		}
	}
	r1, r2, r3 := out.Series[1], out.Series[2], out.Series[3]
	if r1["requests"] != 3.0 || r1["p50"] != 1.0 || r1["max"] != 3.0 || r1["s5xx"] != 1.0 {
		t.Errorf("row 1 = %v", r1)
	}
	if r2["p95"] != nil || r2["stage"] != nil || r2["target"] != nil {
		t.Errorf("row 2 should have null latency, stage and target: %v", r2)
	}
	if r3["rps"] != 2.0 { // 1 request in the half second the run covered
		t.Errorf("last row rps = %v, want 2", r3["rps"])
	}
}

func TestSeriesCSV(t *testing.T) {
	rep, buf := seriesResult()
	if err := rep.WriteSeriesCSV(buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if lines[0] != "t,stage,target,active,requests,responses,errors,s2xx,s3xx,s4xx,s5xx,rps,p50,p95,p99,max" {
		t.Errorf("header = %q", lines[0])
	}
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want header + 4", len(lines))
	}
	if lines[2] != "1,,,2,3,2,1,1,0,0,1,3,1,3,3,3" {
		t.Errorf("row 1 = %q", lines[2])
	}
	if lines[3] != "2,,,1,0,0,0,0,0,0,0,0,,,," {
		t.Errorf("empty second = %q, want empty latency cells", lines[3])
	}
}

func TestTimelineText(t *testing.T) {
	rep, buf := seriesResult()
	rep.WriteText(buf)
	out := buf.String()
	for _, want := range []string{
		"Timeline (1 second per row):",
		"time  req/s  p95 ms  errors  5xx",
		"1-2s      3   3.000       1    1",
		"2-3s      0       -       0    0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text missing %q\n%s", want, out)
		}
	}

	// A run shorter than three seconds prints no timeline.
	var short bytes.Buffer
	New(sampleResult()).WriteText(&short)
	if strings.Contains(short.String(), "Timeline") {
		t.Error("a short run printed a Timeline")
	}
}

func TestGroupInt(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1402: "1,402", 1234567: "1,234,567", -1500: "-1,500"} {
		if got := groupInt(n); got != want {
			t.Errorf("groupInt(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFlowSeries(t *testing.T) {
	res := sampleFlowResult(t)
	res.Elapsed = 3 * time.Second
	for i := range res.Steps {
		s := &res.Steps[i]
		s.Finish = make([]uint32, len(s.Latencies))
		for j := range s.Finish {
			s.Finish[j] = uint32(1000 * (j % 3))
		}
	}
	res.Series = series.Raw{Elapsed: res.Elapsed, Flow: true, Counts: []series.Count{
		{Requests: 4, Responses: 4, S2xx: 4, Iterations: 2},
		{Requests: 3, Responses: 2, Errors: 1, S2xx: 1, S4xx: 1, Iterations: 1, Aborted: 1, CheckFails: 1},
		{Requests: 2, Responses: 2, S2xx: 2, Iterations: 1},
	}}
	rep := NewFlow(res)

	var js bytes.Buffer
	rep.WriteJSON(&js)
	var out struct {
		Series []map[string]any `json:"series"`
	}
	json.Unmarshal(js.Bytes(), &out)
	if len(out.Series) != 3 || out.Series[1]["iterations"] != 1.0 || out.Series[1]["check_fails"] != 1.0 || out.Series[0]["aborted"] != 0.0 {
		t.Errorf("flow series = %v", out.Series)
	}

	var csv bytes.Buffer
	rep.WriteSeriesCSV(&csv)
	if !strings.HasPrefix(csv.String(), "t,stage,target,active,requests,responses,errors,s2xx,s3xx,s4xx,s5xx,rps,p50,p95,p99,max,iterations,aborted,check_fails\n") {
		t.Errorf("flow CSV header: %q", strings.SplitN(csv.String(), "\n", 2)[0])
	}

	var text bytes.Buffer
	rep.WriteText(&text)
	if !strings.Contains(text.String(), "time  req/s  iter/s  p95 ms  errors  failed") {
		t.Errorf("flow timeline header missing\n%s", text.String())
	}
}

// tailResult is a run timed a few ms past a whole second, as a run is
// when it waits for its last requests: secs whole seconds of 100 requests
// each, then a tail of tailMS in which another 100 finished. The tail
// requests are the four latency samples, so the last row's p95 covers them.
func tailResult(secs int, tailMS int) (*Report, *bytes.Buffer) {
	cfg, res := sampleResult()
	res.Elapsed = time.Duration(secs)*time.Second + time.Duration(tailMS)*time.Millisecond
	at := uint32(secs*1000 + tailMS/2)
	res.Finish = []uint32{at, at, at, at}
	counts := make([]series.Count, secs+1)
	for i := range counts {
		counts[i] = series.Count{Requests: 100, Responses: 100, S2xx: 100}
	}
	res.Series = series.Raw{Elapsed: res.Elapsed, Counts: counts}
	return New(cfg, res), &bytes.Buffer{}
}

// timelineLines returns the Timeline's data rows, without its header.
func timelineLines(t *testing.T, out string) []string {
	t.Helper()
	_, after, ok := strings.Cut(out, "Timeline (")
	if !ok {
		t.Fatalf("no Timeline\n%s", out)
	}
	block, _, _ := strings.Cut(after, "\n\n")
	lines := strings.Split(block, "\n")
	return lines[2:] // skip "N second(s) per row):" and the column header
}

func TestTimelineFoldsShortTail(t *testing.T) {
	// 3.004s: the 4 ms tail joins the 2-3s row instead of being a row of
	// its own at 100 / 0.004 = 25,000 req/s.
	rep, buf := tailResult(3, 4)
	rep.WriteText(buf)
	out := buf.String()
	rows := timelineLines(t, out)
	if len(rows) != 3 {
		t.Fatalf("Timeline has %d rows, want 3:\n%s", len(rows), out)
	}
	last := strings.Fields(rows[2])
	// 200 requests in 1.004s is 199/s; p95 comes from the tail's samples.
	if last[0] != "2-3s" || last[1] != "199" || last[2] != "4.000" {
		t.Errorf("last row = %q, want 2-3s at 199 req/s with p95 4.000", rows[2])
	}
	if strings.Contains(out, "25,000") || strings.Contains(out, "3-4s") {
		t.Errorf("tail shown as its own row:\n%s", out)
	}

	// The JSON series and CSV still keep one row per second.
	var js bytes.Buffer
	rep.WriteJSON(&js)
	var j struct {
		Series []map[string]any `json:"series"`
	}
	json.Unmarshal(js.Bytes(), &j)
	if len(j.Series) != 4 {
		t.Errorf("JSON series has %d rows, want 4", len(j.Series))
	}
}

func TestTimelineTailRows(t *testing.T) {
	// A 20.003s run fits 20 rows of 1 second; without the fold its 21
	// seconds would make 2 seconds per row.
	rep, buf := tailResult(20, 3)
	rep.WriteText(buf)
	if !strings.Contains(buf.String(), "Timeline (1 second per row):") {
		t.Errorf("want 1 second per row:\n%s", buf.String())
	}
	if rows := timelineLines(t, buf.String()); len(rows) != 20 {
		t.Errorf("Timeline has %d rows, want 20", len(rows))
	}

	// A tail of half a second or more is a row of its own.
	rep, buf = tailResult(3, 500)
	rep.WriteText(buf)
	rows := timelineLines(t, buf.String())
	if len(rows) != 4 || !strings.HasPrefix(strings.TrimSpace(rows[3]), "3-4s") {
		t.Errorf("want a 3-4s row for a 500 ms tail:\n%s", buf.String())
	}

	// 2.004s is a two-second run: still too short for a Timeline.
	rep, buf = tailResult(2, 4)
	rep.WriteText(buf)
	if strings.Contains(buf.String(), "Timeline") {
		t.Errorf("a 2.004s run printed a Timeline:\n%s", buf.String())
	}
}
