package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// orderServer answers /order/<id> with a small JSON order, and /slow after
// 50ms.
func orderServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(50 * time.Millisecond)
		}
		id := strings.TrimPrefix(r.URL.Path, "/order/")
		io.WriteString(w, `{"id": `+id+`, "status": "paid", "total": 42.50, "items": [{"sku": "A1", "qty": 2}], "note": null}`)
	}))
}

func TestBodyChecks(t *testing.T) {
	srv := orderServer()
	defer srv.Close()

	cases := []struct {
		name  string
		check string
		fails bool
	}{
		{"contains", `{"body_contains": "paid"}`, false},
		{"contains all", `{"body_contains": ["paid", "A1"]}`, false},
		{"contains missing", `{"body_contains": ["paid", "refunded"]}`, true},
		{"contains placeholder", `{"body_contains": "\"id\": {{$iteration}}"}`, false},
		{"json string", `{"json": {"status": "paid"}}`, false},
		{"json number by value", `{"json": {"total": 42.5, "items[0].qty": 2}}`, false},
		{"json object", `{"json": {"items[0]": {"qty": 2, "sku": "A1"}}}`, false},
		{"json null", `{"json": {"note": null}}`, false},
		{"json wrong value", `{"json": {"status": "refunded"}}`, true},
		{"json wrong type", `{"json": {"total": "42.5"}}`, true},
		{"json missing path", `{"json": {"customer.id": 1}}`, true},
		{"json placeholder matches a number", `{"json": {"id": "{{$iteration}}"}}`, false},
		{"latency ok", `{"max_latency": "5s"}`, false},
		{"with status", `{"status": 200, "json": {"status": "paid"}}`, false},
		{"status fails", `{"status": 201, "json": {"status": "paid"}}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
			  {"name": "get", "url": "/order/{{$iteration}}", "check": `+c.check+`}]}]}`, srv.URL)
			res := runScenario(t, sc, Options{Users: 1, Iterations: 3})
			got := stepByName(t, res, "f", "get").CheckFails
			if want := map[bool]int{true: 3, false: 0}[c.fails]; got != want {
				t.Errorf("failed checks = %d, want %d", got, want)
			}
		})
	}
}

func TestMaxLatencyCheck(t *testing.T) {
	srv := orderServer()
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "slow", "url": "/slow", "check": {"max_latency": "10ms"}}]}]}`, srv.URL)
	res := runScenario(t, sc, Options{Users: 1, Iterations: 2})
	if s := stepByName(t, res, "f", "slow"); s.CheckFails != 2 || res.Flows[0].Aborted != 2 {
		t.Errorf("failed checks = %d, aborted = %d; want 2 and 2", s.CheckFails, res.Flows[0].Aborted)
	}
}

func TestBodyCheckDryRun(t *testing.T) {
	srv := orderServer()
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "ok", "url": "/order/7", "check": {"status": 200, "body_contains": "paid", "json": {"id": 7}}},
	  {"name": "bad", "url": "/order/7", "check": {"json": {"status": "refunded", "id": 8}}}]}]}`, srv.URL)
	var out bytes.Buffer
	if DryRun(context.Background(), sc, Options{Timeout: 5 * time.Second}, &out) {
		t.Error("dry run passed with a failing check")
	}
	s := out.String()
	for _, want := range []string{
		`check ok: status 200, body contains "paid", id = 7`,
		`check FAILED: id is 7, want 8; status is "paid", want "refunded"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, s)
		}
	}
}

func TestJSONEqual(t *testing.T) {
	dec := func(s string) any {
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		a, b string
		eq   bool
	}{
		{`1`, `1.0`, true}, {`1e2`, `100`, true}, {`1`, `2`, false}, {`1`, `"1"`, false},
		{`[1,2]`, `[1,2]`, true}, {`[1,2]`, `[2,1]`, false}, {`{"a":1}`, `{"a":1.0}`, true},
		{`{"a":1}`, `{"a":1,"b":2}`, false}, {`null`, `null`, true}, {`true`, `false`, false},
	} {
		if got := jsonEqual(dec(c.a), dec(c.b)); got != c.eq {
			t.Errorf("jsonEqual(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// TestDataModes checks who gets which row: unique gives each user its own
// for the whole run; sequential hands rows out in order across users,
// wrapping; random spreads them using the seed.
func TestDataModes(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Query().Get("u"))
		mu.Unlock()
	}))
	defer srv.Close()
	reset := func() []string {
		mu.Lock()
		defer mu.Unlock()
		s := seen
		seen = nil
		return s
	}
	src := func(mode string) string {
		return `{"base_url": "BASE", "data": {"file": "users.csv", "mode": "` + mode + `"},
		  "flows": [{"name": "f", "steps": [{"name": "s", "url": "/?u={{username}}"}]}]}`
	}

	// unique: 2 users, each keeps its row.
	runScenario(t, load(t, src("unique"), srv.URL), Options{Users: 2, Iterations: 10})
	counts := map[string]int{}
	for _, u := range reset() {
		counts[u]++
	}
	if len(counts) != 2 || counts["user1"]+counts["user2"] != 10 {
		t.Errorf("unique: rows used %v, want only user1 and user2", counts)
	}

	// sequential: one user, 150 iterations over 100 rows: rows in order,
	// then wrapping back to user1.
	runScenario(t, load(t, src("sequential"), srv.URL), Options{Users: 1, Iterations: 150})
	got := reset()
	if len(got) != 150 || got[0] != "user1" || got[99] != "user100" || got[100] != "user1" || got[149] != "user50" {
		t.Errorf("sequential: got %d requests, [0]=%s [99]=%s [100]=%s [149]=%s", len(got), got[0], got[99], got[100], got[149])
	}

	// sequential with more users than rows is allowed (unique refuses).
	sc := load(t, src("sequential"), srv.URL)
	sc.Data.Rows, sc.Data.maps = sc.Data.Rows[:3], sc.Data.maps[:3]
	if _, err := New(sc, Options{Users: 5, Iterations: 5, Timeout: time.Second}); err != nil {
		t.Errorf("sequential with 3 rows and 5 users: %v", err)
	}
	reset()

	// random: many rows used, and the same seed repeats the same rows.
	runOnce := func() []string {
		runScenario(t, load(t, src("random"), srv.URL), Options{Users: 1, Iterations: 50, Seed: 9})
		return reset()
	}
	a, b := runOnce(), runOnce()
	distinct := map[string]bool{}
	for _, u := range a {
		distinct[u] = true
	}
	if len(distinct) < 20 {
		t.Errorf("random: only %d distinct rows in 50 iterations", len(distinct))
	}
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Error("random: the same seed gave different rows")
	}
}
