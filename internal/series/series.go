// Package series records a run second by second: how many requests
// finished in each second, how they ended, and their latency percentiles.
//
// It has three parts:
//
//   - Counters, shared by every worker while the run is going. Each worker
//     adds to the row for the second its request finished in, with atomic
//     adds and no locks.
//   - A Sampler that wakes at the end of every second to note values that
//     are a level rather than a count, such as requests in flight.
//   - Build, run once after the run, which groups the latency samples by
//     the second they finished in and turns everything into Rows.
//
// A request counts in the second it finished, so the rows add up to the
// run's totals and a slow request shows in the second it hurt.
package series

import (
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/namnguyen3019/pelt/internal/stats"
)

// Since returns the time from start in whole milliseconds, the form a
// finish time is kept in. A uint32 covers 49 days.
func Since(start time.Time) uint32 {
	ms := time.Since(start) / time.Millisecond
	if ms < 0 {
		return 0
	}
	if ms > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(ms)
}

// Seconds per chunk, and the most chunks a run can have. 4,096 chunks of
// 1,024 seconds is about 48 days, which matches what a uint32 of
// milliseconds can hold. Only chunks a run reaches are allocated.
const (
	chunkSeconds = 1024
	maxChunks    = 4096
)

// counterRow is one second's counts. Every field is updated with atomic
// adds, so many workers can share a row.
type counterRow struct {
	requests, responses, errors     atomic.Int64
	s2xx, s3xx, s4xx, s5xx          atomic.Int64
	iterations, aborted, checkFails atomic.Int64
}

type chunk [chunkSeconds]counterRow

// Counters holds per-second counts for a run. The zero value is not ready;
// use NewCounters. All methods are safe for concurrent use.
type Counters struct {
	start  time.Time
	chunks [maxChunks]atomic.Pointer[chunk]
	last   atomic.Int64 // highest second touched, -1 before any
}

// NewCounters starts the clock for a run.
func NewCounters(start time.Time) *Counters {
	c := &Counters{start: start}
	c.last.Store(-1)
	return c
}

// Start is the time the run's seconds are counted from.
func (c *Counters) Start() time.Time { return c.start }

// Now is the time since the start in milliseconds, for a finish time.
func (c *Counters) Now() uint32 { return Since(c.start) }

// row returns the counters for second sec, allocating its chunk on first
// use. Two workers racing to allocate the same chunk both try a
// compare-and-swap and the loser uses the winner's chunk.
func (c *Counters) row(sec int64) *counterRow {
	ci := sec / chunkSeconds
	if ci >= maxChunks {
		ci, sec = maxChunks-1, maxChunks*chunkSeconds-1 // clamp absurdly long runs
	}
	ch := c.chunks[ci].Load()
	if ch == nil {
		fresh := new(chunk)
		if c.chunks[ci].CompareAndSwap(nil, fresh) {
			ch = fresh
		} else {
			ch = c.chunks[ci].Load()
		}
	}
	for {
		last := c.last.Load()
		if sec <= last || c.last.CompareAndSwap(last, sec) {
			break
		}
	}
	return &ch[sec%chunkSeconds]
}

func second(ms uint32) int64 { return int64(ms / 1000) }

// Response counts a request that got an HTTP response with status code,
// finishing at ms.
func (c *Counters) Response(ms uint32, code int) {
	r := c.row(second(ms))
	r.requests.Add(1)
	r.responses.Add(1)
	switch code / 100 {
	case 2:
		r.s2xx.Add(1)
	case 3:
		r.s3xx.Add(1)
	case 4:
		r.s4xx.Add(1)
	case 5:
		r.s5xx.Add(1)
	}
}

// Error counts a request that failed without a response, at ms.
func (c *Counters) Error(ms uint32) {
	r := c.row(second(ms))
	r.requests.Add(1)
	r.errors.Add(1)
}

// Iteration counts a flow iteration that ended at ms; aborted marks one
// stopped by a failed check, capture or transport error.
func (c *Counters) Iteration(ms uint32, aborted bool) {
	r := c.row(second(ms))
	r.iterations.Add(1)
	if aborted {
		r.aborted.Add(1)
	}
}

// CheckFail counts a failed status check or capture at ms.
func (c *Counters) CheckFail(ms uint32) {
	c.row(second(ms)).checkFails.Add(1)
}

// Count is one second's counts, read back as plain numbers.
type Count struct {
	Requests, Responses, Errors int
	S2xx, S3xx, S4xx, S5xx      int
	Iterations, Aborted         int
	CheckFails                  int
}

// Get reads second sec. Seconds no request has reached read as zero.
func (c *Counters) Get(sec int) Count {
	if sec < 0 || sec/chunkSeconds >= maxChunks {
		return Count{}
	}
	ch := c.chunks[sec/chunkSeconds].Load()
	if ch == nil {
		return Count{}
	}
	r := &ch[sec%chunkSeconds]
	return Count{
		Requests: int(r.requests.Load()), Responses: int(r.responses.Load()), Errors: int(r.errors.Load()),
		S2xx: int(r.s2xx.Load()), S3xx: int(r.s3xx.Load()), S4xx: int(r.s4xx.Load()), S5xx: int(r.s5xx.Load()),
		Iterations: int(r.iterations.Load()), Aborted: int(r.aborted.Load()), CheckFails: int(r.checkFails.Load()),
	}
}

// Snapshot reads the first n seconds, for after the run.
func (c *Counters) Snapshot(n int) []Count {
	out := make([]Count, n)
	for i := range out {
		out[i] = c.Get(i)
	}
	return out
}

// Last is the highest second any count has landed in, or -1.
func (c *Counters) Last() int { return int(c.last.Load()) }

// Level is what the sampler notes at the end of a second.
type Level struct {
	// Active is requests in flight (single-URL) or users running (flow).
	Active int
	// Target is what the load stages asked for at that moment, and Stage
	// the stage in effect (1-based). Both are zero without stages.
	Target float64
	Stage  int
}

// Sampler calls read at the end of each second of the run and keeps the
// results. Start it with StartSampler and call Stop once the run is over.
type Sampler struct {
	read   func(at time.Duration) Level
	start  time.Time
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	levels []Level // written only by the sampler goroutine until done
}

// StartSampler starts sampling on the run's clock. read gets the time
// since start that the sample stands for.
func StartSampler(start time.Time, read func(at time.Duration) Level) *Sampler {
	s := &Sampler{read: read, start: start, stop: make(chan struct{}), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *Sampler) loop() {
	defer close(s.done)
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for sec := 1; ; sec++ {
		// Wake on an absolute schedule so the samples do not drift.
		t.Reset(time.Until(s.start.Add(time.Duration(sec) * time.Second)))
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.levels = append(s.levels, s.read(time.Duration(sec)*time.Second))
		}
	}
}

// Stop ends sampling and returns one Level per second, the last one taken
// at the moment of the call for the final, partial second.
func (s *Sampler) Stop() []Level {
	s.once.Do(func() {
		close(s.stop)
		<-s.done
		s.levels = append(s.levels, s.read(time.Since(s.start)))
	})
	return s.levels
}

// Raw is what a run hands to Build: its length, the per-second counts and
// the sampled levels.
type Raw struct {
	Elapsed time.Duration
	Counts  []Count
	Levels  []Level
	// Staged says load stages were set, so Target and Stage mean something.
	Staged bool
	// Flow says the counts include iterations and failed checks.
	Flow bool
}

// Collect reads a finished run into a Raw. It makes one row per second the
// run lasted, plus any second a late count landed in.
func Collect(c *Counters, s *Sampler, elapsed time.Duration) Raw {
	levels := s.Stop()
	n := Len(elapsed)
	n = max(n, c.Last()+1)
	return Raw{Elapsed: elapsed, Counts: c.Snapshot(n), Levels: levels}
}

// Len is the number of rows for a run of length elapsed: one per second
// started, and at least one.
func Len(elapsed time.Duration) int {
	n := int((elapsed + time.Second - 1) / time.Second)
	return max(n, 1)
}

// Samples is a set of latencies with the finish time of each, in
// milliseconds since the start. The two slices are parallel.
type Samples struct {
	Latencies []time.Duration
	Finish    []uint32
}

// Row is one second of a run.
type Row struct {
	T      int     // second since the start: 0, 1, 2 ...
	Stage  int     // 1-based stage, 0 without stages
	Target float64 // what the stages asked for at the end of the second
	Active int     // requests in flight or users running at the end of the second
	Count
	// Seconds is how much of this second the run covered: 1, except for
	// the last row of a run that did not end on a whole second.
	Seconds float64
	// Latency of the responses that finished in this second. Count is 0
	// when there were none.
	Latency RangeLatency
}

// RPS is requests finished per second of run time in this row.
func (r Row) RPS() float64 { return perSec(r.Requests, r.Seconds) }

// IPS is iterations finished per second of run time in this row.
func (r Row) IPS() float64 { return perSec(r.Iterations, r.Seconds) }

func perSec(n int, secs float64) float64 {
	if secs <= 0 {
		return 0
	}
	return float64(n) / secs
}

// RangeLatency is the latency of a set of responses.
type RangeLatency struct {
	Count              int
	P50, P95, P99, Max time.Duration
}

// Series is a run, second by second.
type Series struct {
	Rows   []Row
	Staged bool
	Flow   bool

	// grouped holds every latency, grouped by finish second: second t's
	// samples are grouped[offset[t]:offset[t+1]], each group sorted.
	grouped []time.Duration
	offset  []int
}

// Build turns a run's raw counts and latency samples into rows. It copies
// the latencies, so the caller can sort its own slices afterwards; the
// copy is held until Release.
func Build(raw Raw, sets ...Samples) *Series {
	n := len(raw.Counts)
	if n == 0 {
		return &Series{Staged: raw.Staged, Flow: raw.Flow}
	}
	s := &Series{Staged: raw.Staged, Flow: raw.Flow, offset: make([]int, n+1)}

	// Counting pass: how many samples landed in each second.
	total := 0
	for _, set := range sets {
		for _, f := range set.Finish {
			s.offset[clampSec(f, n)+1]++
			total++
		}
	}
	for t := 1; t <= n; t++ {
		s.offset[t] += s.offset[t-1]
	}
	// Fill pass, then sort each second's group.
	s.grouped = make([]time.Duration, total)
	next := slices.Clone(s.offset[:n])
	for _, set := range sets {
		for i, f := range set.Finish {
			t := clampSec(f, n)
			s.grouped[next[t]] = set.Latencies[i]
			next[t]++
		}
	}

	s.Rows = make([]Row, n)
	for t := range s.Rows {
		g := s.grouped[s.offset[t]:s.offset[t+1]]
		slices.Sort(g)
		row := Row{T: t, Count: raw.Counts[t], Seconds: covered(t, raw.Elapsed), Latency: latencyOf(g)}
		if t < len(raw.Levels) {
			lv := raw.Levels[t]
			row.Active = lv.Active
			if raw.Staged {
				row.Target, row.Stage = lv.Target, lv.Stage
			}
		}
		s.Rows[t] = row
	}
	return s
}

func clampSec(ms uint32, n int) int {
	return min(int(ms/1000), n-1)
}

// covered is how much of second t a run of length elapsed covered.
func covered(t int, elapsed time.Duration) float64 {
	left := elapsed.Seconds() - float64(t)
	return math.Max(0, math.Min(1, left))
}

func latencyOf(sorted []time.Duration) RangeLatency {
	if len(sorted) == 0 {
		return RangeLatency{}
	}
	return RangeLatency{
		Count: len(sorted),
		P50:   stats.PercentileOf(sorted, 50),
		P95:   stats.PercentileOf(sorted, 95),
		P99:   stats.PercentileOf(sorted, 99),
		Max:   sorted[len(sorted)-1],
	}
}

// Range is a span of whole seconds [From, To) added up.
type Range struct {
	From, To int
	Count
	Seconds float64
	Latency RangeLatency
}

// RPS is requests per second over the range.
func (r Range) RPS() float64 { return perSec(r.Requests, r.Seconds) }

// IPS is iterations per second over the range.
func (r Range) IPS() float64 { return perSec(r.Iterations, r.Seconds) }

// Range adds up seconds [from, to). Latency percentiles are over every
// response in the range, not an average of the seconds. It must be called
// before Release.
func (s *Series) Range(from, to int) Range {
	from = max(from, 0)
	to = min(to, len(s.Rows))
	r := Range{From: from, To: to}
	if from >= to {
		return r
	}
	for _, row := range s.Rows[from:to] {
		r.Count.add(row.Count)
		r.Seconds += row.Seconds
	}
	if s.grouped != nil {
		g := slices.Clone(s.grouped[s.offset[from]:s.offset[to]])
		slices.Sort(g)
		r.Latency = latencyOf(g)
	}
	return r
}

// Release drops the grouped latencies once every Range is computed.
func (s *Series) Release() { s.grouped, s.offset = nil, nil }

func (c *Count) add(o Count) {
	c.Requests += o.Requests
	c.Responses += o.Responses
	c.Errors += o.Errors
	c.S2xx += o.S2xx
	c.S3xx += o.S3xx
	c.S4xx += o.S4xx
	c.S5xx += o.S5xx
	c.Iterations += o.Iterations
	c.Aborted += o.Aborted
	c.CheckFails += o.CheckFails
}

// Slices splits n seconds into at most maxRows equal spans of whole
// seconds, for the text timeline. It returns the span length and the
// [from, to) bounds of each.
func Slices(n, maxRows int) (width int, spans [][2]int) {
	if n <= 0 || maxRows <= 0 {
		return 0, nil
	}
	width = (n + maxRows - 1) / maxRows
	for from := 0; from < n; from += width {
		spans = append(spans, [2]int{from, min(from+width, n)})
	}
	return width, spans
}
