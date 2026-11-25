package stages

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in    string
		kind  Kind
		want  string // String() of the result
		total time.Duration
		max   float64
	}{
		{"30s:100", Level, "30s:100", 30 * time.Second, 100},
		{"30s:200,2m:200,30s:0", Level, "30s:200,2m:200,30s:0", 3 * time.Minute, 200},
		{" 30s : 200 , 2m:200 ", Level, "30s:200,2m:200", 150 * time.Second, 200},
		{"0s:50,1m:50,0s:150,1m:150", Level, "0s:50,1m:50,0s:150,1m:150", 2 * time.Minute, 150},
		{"1m:500/s,5m:500/s", Rate, "1m:500/s,5m:500/s", 6 * time.Minute, 500},
		{"10s:12.5/s", Rate, "10s:12.5/s", 10 * time.Second, 12.5},
		{"1m30s:10", Level, "1m30s:10", 90 * time.Second, 10},
	}
	for _, tt := range tests {
		p, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if p.Kind != tt.kind || p.String() != tt.want || p.Total() != tt.total || p.Max() != tt.max {
			t.Errorf("Parse(%q) = kind %v %q total %v max %v; want kind %v %q total %v max %v",
				tt.in, p.Kind, p.String(), p.Total(), p.Max(), tt.kind, tt.want, tt.total, tt.max)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "no stages"},
		{"30s", "want duration:target"},
		{":100", "want duration:target"},
		{"30s:", "want duration:target"},
		{"30:100", "bad duration"},
		{"30s:abc", "bad target"},
		{"30s:100,30s:500/s", "not a mix"},
		{"30s:-5", "cannot be negative"},
		{"-30s:5", "cannot be negative"},
		{"0s:100", "add up to 0s"},
		{"30s:0,1m:0", "target of 0"},
		{"30s:10.5", "whole number"},
		{"30s:NaN/s", "bad target"},
		{"30s:100,", "want duration:target"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.in)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Parse(%q) error = %v, want one containing %q", tt.in, err, tt.want)
		}
	}
}

func TestTarget(t *testing.T) {
	p, err := Parse("10s:100,10s:100,10s:0,0s:40,10s:40")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		at   time.Duration
		want float64
	}{
		{-time.Second, 0},
		{0, 0},
		{5 * time.Second, 50},
		{10 * time.Second, 100}, // the edge between stage 1 and 2
		{15 * time.Second, 100},
		{25 * time.Second, 50},
		{30 * time.Second, 40}, // the 0s stage jumps
		{35 * time.Second, 40},
		{40 * time.Second, 40}, // after the end it holds the last target
		{time.Hour, 40},
	}
	for _, tt := range tests {
		if got := p.Target(tt.at); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("Target(%v) = %v, want %v", tt.at, got, tt.want)
		}
	}
}

func TestStageAt(t *testing.T) {
	p, _ := Parse("10s:100,0s:50,10s:50")
	tests := []struct {
		at   time.Duration
		want int
	}{
		{0, 1},
		{9999 * time.Millisecond, 1},
		{10 * time.Second, 3}, // a boundary belongs to the next stage; 0s stages are skipped
		{19 * time.Second, 3},
		{time.Minute, 3},
	}
	for _, tt := range tests {
		if got := p.StageAt(tt.at); got != tt.want {
			t.Errorf("StageAt(%v) = %d, want %d", tt.at, got, tt.want)
		}
	}
	start, end := p.Bounds(2)
	if start != 10*time.Second || end != 20*time.Second || p.From(2) != 50 || p.From(0) != 0 {
		t.Errorf("Bounds(2) = %v-%v, From(2) = %v", start, end, p.From(2))
	}
}

func TestRamp(t *testing.T) {
	p, err := Ramp(30*time.Second, 3*time.Minute, 200)
	if err != nil {
		t.Fatal(err)
	}
	long, _ := Parse("30s:200,2m30s:200")
	if p.String() != long.String() {
		t.Errorf("Ramp = %q, want %q", p, long)
	}
	if p, _ := Ramp(time.Minute, time.Minute, 5); p.String() != "1m:5" {
		t.Errorf("Ramp over the whole run = %q, want 1m:5", p)
	}
	for _, tc := range []struct {
		over, total time.Duration
		want        string
	}{
		{0, time.Minute, "longer than 0s"},
		{time.Minute, 0, "needs a run length"},
		{2 * time.Minute, time.Minute, "longer than the run"},
	} {
		if _, err := Ramp(tc.over, tc.total, 10); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Ramp(%v, %v) error = %v, want %q", tc.over, tc.total, err, tc.want)
		}
	}
}

func TestAreaAndTimeFor(t *testing.T) {
	// 0 → 100/s over 10s (500 requests), hold 10s (1,000), down to 0 over 10s (500).
	p, _ := Parse("10s:100/s,10s:100/s,10s:0/s")
	if got := p.Area(30 * time.Second); math.Abs(got-2000) > 1e-6 {
		t.Errorf("Area(end) = %v, want 2000", got)
	}
	if got := p.Area(5 * time.Second); math.Abs(got-125) > 1e-6 {
		t.Errorf("Area(5s) = %v, want 125", got)
	}
	for _, x := range []float64{0, 1, 125, 499.5, 500, 1000, 1500, 1750, 1999} {
		at, ok := p.TimeFor(x)
		if !ok {
			t.Errorf("TimeFor(%v) ran past the end", x)
			continue
		}
		if got := p.Area(at); math.Abs(got-x) > 1e-3 {
			t.Errorf("TimeFor(%v) = %v, whose area is %v", x, at, got)
		}
	}
	if _, ok := p.TimeFor(2001); ok {
		t.Error("TimeFor past the total area should report false")
	}

	// A stage at 0/s is skipped over: the next request waits for the rate to return.
	gap, _ := Parse("0s:10/s,1s:10/s,0s:0/s,5s:0/s,0s:10/s,1s:10/s")
	at, ok := gap.TimeFor(15)
	if !ok || at < 6*time.Second || at > 7*time.Second {
		t.Errorf("TimeFor across a 0/s gap = %v %v, want between 6s and 7s", at, ok)
	}
}

func TestDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:                   "30s",
		2 * time.Minute:                    "2m",
		90 * time.Second:                   "1m30s",
		time.Hour:                          "1h",
		1500 * time.Millisecond:            "1.5s",
		time.Hour + 30*time.Minute:         "1h30m",
		time.Hour + 30*time.Minute + 5*1e9: "1h30m5s",
		0:                                  "0s",
	} {
		if got := Dur(d); got != want {
			t.Errorf("Dur(%v) = %q, want %q", d, got, want)
		}
	}
}
