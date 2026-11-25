package flow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/namnguyen3019/pelt/internal/stages"
)

func TestParseStages(t *testing.T) {
	sc, err := Parse([]byte(`{
  "base_url": "http://x",
  "load": {
    "stages": [
      {"duration": "1m", "users": 100},
      {"duration": "5m", "users": 100, "name": "hold"},
      {"duration": "0s", "users": 20}
    ],
    "iterations": 5000
  },
  "flows": [{"name": "f", "steps": [{"url": "/"}]}]
}`), ".", "")
	if err != nil {
		t.Fatal(err)
	}
	p := sc.Load.Stages
	if p == nil || p.String() != "1m:100,5m:100,0s:20" || p.Stages[1].Name != "hold" || p.Kind != stages.Level {
		t.Fatalf("stages = %+v", p)
	}
	o := sc.Options()
	if o.Stages != p || o.Iterations != 5000 {
		t.Errorf("options = %+v", o)
	}
	if err := o.validate(sc); err != nil || o.Users != 100 {
		t.Errorf("validate: users %d, err %v; want 100 users", o.Users, err)
	}
}

func TestParseStagesErrors(t *testing.T) {
	flows := `"flows": [{"name": "f", "steps": [{"url": "/"}]}]`
	cases := []struct{ name, load, want string }{
		{"with users", `{"users": 5, "stages": [{"duration": "1m", "users": 5}]}`, "stages or users"},
		{"with duration", `{"duration": "1m", "stages": [{"duration": "1m", "users": 5}]}`, "stages or duration"},
		{"empty", `{"stages": []}`, "is empty"},
		{"no duration", `{"stages": [{"users": 5}]}`, "load.stages[0]: duration is required"},
		{"no users", `{"stages": [{"duration": "1m"}]}`, "load.stages[0]: users is required"},
		{"bad duration", `{"stages": [{"duration": "1m", "users": 5}, {"duration": "soon", "users": 5}]}`, "load.stages[1]: duration \"soon\""},
		{"negative", `{"stages": [{"duration": "1m", "users": -5}]}`, "cannot be negative"},
		{"unknown key", `{"stages": [{"duration": "1m", "users": 5, "rate": 3}]}`, `unknown field "rate"`},
		{"fractional users", `{"stages": [{"duration": "1m", "users": 2.5}]}`, "load.stages[0]"},
		{"zero total", `{"stages": [{"duration": "0s", "users": 5}]}`, "add up to 0s"},
		{"all zero", `{"stages": [{"duration": "1m", "users": 0}]}`, "target of 0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "{\n\"base_url\": \"http://x\",\n\"load\": " + c.load + ",\n" + flows + "}"
			_, err := Parse([]byte(src), ".", "")
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "line 3") {
				t.Errorf("err = %v, want one on line 3 containing %q", err, c.want)
			}
		})
	}
}

func TestStagesNeedDataRows(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "u.csv"), []byte("username\nuser1\nuser2\n"), 0o644)
	sc, err := Parse([]byte(`{"base_url": "http://x", "data": {"file": "u.csv"},
	  "load": {"stages": [{"duration": "1m", "users": 3}, {"duration": "1m", "users": 1}]},
	  "flows": [{"name": "a", "steps": [{"url": "/{{username}}"}]}]}`), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(sc, sc.Options()); err == nil || !strings.Contains(err.Error(), "stages reach 3 users") {
		t.Errorf("err = %v, want too few rows for the highest stage", err)
	}
}

// userLog is a test server that logs in each user once per setup and
// remembers when each user sent a request.
type userLog struct {
	mu     sync.Mutex
	start  time.Time
	setups map[string]int
	seen   map[string][]time.Duration
}

func newUserLog() *userLog {
	return &userLog{start: time.Now(), setups: map[string]int{}, seen: map[string][]time.Duration{}}
}

func (u *userLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vu := r.URL.Query().Get("vu")
	u.mu.Lock()
	defer u.mu.Unlock()
	switch r.URL.Path {
	case "/login":
		u.setups[vu]++
		http.SetCookie(w, &http.Cookie{Name: "vu", Value: vu})
	default:
		if c, err := r.Cookie("vu"); err != nil || c.Value != vu {
			w.WriteHeader(http.StatusUnauthorized) // the cookie did not survive
			return
		}
		u.seen[vu] = append(u.seen[vu], time.Since(u.start))
	}
}

// between counts vu's requests in [from, to).
func (u *userLog) between(vu string, from, to time.Duration) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, at := range u.seen[vu] {
		if at >= from && at < to {
			n++
		}
	}
	return n
}

const userLogScenario = `{"base_url": "BASE",
  "setup": [{"name": "login", "method": "POST", "url": "/login?vu={{$vu}}", "check": {"status": 200}}],
  "flows": [{"name": "f", "steps": [{"name": "a", "url": "/a?vu={{$vu}}", "check": {"status": 200}, "think": "40ms"}]}]}`

func TestStagesParkAndWake(t *testing.T) {
	ul := newUserLog()
	srv := httptest.NewServer(ul)
	defer srv.Close()
	sc := load(t, userLogScenario, srv.URL)
	plan, _ := stages.Parse("0s:3,600ms:3,0s:1,600ms:1,0s:3,600ms:3")

	ul.start = time.Now()
	res := runScenario(t, sc, Options{Stages: &plan})

	if res.CheckFails() != 0 {
		t.Errorf("%d failed checks: a parked user lost its cookie", res.CheckFails())
	}
	for _, vu := range []string{"1", "2", "3"} {
		if n := ul.setups[vu]; n != 1 {
			t.Errorf("user %s ran setup %d times, want once", vu, n)
		}
	}
	for _, vu := range []string{"2", "3"} {
		before, parked, after := ul.between(vu, 0, 550*time.Millisecond),
			ul.between(vu, 850*time.Millisecond, 1150*time.Millisecond),
			ul.between(vu, 1400*time.Millisecond, 1800*time.Millisecond)
		if before == 0 || parked != 0 || after == 0 {
			t.Errorf("user %s sent %d, %d, %d requests before, while parked and after; want some, none, some", vu, before, parked, after)
		}
	}
	if ul.between("1", 850*time.Millisecond, 1150*time.Millisecond) == 0 {
		t.Error("user 1 should keep running through the low stage")
	}
	// Second 0's midpoint falls in stage 2 (stage 1 is a 0s jump to 3).
	if !res.Series.Staged || len(res.Series.Levels) < 2 || res.Series.Levels[0].Stage != 2 || res.Series.Levels[0].Target != 3 {
		t.Errorf("series levels = %+v", res.Series.Levels)
	}
}

func TestRampDownFinishesIteration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [
	  {"name": "a", "url": "/a", "think": "5s"},
	  {"name": "b", "url": "/b", "think": "5s"}]}]}`, srv.URL)
	plan, _ := stages.Parse("0s:2,300ms:2,0s:0,700ms:0")

	start := time.Now()
	res := runScenario(t, sc, Options{Stages: &plan})
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("run took %v; the stages add up to 1s", el)
	}
	f := res.Flows[0]
	// At 300ms both users are thinking after step a. The ramp-down ends
	// their think, they run step b, finish the iteration and park.
	if f.Started != 2 || f.Completed != 2 || f.Cut != 0 {
		t.Errorf("flow = started %d completed %d cut %d; want 2 finished, none cut", f.Started, f.Completed, f.Cut)
	}
	if lv := res.Series.Levels[len(res.Series.Levels)-1]; lv.Active != 0 {
		t.Errorf("active users at the end = %d, want 0", lv.Active)
	}
}

func TestStagesStopAtIterationCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "flows": [{"name": "f", "steps": [{"name": "a", "url": "/a"}]}]}`, srv.URL)
	// Users 2 and 3 wait for a stage that never comes; the count still
	// ends the run.
	plan, _ := stages.Parse("0s:1,10s:1,0s:3,10s:3")
	start := time.Now()
	res := runScenario(t, sc, Options{Stages: &plan, Iterations: 20})
	if _, completed, _, _ := res.Iterations(); completed != 20 || time.Since(start) > 2*time.Second {
		t.Errorf("%d iterations in %v, want 20 well before the stages end", completed, time.Since(start))
	}
}

func TestDryRunIgnoresStages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	sc := load(t, `{"base_url": "BASE", "load": {"stages": [{"duration": "1h", "users": 50}]},
	  "flows": [{"name": "f", "steps": [{"name": "a", "url": "/a"}]}]}`, srv.URL)
	var out strings.Builder
	if !DryRun(context.Background(), sc, sc.Options(), &out) || !strings.Contains(out.String(), "1 user") {
		t.Errorf("dry run failed:\n%s", out.String())
	}
}
