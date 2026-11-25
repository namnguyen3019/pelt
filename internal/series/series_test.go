package series

import (
	"sync"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestCountersConcurrent(t *testing.T) {
	c := NewCounters(time.Now())
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				// Spread over seconds 0-2 and across a chunk boundary.
				at := uint32((i%3)*1000 + w)
				c.Response(at, 200+(i%4)*100)
				if i%10 == 0 {
					c.Error(at)
				}
			}
			c.Response(uint32(chunkSeconds*1000+5), 200) // second 1024, the next chunk
		}()
	}
	wg.Wait()

	var total Count
	for sec := range c.Last() + 1 {
		total.add(c.Get(sec))
	}
	if total.Responses != 8*1001 || total.Errors != 8*100 || total.Requests != 8*1101 {
		t.Errorf("totals = %+v, want 8008 responses, 800 errors, 8808 requests", total)
	}
	if total.S2xx != 8*250+8 || total.S3xx != 8*250 || total.S4xx != 8*250 || total.S5xx != 8*250 {
		t.Errorf("status classes = %d/%d/%d/%d", total.S2xx, total.S3xx, total.S4xx, total.S5xx)
	}
	if c.Last() != chunkSeconds {
		t.Errorf("Last = %d, want %d", c.Last(), chunkSeconds)
	}
	if got := c.Get(chunkSeconds).Responses; got != 8 {
		t.Errorf("second %d has %d responses, want 8", chunkSeconds, got)
	}
	if got := c.Get(500); got != (Count{}) {
		t.Errorf("untouched second = %+v, want zero", got)
	}
}

func TestFinishedRequestCountsInItsSecond(t *testing.T) {
	c := NewCounters(time.Now())
	c.Response(999, 200)  // 0.999 s: second 0
	c.Response(1000, 200) // 1.000 s: second 1
	c.Response(1999, 503) // 1.999 s: second 1
	c.Error(2000)         // 2.000 s: second 2
	c.Iteration(1500, true)
	c.CheckFail(1500)

	raw := Raw{Elapsed: ms(2500), Counts: c.Snapshot(Len(ms(2500))), Flow: true}
	s := Build(raw, Samples{
		Latencies: []time.Duration{ms(5), ms(7), ms(9)},
		Finish:    []uint32{999, 1000, 1999},
	})
	if len(s.Rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(s.Rows))
	}
	r0, r1, r2 := s.Rows[0], s.Rows[1], s.Rows[2]
	if r0.Requests != 1 || r1.Requests != 2 || r2.Requests != 1 {
		t.Errorf("requests by second = %d/%d/%d, want 1/2/1", r0.Requests, r1.Requests, r2.Requests)
	}
	if r1.S5xx != 1 || r2.Errors != 1 || r1.Iterations != 1 || r1.Aborted != 1 || r1.CheckFails != 1 {
		t.Errorf("row 1 = %+v, row 2 = %+v", r1, r2)
	}
	if r0.Latency.P50 != ms(5) || r1.Latency.Count != 2 || r1.Latency.P50 != ms(7) || r1.Latency.Max != ms(9) {
		t.Errorf("latency by second: %+v %+v", r0.Latency, r1.Latency)
	}
	if r2.Latency.Count != 0 {
		t.Errorf("second with only an error has latency %+v, want none", r2.Latency)
	}
	if r2.Seconds != 0.5 || r2.RPS() != 2 {
		t.Errorf("last partial row covers %v s at %v req/s, want 0.5 s and 2", r2.Seconds, r2.RPS())
	}
}

func TestRowsAddUpToTotals(t *testing.T) {
	c := NewCounters(time.Now())
	var lat []time.Duration
	var fin []uint32
	for i := range 5000 {
		at := uint32(i * 7 % 9000)
		c.Response(at, 200)
		lat = append(lat, ms(1+i%50))
		fin = append(fin, at)
	}
	raw := Collect(c, StartSampler(c.Start(), func(time.Duration) Level { return Level{} }), 9*time.Second)
	s := Build(raw, Samples{Latencies: lat, Finish: fin})
	var all Range = s.Range(0, len(s.Rows))
	if all.Requests != 5000 || all.Latency.Count != 5000 {
		t.Errorf("rows add up to %d requests and %d latencies, want 5000", all.Requests, all.Latency.Count)
	}
	if all.Latency.Max != ms(50) || all.Latency.P50 != ms(25) {
		t.Errorf("range latency = %+v", all.Latency)
	}
	// Build copies: the caller's slice keeps its order.
	if lat[0] != ms(1) || lat[1] != ms(2) {
		t.Error("Build changed the caller's latencies")
	}
	s.Release()
	if r := s.Range(0, 2); r.Latency.Count != 0 || r.Requests == 0 {
		t.Errorf("after Release, Range = %+v: want counts but no latency", r)
	}
}

func TestEmptyRaw(t *testing.T) {
	s := Build(Raw{}, Samples{Latencies: []time.Duration{ms(1)}})
	if len(s.Rows) != 0 {
		t.Errorf("rows = %d, want 0", len(s.Rows))
	}
	if r := s.Range(0, 5); r.Requests != 0 {
		t.Errorf("Range on empty series = %+v", r)
	}
}

func TestLenAndCovered(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    int
	}{{0, 1}, {ms(300), 1}, {time.Second, 1}, {ms(1001), 2}, {ms(4500), 5}} {
		if got := Len(tc.elapsed); got != tc.want {
			t.Errorf("Len(%v) = %d, want %d", tc.elapsed, got, tc.want)
		}
	}
	if covered(4, ms(4500)) != 0.5 || covered(3, ms(4500)) != 1 || covered(5, ms(4500)) != 0 {
		t.Error("covered is wrong around the last second")
	}
}

func TestSlices(t *testing.T) {
	for _, tc := range []struct {
		n, rows, width, spans int
	}{{3, 20, 1, 3}, {20, 20, 1, 20}, {21, 20, 2, 11}, {600, 20, 30, 20}, {601, 20, 31, 20}} {
		w, spans := Slices(tc.n, tc.rows)
		if w != tc.width || len(spans) != tc.spans {
			t.Errorf("Slices(%d, %d) = width %d, %d spans; want %d, %d", tc.n, tc.rows, w, len(spans), tc.width, tc.spans)
		}
		if spans[len(spans)-1][1] != tc.n || spans[0][0] != 0 {
			t.Errorf("Slices(%d) spans %v do not cover 0..n", tc.n, spans)
		}
	}
}

func TestSampler(t *testing.T) {
	start := time.Now()
	n := 0
	s := StartSampler(start, func(at time.Duration) Level { n++; return Level{Active: n} })
	time.Sleep(1100 * time.Millisecond)
	levels := s.Stop()
	if len(levels) != 2 || levels[0].Active != 1 || levels[1].Active != 2 {
		t.Errorf("levels = %+v, want one at 1 s and one at Stop", levels)
	}
	if again := s.Stop(); len(again) != 2 {
		t.Errorf("second Stop returned %d levels", len(again))
	}
}
