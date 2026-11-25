package loadtest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newRunner is a test helper that fails the test on a config error.
func newRunner(t *testing.T, cfg Config) *Runner {
	t.Helper()
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestRunFixedRequestCount(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "hello")
	}))
	defer srv.Close()

	res := newRunner(t, Config{URL: srv.URL, Requests: 500, Concurrency: 20}).Run(context.Background())

	if res.Total != 500 || res.Successes != 500 || res.ErrorCount() != 0 {
		t.Errorf("total/ok/errors = %d/%d/%d, want 500/500/0 (errors: %v)",
			res.Total, res.Successes, res.ErrorCount(), res.Errors)
	}
	if got := hits.Load(); got != 500 {
		t.Errorf("server saw %d requests, want 500", got)
	}
	if res.StatusCodes[200] != 500 {
		t.Errorf("status codes = %v, want 500x200", res.StatusCodes)
	}
	if len(res.Latencies) != 500 {
		t.Errorf("got %d latency samples, want 500", len(res.Latencies))
	}
	if res.BytesRead != 500*5 {
		t.Errorf("BytesRead = %d, want %d", res.BytesRead, 500*5)
	}
	if res.RequestsPerSecond() <= 0 {
		t.Errorf("RequestsPerSecond = %v, want > 0", res.RequestsPerSecond())
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	var inFlight, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for { // record the highest concurrency the server ever saw
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
	}))
	defer srv.Close()

	res := newRunner(t, Config{URL: srv.URL, Requests: 64, Concurrency: 8}).Run(context.Background())

	if res.Total != 64 {
		t.Fatalf("Total = %d, want 64", res.Total)
	}
	if p := peak.Load(); p > 8 || p < 2 {
		t.Errorf("peak concurrency = %d, want between 2 and 8", p)
	}
}

func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	// 30 requests at 100/s: the last is scheduled 29 * 10ms = 290ms in.
	start := time.Now()
	res := newRunner(t, Config{URL: srv.URL, Requests: 30, Concurrency: 10, RateLimit: 100}).Run(context.Background())
	elapsed := time.Since(start)

	if res.Total != 30 {
		t.Fatalf("Total = %d, want 30", res.Total)
	}
	if elapsed < 280*time.Millisecond || elapsed > 2*time.Second {
		t.Errorf("elapsed = %v, want about 290ms", elapsed)
	}
}

func TestDurationMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(5 * time.Millisecond)
	}))
	defer srv.Close()

	start := time.Now()
	res := newRunner(t, Config{URL: srv.URL, Duration: 300 * time.Millisecond, Concurrency: 4}).Run(context.Background())
	elapsed := time.Since(start)

	if elapsed < 300*time.Millisecond || elapsed > time.Second {
		t.Errorf("elapsed = %v, want just over 300ms", elapsed)
	}
	if res.Total < 20 {
		t.Errorf("Total = %d, want at least 20 in 300ms with 4 workers", res.Total)
	}
	if res.Interrupted {
		t.Error("a run that hit its own deadline must not be marked Interrupted")
	}
	if res.ErrorCount() != 0 {
		t.Errorf("errors = %v; the deadline must not abort in-flight requests", res.Errors)
	}
}

func TestTimeoutsAreClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	res := newRunner(t, Config{URL: srv.URL, Requests: 5, Concurrency: 5, Timeout: 50 * time.Millisecond}).Run(context.Background())

	if res.Errors["timeout"] != 5 {
		t.Errorf("errors = %v, want 5 timeouts", res.Errors)
	}
	if len(res.Latencies) != 0 {
		t.Errorf("failed requests must not add latency samples, got %d", len(res.Latencies))
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens on the port any more

	res := newRunner(t, Config{URL: url, Requests: 10, Concurrency: 2}).Run(context.Background())

	if res.Errors["connection refused"] != 10 {
		t.Errorf("errors = %v, want 10 x connection refused", res.Errors)
	}
}

func TestRequestShapeAndStatusCodes(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("X-Test") != "yes" || string(body) != `{"a":1}` {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if n.Add(1)%2 == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	res := newRunner(t, Config{
		URL:         srv.URL,
		Method:      http.MethodPost,
		Header:      http.Header{"X-Test": {"yes"}},
		Body:        []byte(`{"a":1}`),
		Requests:    100,
		Concurrency: 10,
	}).Run(context.Background())

	if res.StatusCodes[201] != 50 || res.StatusCodes[503] != 50 {
		t.Errorf("status codes = %v, want 50x201 and 50x503", res.StatusCodes)
	}
	// HTTP error statuses are responses, not transport errors.
	if res.ErrorCount() != 0 {
		t.Errorf("errors = %v, want none", res.Errors)
	}
}

func TestCancelReturnsPartialResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel) // simulates Ctrl-C

	start := time.Now()
	res := newRunner(t, Config{URL: srv.URL, Requests: 10_000, Concurrency: 10}).Run(ctx)

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Run took %v after cancel, want a prompt return", elapsed)
	}
	if !res.Interrupted {
		t.Error("Interrupted = false, want true")
	}
	if res.Total == 0 || res.Total >= 10_000 {
		t.Errorf("Total = %d, want a partial count", res.Total)
	}
	if res.ErrorCount() != 0 {
		t.Errorf("errors = %v; cancelled requests must count as aborted, not errors", res.Errors)
	}
}

// TestConnectionReuse checks the point of the transport tuning: with the
// tuned pool each worker keeps one connection; with Go's stock limit of two
// idle connections per host, most finished connections get closed and
// re-dialled.
func TestConnectionReuse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(time.Millisecond)
	}))
	defer srv.Close()

	cfg := Config{URL: srv.URL, Requests: 400, Concurrency: 20}
	tuned := newRunner(t, cfg).Run(context.Background())

	cfg.Untuned = true
	untuned := newRunner(t, cfg).Run(context.Background())

	if tuned.ConnsNew > 20 {
		t.Errorf("tuned: %d new connections, want at most 20 (one per worker)", tuned.ConnsNew)
	}
	if tuned.ConnsReused < 380 {
		t.Errorf("tuned: %d requests reused a connection, want at least 380 of 400", tuned.ConnsReused)
	}
	if untuned.ConnsNew <= tuned.ConnsNew*2 {
		t.Errorf("untuned opened %d connections vs tuned %d; expected far more churn",
			untuned.ConnsNew, tuned.ConnsNew)
	}
	t.Logf("new connections: tuned=%d untuned=%d", tuned.ConnsNew, untuned.ConnsNew)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"ok", Config{URL: "http://x", Requests: 1, Concurrency: 1, Timeout: time.Second}, false},
		{"bad scheme", Config{URL: "ftp://x", Requests: 1, Concurrency: 1, Timeout: time.Second}, true},
		{"no host", Config{URL: "http://", Requests: 1, Concurrency: 1, Timeout: time.Second}, true},
		{"no stop condition", Config{URL: "http://x", Concurrency: 1, Timeout: time.Second}, true},
		{"zero concurrency", Config{URL: "http://x", Requests: 1, Timeout: time.Second}, true},
		{"negative rate", Config{URL: "http://x", Requests: 1, Concurrency: 1, Timeout: time.Second, RateLimit: -1}, true},
		{"no timeout", Config{URL: "http://x", Requests: 1, Concurrency: 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}

	// Workers are capped at the number of requests.
	cfg := Config{URL: "http://x", Requests: 3, Concurrency: 100, Timeout: time.Second}
	if err := cfg.Validate(); err != nil || cfg.Concurrency != 3 || cfg.Method != "GET" {
		t.Errorf("after Validate: concurrency=%d method=%q err=%v, want 3, GET, nil", cfg.Concurrency, cfg.Method, err)
	}
}

func TestSeriesAddsUpToTotals(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%5 == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		time.Sleep(time.Millisecond)
	}))
	defer srv.Close()

	res := newRunner(t, Config{URL: srv.URL, Duration: 1500 * time.Millisecond, Concurrency: 4, RateLimit: 200}).
		Run(context.Background())

	if len(res.Finish) != len(res.Latencies) {
		t.Fatalf("%d finish times for %d latencies", len(res.Finish), len(res.Latencies))
	}
	if len(res.Series.Counts) != 2 {
		t.Fatalf("%d series rows for a %v run, want 2", len(res.Series.Counts), res.Elapsed)
	}
	var req, resp, s5 int
	for _, c := range res.Series.Counts {
		req += c.Requests
		resp += c.Responses
		s5 += c.S5xx
	}
	if req != res.Total || resp != res.Successes || s5 != res.StatusCodes[503] {
		t.Errorf("series sums %d/%d/%d, run totals %d/%d/%d", req, resp, s5, res.Total, res.Successes, res.StatusCodes[503])
	}
	if first := res.Series.Counts[0].Requests; first < 150 || first > 210 {
		t.Errorf("first second has %d requests at 200/s", first)
	}
	if len(res.Series.Levels) != 2 || res.Series.Levels[0].Active > 4 {
		t.Errorf("levels = %+v, want 2 with at most 4 in flight", res.Series.Levels)
	}
}
