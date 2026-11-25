package stats

import (
	"testing"
	"time"
)

func ms(v ...int) []time.Duration {
	out := make([]time.Duration, len(v))
	for i, x := range v {
		out[i] = time.Duration(x) * time.Millisecond
	}
	return out
}

func TestPercentileOf(t *testing.T) {
	oneToTen := ms(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	tests := []struct {
		name   string
		sorted []time.Duration
		p      float64
		want   time.Duration
	}{
		{"empty", nil, 50, 0},
		{"single", ms(7), 99, 7 * time.Millisecond},
		{"p0 is min", oneToTen, 0, 1 * time.Millisecond},
		{"p10", oneToTen, 10, 1 * time.Millisecond},
		{"p50", oneToTen, 50, 5 * time.Millisecond},
		{"p51 rounds up a rank", oneToTen, 51, 6 * time.Millisecond},
		{"p90", oneToTen, 90, 9 * time.Millisecond},
		{"p95", oneToTen, 95, 10 * time.Millisecond},
		{"p99", oneToTen, 99, 10 * time.Millisecond},
		{"p100 is max", oneToTen, 100, 10 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PercentileOf(tt.sorted, tt.p); got != tt.want {
				t.Errorf("PercentileOf(p=%v) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}

func TestPercentileOfLargeSet(t *testing.T) {
	// 1..1000 ms: the nearest-rank p99 is exactly the 990th value.
	s := make([]time.Duration, 1000)
	for i := range s {
		s[i] = time.Duration(i+1) * time.Millisecond
	}
	for p, want := range map[float64]int{50: 500, 90: 900, 99: 990, 99.9: 999} {
		if got := PercentileOf(s, p); got != time.Duration(want)*time.Millisecond {
			t.Errorf("p%v = %v, want %dms", p, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	// Deliberately unsorted; Summarize must sort it.
	s := Summarize(ms(4, 2, 8, 6), DefaultPercentiles)

	if s.Count != 4 {
		t.Fatalf("Count = %d, want 4", s.Count)
	}
	if s.Min != 2*time.Millisecond || s.Max != 8*time.Millisecond {
		t.Errorf("Min/Max = %v/%v, want 2ms/8ms", s.Min, s.Max)
	}
	if s.Mean != 5*time.Millisecond {
		t.Errorf("Mean = %v, want 5ms", s.Mean)
	}
	// Population std dev of {2,4,6,8} is sqrt(5) ms ≈ 2.236068ms.
	if want := 2236068 * time.Nanosecond; s.StdDev != want {
		t.Errorf("StdDev = %v, want %v", s.StdDev, want)
	}
	wantP := map[float64]time.Duration{50: 4 * time.Millisecond, 90: 8 * time.Millisecond, 95: 8 * time.Millisecond, 99: 8 * time.Millisecond}
	if len(s.Percentiles) != len(DefaultPercentiles) {
		t.Fatalf("got %d percentiles, want %d", len(s.Percentiles), len(DefaultPercentiles))
	}
	for _, p := range s.Percentiles {
		if p.Value != wantP[p.P] {
			t.Errorf("p%v = %v, want %v", p.P, p.Value, wantP[p.P])
		}
	}
}

func TestSummarizeEmpty(t *testing.T) {
	s := Summarize(nil, DefaultPercentiles)
	if s.Count != 0 || s.Mean != 0 || s.Percentiles != nil {
		t.Errorf("Summarize(nil) = %+v, want zero value", s)
	}
}

func TestHistogram(t *testing.T) {
	samples := ms(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	b := Histogram(samples, 5)
	if len(b) != 5 {
		t.Fatalf("got %d buckets, want 5", len(b))
	}
	// Width is 2ms: [0,2) [2,4) [4,6) [6,8) [8,10]
	want := []int{2, 2, 2, 2, 3}
	total := 0
	for i, bk := range b {
		if bk.Count != want[i] {
			t.Errorf("bucket %d [%v,%v) = %d, want %d", i, bk.Low, bk.High, bk.Count, want[i])
		}
		total += bk.Count
	}
	if total != len(samples) {
		t.Errorf("bucket counts sum to %d, want %d", total, len(samples))
	}
	if b[0].Low != 0 || b[4].High != 10*time.Millisecond {
		t.Errorf("edges = %v..%v, want 0..10ms", b[0].Low, b[4].High)
	}
}

func TestHistogramIdenticalValues(t *testing.T) {
	b := Histogram(ms(3, 3, 3), 10)
	if len(b) != 1 || b[0].Count != 3 {
		t.Errorf("Histogram(identical) = %+v, want one bucket of 3", b)
	}
}

func TestHistogramEmpty(t *testing.T) {
	if b := Histogram(nil, 10); b != nil {
		t.Errorf("Histogram(nil) = %v, want nil", b)
	}
}
