package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/namnguyen3019/pelt/internal/testshop"
)

// shopScenario is the example shop journey, pointed at base.
const shopScenario = `{
  "name": "shop",
  "base_url": "BASE",
  "defaults": {"headers": {"Accept": "application/json"}},
  "data": {"file": "users.csv"},
  "setup": [
    {"name": "login", "method": "POST", "url": "/login",
     "json": {"username": "{{username}}", "password": "{{password}}"},
     "check": {"status": 200},
     "capture": [{"json": "token", "as": "token"}]}
  ],
  "flows": [
    {"name": "browse", "weight": 7, "steps": [
      {"name": "list products", "url": "/products?limit=5",
       "capture": [{"json": "items[0].id", "as": "product_id"}]},
      {"name": "view product", "url": "/products/{{product_id}}", "check": {"status": 200}}
    ]},
    {"name": "checkout", "weight": 3, "steps": [
      {"name": "list products", "url": "/products?limit=5",
       "capture": [{"json": "items[1].id", "as": "product_id"}]},
      {"name": "add to cart", "method": "POST", "url": "/cart",
       "headers": {"Authorization": "Bearer {{token}}"},
       "json": {"product_id": "{{product_id}}", "qty": 1}, "check": {"status": [200, 201]}},
      {"name": "checkout", "method": "POST", "url": "/checkout", "check": {"status": 201},
       "capture": [{"json": "order_id", "as": "order_id"}]},
      {"name": "order status", "url": "/orders/{{order_id}}", "check": {"status": 200}}
    ]}
  ]
}`

// load writes src (with BASE replaced) and a users.csv to a temp dir and
// loads it.
func load(t *testing.T, src, base string) *Scenario {
	t.Helper()
	dir := t.TempDir()
	var csv strings.Builder
	csv.WriteString("username,password\n")
	for i := 1; i <= 100; i++ {
		csv.WriteString("user" + itoa(i) + ",pw\n")
	}
	os.WriteFile(filepath.Join(dir, "users.csv"), []byte(csv.String()), 0o644)
	path := filepath.Join(dir, "s.json")
	os.WriteFile(path, []byte(strings.ReplaceAll(src, "BASE", base)), 0o644)
	sc, err := LoadFile(path, "")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	return sc
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func runScenario(t *testing.T, sc *Scenario, opts Options) *Result {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	r, err := New(sc, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r.setupBackoff = 10 * time.Millisecond
	return r.Run(context.Background())
}

func stepByName(t *testing.T, res *Result, flow, name string) *StepResult {
	t.Helper()
	for i := range res.Steps {
		if res.Steps[i].Step.Flow == flow && res.Steps[i].Step.Name == name {
			return &res.Steps[i]
		}
	}
	t.Fatalf("no step %s / %s", flow, name)
	return nil
}

func TestShopFlowsEndToEnd(t *testing.T) {
	shop := testshop.New()
	srv := httptest.NewServer(shop)
	defer srv.Close()

	sc := load(t, shopScenario, srv.URL)
	res := runScenario(t, sc, Options{Users: 10, Iterations: 300, Seed: 7})

	started, completed, aborted, cutN := res.Iterations()
	if started != 300 || completed != 300 || aborted != 0 || cutN != 0 {
		t.Fatalf("iterations started/completed/aborted/cut = %d/%d/%d/%d, want 300/300/0/0", started, completed, aborted, cutN)
	}
	if res.CheckFails() != 0 {
		for _, s := range res.Steps {
			t.Logf("%s / %s: fails=%d status=%v errors=%v", s.Step.Flow, s.Step.Name, s.CheckFails, s.StatusCodes, s.Errors)
		}
		t.Fatalf("%d failed checks, want 0", res.CheckFails())
	}
	if got := stepByName(t, res, "setup", "login").Requests; got != 10 {
		t.Errorf("login ran %d times, want once per user (10)", got)
	}
	if shop.Sessions() != 10 {
		t.Errorf("shop saw %d sessions, want 10", shop.Sessions())
	}
	co := res.Flows[1]
	if co.Completed != shop.Orders() {
		t.Errorf("%d checkouts completed but the shop has %d orders", co.Completed, shop.Orders())
	}
	// Weights 7:3 over 300 iterations: allow plenty of slack.
	if b := res.Flows[0].Started; b < 170 || b > 250 {
		t.Errorf("browse ran %d of 300 iterations, want about 210", b)
	}
	if len(co.FlowTimes) != co.Completed || len(co.WallTimes) != co.Completed {
		t.Errorf("flow times %d / wall times %d for %d completed", len(co.FlowTimes), len(co.WallTimes), co.Completed)
	}
	// One shared, tuned pool: about one connection per user.
	if res.ConnsNew > 20 {
		t.Errorf("opened %d connections for 10 users, want about 10", res.ConnsNew)
	}
}

// TestCookieIsolation proves each user's cookie jar is its own: two users
// log in as different people, add different products using only the
// session cookie, and each sees only its own cart.
func TestCookieIsolation(t *testing.T) {
	shop := testshop.New()
	var mu sync.Mutex
	carts := map[string]string{} // user -> cart JSON seen by that user
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/cart" {
			rec := httptest.NewRecorder()
			shop.ServeHTTP(rec, r)
			var c struct {
				User  string `json:"user"`
				Items []any  `json:"items"`
			}
			json.Unmarshal(rec.Body.Bytes(), &c)
			items, _ := json.Marshal(c.Items)
			mu.Lock()
			carts[c.User] = string(items)
			mu.Unlock()
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
			return
		}
		shop.ServeHTTP(w, r)
	}))
	defer srv.Close()

	sc := load(t, `{
	  "base_url": "BASE",
	  "data": {"file": "users.csv"},
	  "setup": [{"name": "login", "method": "POST", "url": "/login",
	             "json": {"username": "{{username}}", "password": "{{password}}"}, "check": {"status": 200}}],
	  "flows": [{"name": "f", "steps": [
	    {"name": "add", "method": "POST", "url": "/cart", "json": {"product_id": "{{$vu}}"}, "check": {"status": 200}},
	    {"name": "cart", "url": "/cart", "check": {"status": 200}}
	  ]}]
	}`, srv.URL)
	res := runScenario(t, sc, Options{Users: 2, Iterations: 2})
	if res.CheckFails() != 0 {
		t.Fatalf("%d failed checks (cookies not sent?)", res.CheckFails())
	}
	// Users 1 and 2 each ran one iteration, or one of them ran both; either
	// way, every cart holds only that user's own product id.
	for user, items := range carts {
		want := map[string]string{"user1": `[{"product_id":1`, "user2": `[{"product_id":2`}[user]
		if want == "" || !strings.HasPrefix(items, want) || strings.Count(items, "product_id") != 1 {
			t.Errorf("%s saw cart %s", user, items)
		}
	}
	if len(carts) == 0 {
		t.Fatal("no cart was read")
	}
}

func TestCookiesOff(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	sc := load(t, `{
	  "base_url": "BASE",
	  "defaults": {"cookies": false},
	  "setup": [{"name": "login", "method": "POST", "url": "/login", "json": {"username": "user1", "password": "x"}}],
	  "flows": [{"name": "f", "steps": [{"name": "cart", "url": "/cart", "check": {"status": 401}}]}]
	}`, srv.URL)
	res := runScenario(t, sc, Options{Users: 1, Iterations: 3})
	if res.CheckFails() != 0 {
		t.Errorf("with cookies off /cart should answer 401; %d failed checks", res.CheckFails())
	}
}

// TestFailedCheckAborts checks that a failed check stops the iteration so
// later steps send nothing, and that on_fail continue sends them anyway.
func TestFailedCheckAborts(t *testing.T) {
	var second atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			second.Add(1)
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	for _, onFail := range []string{"abort", "continue"} {
		second.Store(0)
		sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
		  {"name": "first", "url": "/first", "check": {"status": 200}, "on_fail": "`+onFail+`"},
		  {"name": "second", "url": "/second"}]}]}`, srv.URL)
		res := runScenario(t, sc, Options{Users: 2, Iterations: 20})
		f := res.Flows[0]
		first := stepByName(t, res, "f", "first")
		if first.CheckFails != 20 {
			t.Errorf("%s: first step failed checks = %d, want 20", onFail, first.CheckFails)
		}
		if onFail == "abort" {
			if second.Load() != 0 || f.Aborted != 20 || f.Completed != 0 || first.AbortedIterations != 20 {
				t.Errorf("abort: second got %d requests, flow %+v", second.Load(), f)
			}
		} else if second.Load() != 20 || f.Completed != 20 || f.Aborted != 0 {
			t.Errorf("continue: second got %d requests, flow completed %d aborted %d", second.Load(), f.Completed, f.Aborted)
		}
	}
}

func TestFailedCaptureAborts(t *testing.T) {
	var second atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			second.Add(1)
		}
		io.WriteString(w, `{"other": 1}`)
	}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "first", "url": "/first", "capture": [{"json": "id", "as": "id"}]},
	  {"name": "second", "url": "/second/{{id}}"}]}]}`, srv.URL)
	res := runScenario(t, sc, Options{Users: 1, Iterations: 5})
	if second.Load() != 0 || res.Flows[0].Aborted != 5 {
		t.Errorf("a missing capture should abort: second got %d, flow %+v", second.Load(), res.Flows[0])
	}
}

// TestChaining checks that a value captured in one step reaches the next,
// URL-escaped in the path and JSON-escaped in the body.
func TestChaining(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			w.Header().Set("X-Next", `a "b"/c`)
			io.WriteString(w, `{"order": {"id": "o-42"}}`)
		default:
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotPath, gotBody = r.URL.EscapedPath(), string(b)
			mu.Unlock()
		}
	}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "start", "url": "/start", "capture": [{"json": "order.id", "as": "oid"}, {"header": "X-Next", "as": "next"}]},
	  {"name": "use", "method": "POST", "url": "/orders/{{oid}}", "json": {"note": "{{next}}"}}]}]}`, srv.URL)
	res := runScenario(t, sc, Options{Users: 1, Iterations: 1})
	if res.Flows[0].Completed != 1 {
		t.Fatalf("flow = %+v", res.Flows[0])
	}
	if gotPath != "/orders/o-42" {
		t.Errorf("path = %q, want /orders/o-42", gotPath)
	}
	if gotBody != `{"note":"a \"b\"/c"}` {
		t.Errorf("body = %s", gotBody)
	}
}

// TestSetupFailureRetiresUser checks that a user whose setup keeps failing
// retries, then gives up without running flows, and that the run still
// ends even with an iteration count nobody is left to reach.
func TestSetupFailureRetiresUser(t *testing.T) {
	var logins, flows atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			logins.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		flows.Add(1)
	}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE",
	  "setup": [{"name": "login", "url": "/login", "check": {"status": 200}}],
	  "flows": [{"name": "f", "steps": [{"url": "/x"}]}]}`, srv.URL)

	done := make(chan *Result)
	go func() { done <- runScenario(t, sc, Options{Users: 3, Iterations: 100}) }()
	var res *Result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not end after every user gave up")
	}
	if res.SetupFailedVUs != 3 {
		t.Errorf("SetupFailedVUs = %d, want 3", res.SetupFailedVUs)
	}
	if logins.Load() != 3*setupAttempts || flows.Load() != 0 {
		t.Errorf("logins = %d (want %d), flow requests = %d (want 0)", logins.Load(), 3*setupAttempts, flows.Load())
	}
}

// TestDurationEndsOnTime checks that -d ends the run on time even with long
// think times, and that iterations stopped part way count as cut.
func TestDurationEndsOnTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "a", "url": "/a", "think": "10s"},
	  {"name": "b", "url": "/b"}]}]}`, srv.URL)

	start := time.Now()
	res := runScenario(t, sc, Options{Users: 4, Duration: 300 * time.Millisecond})
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("run took %v; think time should be cut short at the deadline", el)
	}
	f := res.Flows[0]
	if f.Cut != 4 || f.Completed != 0 || f.Aborted != 0 {
		t.Errorf("flow = started %d completed %d aborted %d cut %d, want 4 cut", f.Started, f.Completed, f.Aborted, f.Cut)
	}
	if res.Interrupted {
		t.Error("a deadline is not an interrupt")
	}
}

// TestCancelReturnsPartialResults is Ctrl-C: in-flight requests abort and
// the run returns what it has.
func TestCancelReturnsPartialResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		}
	}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "fast", "url": "/fast"}, {"name": "slow", "url": "/slow"}]}]}`, srv.URL)
	r, err := New(sc, Options{Users: 3, Duration: time.Minute, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := r.Run(ctx)
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancel did not stop the run promptly")
	}
	slow := stepByName(t, res, "f", "slow")
	if !res.Interrupted || slow.Aborted != 3 || slow.Requests != 0 || res.Flows[0].Cut != 3 {
		t.Errorf("interrupted=%v slow aborted=%d requests=%d cut=%d", res.Interrupted, slow.Aborted, slow.Requests, res.Flows[0].Cut)
	}
}

func TestTransportErrorAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing listens any more
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "a", "url": "/a", "on_fail": "continue"}, {"name": "b", "url": "/b"}]}]}`, base)
	res := runScenario(t, sc, Options{Users: 1, Iterations: 3})
	a, b := stepByName(t, res, "f", "a"), stepByName(t, res, "f", "b")
	if a.ErrorCount() != 3 || a.Errors["connection refused"] != 3 || b.Requests != 0 || res.Flows[0].Aborted != 3 {
		t.Errorf("a errors=%v b requests=%d flow=%+v", a.Errors, b.Requests, res.Flows[0])
	}
}

func TestDryRun(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	sc := load(t, shopScenario, srv.URL)
	var out bytes.Buffer
	if !DryRun(context.Background(), sc, Options{Seed: 1}, &out) {
		t.Fatalf("dry run failed:\n%s", out.String())
	}
	s := out.String()
	for _, want := range []string{
		"POST " + srv.URL + "/login",
		`body {"username":"user1","password":"pw"}`,
		"capture token = ",
		"GET " + srv.URL + "/products/1",
		"-> 201 Created",
		"capture order_id = ",
		"All steps passed.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, s)
		}
	}

	// A failing check is reported and makes the dry run fail.
	bad := load(t, strings.Replace(shopScenario, `"check": {"status": 201}`, `"check": {"status": 204}`, 1), srv.URL)
	out.Reset()
	if DryRun(context.Background(), bad, Options{}, &out) {
		t.Errorf("dry run should fail:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "check FAILED: status 201, want 204") ||
		!strings.Contains(out.String(), "rest of checkout skipped") {
		t.Errorf("failure not shown:\n%s", out.String())
	}
}

func TestSeriesCountsFlowRun(t *testing.T) {
	srv := httptest.NewServer(testshop.New())
	defer srv.Close()
	sc := load(t, shopScenario, srv.URL)
	res := runScenario(t, sc, Options{Users: 3, Duration: 1200 * time.Millisecond})

	if !res.Series.Flow || len(res.Series.Counts) != 2 {
		t.Fatalf("series = flow %v, %d rows; want flow and 2 rows", res.Series.Flow, len(res.Series.Counts))
	}
	tot := res.Totals()
	_, completed, aborted, _ := res.Iterations()
	var req, resp, iters, ab int
	for _, c := range res.Series.Counts {
		req += c.Requests
		resp += c.Responses
		iters += c.Iterations
		ab += c.Aborted
	}
	if req != tot.Total || resp != tot.Successes {
		t.Errorf("series sums %d requests, %d responses; run has %d, %d", req, resp, tot.Total, tot.Successes)
	}
	if iters != completed+aborted || ab != aborted {
		t.Errorf("series has %d iterations (%d aborted), run has %d completed + %d aborted", iters, ab, completed, aborted)
	}
	for i := range res.Steps {
		if len(res.Steps[i].Finish) != len(res.Steps[i].Latencies) {
			t.Errorf("step %d: %d finish times for %d latencies", i, len(res.Steps[i].Finish), len(res.Steps[i].Latencies))
		}
	}
	if res.Series.Levels[0].Active != 3 {
		t.Errorf("active users after 1 s = %d, want 3", res.Series.Levels[0].Active)
	}
}
