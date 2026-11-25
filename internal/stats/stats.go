// Package stats turns raw latency samples into the numbers a load test
// reports: min, mean, max, standard deviation, percentiles and a histogram.
//
// Everything here is pure computation with no I/O, which keeps it easy to
// unit test independently of the networking code.
package stats

import (
	"math"
	"slices"
	"time"
)

// DefaultPercentiles are the percentiles reported when the caller does not
// ask for specific ones.
var DefaultPercentiles = []float64{50, 90, 95, 99}

// Percentile is a single percentile value, e.g. {P: 99, Value: 12ms}.
type Percentile struct {
	P     float64
	Value time.Duration
}

// Summary describes a set of latency samples.
type Summary struct {
	Count       int
	Min         time.Duration
	Max         time.Duration
	Mean        time.Duration
	StdDev      time.Duration
	Percentiles []Percentile
}

// Summarize computes a Summary over samples.
//
// It sorts samples in place: callers hand over a slice they no longer need,
// which avoids copying what can be millions of values. An empty slice yields
// a zero Summary.
func Summarize(samples []time.Duration, percentiles []float64) Summary {
	s := Summary{Count: len(samples)}
	if len(samples) == 0 {
		return s
	}
	slices.Sort(samples)

	s.Min = samples[0]
	s.Max = samples[len(samples)-1]

	// Accumulate in float64 so a very long run cannot overflow int64
	// nanoseconds, then use a second pass for the (population) variance.
	var sum float64
	for _, d := range samples {
		sum += float64(d)
	}
	mean := sum / float64(len(samples))
	var sq float64
	for _, d := range samples {
		diff := float64(d) - mean
		sq += diff * diff
	}
	s.Mean = time.Duration(math.Round(mean))
	s.StdDev = time.Duration(math.Round(math.Sqrt(sq / float64(len(samples)))))

	for _, p := range percentiles {
		s.Percentiles = append(s.Percentiles, Percentile{P: p, Value: PercentileOf(samples, p)})
	}
	return s
}

// PercentileOf returns the p-th percentile (0 <= p <= 100) of an already
// sorted slice using the nearest-rank method: the smallest sample such that
// at least p% of samples are less than or equal to it.
//
// Nearest-rank always returns a value that was actually observed, which is
// what you want for latency ("99% of requests took at most X").
func PercentileOf(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[n-1]
	}
	// 1-based rank. The tiny epsilon absorbs float error such as
	// 99.9/100*1000 == 999.0000000000001, which would otherwise round up.
	rank := int(math.Ceil(p/100*float64(n) - 1e-9))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// Bucket is one bar of a latency histogram covering [Low, High).
// The last bucket also includes its High edge, so the maximum is counted.
type Bucket struct {
	Low   time.Duration
	High  time.Duration
	Count int
}

// Histogram splits samples into n equal-width buckets between the smallest
// and largest sample. Samples need not be sorted. If every sample has the
// same value, a single bucket is returned.
func Histogram(samples []time.Duration, n int) []Bucket {
	if len(samples) == 0 || n <= 0 {
		return nil
	}
	lo, hi := slices.Min(samples), slices.Max(samples)
	if lo == hi {
		return []Bucket{{Low: lo, High: hi, Count: len(samples)}}
	}

	width := float64(hi-lo) / float64(n)
	buckets := make([]Bucket, n)
	for i := range buckets {
		buckets[i].Low = lo + time.Duration(math.Round(float64(i)*width))
		buckets[i].High = lo + time.Duration(math.Round(float64(i+1)*width))
	}
	buckets[n-1].High = hi // avoid rounding drift on the final edge

	for _, d := range samples {
		i := int(float64(d-lo) / width)
		if i >= n { // d == hi lands exactly on the top edge
			i = n - 1
		}
		buckets[i].Count++
	}
	return buckets
}
