package report

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/namnguyen3019/pelt/internal/series"
	"github.com/namnguyen3019/pelt/internal/stages"
)

// timelineRows is the most rows the text Timeline shows. Every second is
// in the JSON series and the -series CSV.
const timelineRows = 20

// timelineMinSeconds is the shortest run that gets a Timeline. Shorter runs
// print as they did before the series existed.
const timelineMinSeconds = 3

// timeline is the text Timeline: equal spans of the run, added up.
type timeline struct {
	width int // seconds per row
	spans []series.Range
	plan  *stages.Plan // nil without stages
}

// shortTail is the most of a last second the run may cover for the
// Timeline to fold that second into the row before it. A run is timed
// until its last request or iteration ends, so a 20s run lasts a little
// longer, such as 20.003s. Shown as its own row, those 3 ms would divide
// the requests and iterations that ended at the deadline by 0.003s and
// print rates thousands of times too high.
const shortTail = 0.5

// newTimeline adds up s into at most timelineRows spans. It needs the
// grouped latencies, so it runs before s.Release.
func newTimeline(s *series.Series, plan *stages.Plan) timeline {
	n := len(s.Rows)
	// The seconds the Timeline shows: a short tail joins the last row.
	shown := n
	if n > 1 && s.Rows[n-1].Seconds < shortTail {
		shown = n - 1
	}
	if shown < timelineMinSeconds {
		return timeline{}
	}
	width, bounds := series.Slices(shown, timelineRows)
	tl := timeline{width: width, plan: plan}
	for i, b := range bounds {
		to := b[1]
		if i == len(bounds)-1 {
			to = n // take in the tail's counts, its time and its latencies
		}
		sp := s.Range(b[0], to)
		sp.To = b[1] // but label the row by whole seconds
		tl.spans = append(tl.spans, sp)
	}
	return tl
}

// writeTimeline prints the Timeline block, or nothing for a short run.
func writeTimeline(b *strings.Builder, s *series.Series, tl timeline) {
	if len(tl.spans) == 0 {
		return
	}
	fmt.Fprintf(b, "\nTimeline (%s per row):\n", plural2(tl.width, "second"))
	cols := []string{"time"}
	if s.Staged {
		cols = append(cols, "target")
	}
	cols = append(cols, "req/s")
	if s.Flow {
		cols = append(cols, "iter/s")
	}
	cols = append(cols, "p95 ms", "errors")
	if s.Flow {
		cols = append(cols, "failed")
	} else {
		cols = append(cols, "5xx")
	}

	rows := [][]string{cols}
	for _, sp := range tl.spans {
		row := []string{fmt.Sprintf("%d-%ds", sp.From, sp.To)}
		if s.Staged {
			row = append(row, targetCell(tl.plan, meanTarget(s.Rows[sp.From:sp.To])))
		}
		row = append(row, groupInt(int64(math.Round(sp.RPS()))))
		if s.Flow {
			row = append(row, fmt.Sprintf("%.1f", sp.IPS()))
		}
		p95 := "-"
		if sp.Latency.Count > 0 {
			p95 = ms(sp.Latency.P95)
		}
		row = append(row, p95, groupInt(int64(sp.Errors)))
		if s.Flow {
			row = append(row, groupInt(int64(sp.CheckFails)))
		} else {
			row = append(row, groupInt(int64(sp.S5xx)))
		}
		rows = append(rows, row)
	}
	writeTable(b, rows)
}

// meanTarget is the average stage target over rows, which is what the
// span's req/s should be read against.
func meanTarget(rows []series.Row) float64 {
	sum := 0.0
	for _, r := range rows {
		sum += r.Target
	}
	return sum / float64(max(len(rows), 1))
}

// writeTable prints rows with the first column left-aligned and the rest
// right-aligned, two spaces apart, indented by two.
func writeTable(b *strings.Builder, rows [][]string) {
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	for _, r := range rows {
		b.WriteString(" ")
		for i, c := range r {
			if i == 0 {
				fmt.Fprintf(b, " %-*s", widths[i], c)
			} else {
				fmt.Fprintf(b, "  %*s", widths[i], c)
			}
		}
		b.WriteString("\n")
	}
}

func plural2(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// groupInt formats n with thousands separators: 1,402.
func groupInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// GroupInt is groupInt for the command line's live line.
func GroupInt(n int64) string { return groupInt(n) }

// --- JSON ---

// jsonSeriesRow is one second in the JSON series array. Keys are always
// present, with null for a value the second does not have; the flow-only
// keys appear only in flow mode.
type jsonSeriesRow struct {
	T          int      `json:"t"`
	Stage      *int     `json:"stage"`
	Target     *float64 `json:"target"`
	Active     int      `json:"active"`
	Requests   int      `json:"requests"`
	Responses  int      `json:"responses"`
	Errors     int      `json:"errors"`
	S2xx       int      `json:"s2xx"`
	S3xx       int      `json:"s3xx"`
	S4xx       int      `json:"s4xx"`
	S5xx       int      `json:"s5xx"`
	RPS        float64  `json:"rps"`
	P50        *float64 `json:"p50"`
	P95        *float64 `json:"p95"`
	P99        *float64 `json:"p99"`
	Max        *float64 `json:"max"`
	Iterations *int     `json:"iterations,omitempty"`
	Aborted    *int     `json:"aborted,omitempty"`
	CheckFails *int     `json:"check_fails,omitempty"`
}

func toJSONSeries(s *series.Series) []jsonSeriesRow {
	out := make([]jsonSeriesRow, 0, len(s.Rows))
	for _, r := range s.Rows {
		j := jsonSeriesRow{
			T: r.T, Active: r.Active,
			Requests: r.Requests, Responses: r.Responses, Errors: r.Errors,
			S2xx: r.S2xx, S3xx: r.S3xx, S4xx: r.S4xx, S5xx: r.S5xx,
			RPS: round2(r.RPS()),
		}
		if s.Staged {
			stage, target := r.Stage, round2(r.Target)
			j.Stage, j.Target = &stage, &target
		}
		if l := r.Latency; l.Count > 0 {
			j.P50, j.P95, j.P99, j.Max = msPtr(l.P50), msPtr(l.P95), msPtr(l.P99), msPtr(l.Max)
		}
		if s.Flow {
			it, ab, cf := r.Iterations, r.Aborted, r.CheckFails
			j.Iterations, j.Aborted, j.CheckFails = &it, &ab, &cf
		}
		out = append(out, j)
	}
	return out
}

func msPtr(d time.Duration) *float64 {
	v := round3(msf(d))
	return &v
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// --- CSV ---

// seriesColumns is the CSV header. The order and names are stable.
var (
	seriesColumns = []string{"t", "stage", "target", "active", "requests", "responses", "errors",
		"s2xx", "s3xx", "s4xx", "s5xx", "rps", "p50", "p95", "p99", "max"}
	flowSeriesColumns = []string{"iterations", "aborted", "check_fails"}
)

// writeSeriesCSV writes one row per second with a header. Empty cells
// stand for values the second does not have: stage and target without
// stages, latency in a second with no responses.
func writeSeriesCSV(w io.Writer, s *series.Series) error {
	cw := csv.NewWriter(w)
	header := seriesColumns
	if s.Flow {
		header = append(append([]string{}, seriesColumns...), flowSeriesColumns...)
	}
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, j := range toJSONSeries(s) {
		rec := []string{
			strconv.Itoa(j.T), intCell(j.Stage), floatCell(j.Target), strconv.Itoa(j.Active),
			strconv.Itoa(j.Requests), strconv.Itoa(j.Responses), strconv.Itoa(j.Errors),
			strconv.Itoa(j.S2xx), strconv.Itoa(j.S3xx), strconv.Itoa(j.S4xx), strconv.Itoa(j.S5xx),
			fmtFloat(j.RPS), floatCell(j.P50), floatCell(j.P95), floatCell(j.P99), floatCell(j.Max),
		}
		if s.Flow {
			rec = append(rec, intCell(j.Iterations), intCell(j.Aborted), intCell(j.CheckFails))
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func intCell(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func floatCell(p *float64) string {
	if p == nil {
		return ""
	}
	return fmtFloat(*p)
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
