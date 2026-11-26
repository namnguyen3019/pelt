package flow

import (
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// minimal is a valid scenario other tests tweak.
const minimal = `{
  "base_url": "http://127.0.0.1:1",
  "load": {"users": 2, "iterations": 10},
  "flows": [{"name": "f", "steps": [{"url": "/"}]}]
}`

func TestParseMinimal(t *testing.T) {
	sc, err := Parse([]byte(minimal), ".", "")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "scenario" || len(sc.Flows) != 1 || len(sc.Steps) != 1 {
		t.Fatalf("got %+v", sc)
	}
	st := sc.Steps[0]
	if st.Method != "GET" || st.Name != "GET /" || st.Flow != "f" || sc.Flows[0].Weight != 1 {
		t.Errorf("defaults not applied: %+v", st)
	}
	if !sc.Cookies {
		t.Error("cookies should default to on")
	}
	if o := sc.Options(); o.Users != 2 || o.Iterations != 10 {
		t.Errorf("options = %+v", o)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, json, want string
	}{
		{"unknown key", `{"flows": [], "load": {"ramp": "10s"}}`, `unknown field "ramp"`},
		{"syntax error has a line", "{\n  \"flows\": [,]\n}", "line 2"},
		{"wrong type names field", `{"load": {"users": "ten"}}`, "load.users"},
		{"no flows", `{"base_url": "http://x"}`, "at least one flow"},
		{"flow without name", `{"base_url": "http://x", "flows": [{"steps": [{"url": "/"}]}]}`, "name is required"},
		{"duplicate flow", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/"}]}, {"name": "a", "steps": [{"url": "/"}]}]}`, "used twice"},
		{"duplicate step", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/"}, {"url": "/"}]}]}`, "share this name"},
		{"relative url without base", `{"flows": [{"name": "a", "steps": [{"url": "/x"}]}]}`, "no base_url"},
		{"missing as", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "capture": [{"json": "id"}]}]}]}`, `"as" is required`},
		{"two capture sources", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "capture": [{"as": "x", "json": "id", "header": "X"}]}]}]}`, "exactly one"},
		{"bad think range", `{"base_url": "http://x", "defaults": {"think": "2s-1s"}, "flows": [{"name": "a", "steps": [{"url": "/"}]}]}`, "defaults.think"},
		{"bad think", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "think": "soon"}]}]}`, "think"},
		{"undefined var", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/p/{{prodcut_id}}"}]}]}`, "{{prodcut_id}} is not set"},
		{"var captured later", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"name": "1", "url": "/{{id}}"}, {"name": "2", "url": "/", "capture": [{"json": "id", "as": "id"}]}]}]}`, "{{id}} is not set"},
		{"var from other flow", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "capture": [{"json": "id", "as": "id"}]}]}, {"name": "b", "steps": [{"url": "/{{id}}"}]}]}`, "{{id}} is not set"},
		{"two bodies", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "body": "x", "json": {}}]}]}`, "only one of"},
		{"bad status", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"status": 1000}}]}]}`, "not an HTTP status"},
		{"bad on_fail", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "on_fail": "retry"}]}]}`, "on_fail"},
		{"bad builtin", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/{{$nope}}"}]}]}`, "unknown built-in"},
		{"unclosed placeholder", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/{{id"}]}]}`, "unclosed"},
		{"bad json path", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "capture": [{"as": "x", "json": "items[a]"}]}]}]}`, "bad JSON path"},
		{"bad regex", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "capture": [{"as": "x", "regex": "("}]}]}]}`, "regex"},
		{"bad base_url", `{"base_url": "ftp://x", "flows": [{"name": "a", "steps": [{"url": "/"}]}]}`, "base_url"},
		{"unset env", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/{{$env PELT_SURELY_UNSET_VAR}}"}]}]}`, "not set"},
		{"unknown data mode", `{"base_url": "http://x", "data": {"file": "u.csv", "mode": "shuffle"}, "flows": [{"name": "a", "steps": [{"url": "/"}]}]}`, `want "unique", "sequential" or "random"`},
		{"empty body_contains", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"body_contains": ""}}]}]}`, "matches every body"},
		{"body_contains not text", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"body_contains": 5}}]}]}`, "string or a list"},
		{"body_contains unknown var", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"body_contains": "{{nope}}"}}]}]}`, "{{nope}} is not set"},
		{"check json bad path", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"json": {"a[": 1}}}]}]}`, "bad JSON path"},
		{"check json unknown var", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"json": {"id": "{{nope}}"}}}]}]}`, "{{nope}} is not set"},
		{"bad max_latency", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"max_latency": "fast"}}]}]}`, "max_latency"},
		{"unknown check key", `{"base_url": "http://x", "flows": [{"name": "a", "steps": [{"url": "/", "check": {"body": "x"}}]}]}`, "body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.json), t.TempDir(), "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestParseKnownVariables(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "users.csv"), []byte("username,password\nuser1,p1\nuser2,p2\n"), 0o644)
	sc, err := Parse([]byte(`{
	  "base_url": "http://x",
	  "vars": {"size": 20, "flag": true, "label": "a b"},
	  "data": {"file": "users.csv"},
	  "setup": [{"name": "login", "method": "post", "url": "/login",
	             "json": {"u": "{{username}}", "p": "{{password}}"},
	             "capture": [{"json": "token", "as": "token"}]}],
	  "flows": [{"name": "a", "steps": [
	    {"name": "list", "url": "/p?limit={{size}}", "headers": {"Authorization": "Bearer {{token}}"},
	     "capture": [{"json": "items[0].id", "as": "id"}]},
	    {"name": "one", "url": "/p/{{id}}", "body": "{{label}} {{flag}}"}
	  ]}]
	}`), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Vars["size"] != "20" || sc.Vars["flag"] != "true" {
		t.Errorf("vars = %v", sc.Vars)
	}
	if sc.Setup[0].Method != "POST" {
		t.Errorf("method not upper-cased: %s", sc.Setup[0].Method)
	}
	var ct string
	for _, h := range sc.Setup[0].Header {
		if h.Name == "Content-Type" {
			ct = h.Value.Source()
		}
	}
	if ct != "application/json" {
		t.Errorf("json body should set Content-Type, got %q", ct)
	}
	if len(sc.Data.Rows) != 2 || sc.Data.row(1)["username"] != "user2" {
		t.Errorf("data = %+v", sc.Data)
	}
}

func TestBaseOverride(t *testing.T) {
	sc, err := Parse([]byte(`{"flows": [{"name": "a", "steps": [{"url": "/x"}]}]}`), ".", "http://staging.example/")
	if err != nil {
		t.Fatal(err)
	}
	if sc.BaseURL != "http://staging.example" {
		t.Errorf("BaseURL = %q", sc.BaseURL)
	}
}

func TestDataTooFewRows(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "u.csv"), []byte("username\nuser1\n"), 0o644)
	sc, err := Parse([]byte(`{"base_url": "http://x", "data": {"file": "u.csv"}, "flows": [{"name": "a", "steps": [{"url": "/{{username}}"}]}]}`), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(sc, Options{Users: 2, Iterations: 1}); err == nil || !strings.Contains(err.Error(), "1 rows") {
		t.Errorf("err = %v, want too few rows", err)
	}
}

// fakeValues implements Values for template tests.
type fakeValues map[string]string

func (f fakeValues) Lookup(n string) (string, bool) { v, ok := f[n]; return v, ok }
func (fakeValues) VU() int                          { return 7 }
func (fakeValues) Iteration() int                   { return 3 }
func (fakeValues) RandIntN(n int) int               { return n - 1 }
func (fakeValues) UUID() string                     { return "uuid" }

func TestTemplates(t *testing.T) {
	t.Setenv("PELT_TEST_ENV", "from-env")
	vals := fakeValues{"id": "a/b c", "q": "x&y=z", "name": `say "hi"`, "base": "http://h:1/api"}
	cases := []struct {
		src  string
		mode escapeMode
		want string
	}{
		{"plain", escapeNone, "plain"},
		{"{{id}}-{{ id }}", escapeNone, "a/b c-a/b c"},
		{"vu {{$vu}} it {{$iteration}} r {{$rand 5 10}} u {{$uuid}}", escapeNone, "vu 7 it 3 r 10 u uuid"},
		{"{{$env PELT_TEST_ENV}}", escapeNone, "from-env"},
		{"/items/{{id}}?q={{q}}", escapeURL, "/items/a%2Fb%20c?q=x%26y%3Dz"},
		{"{{base}}/items/{{id}}", escapeURL, "http://h:1/api/items/a%2Fb%20c"},
		{`{"n":"{{name}}","id":"{{id}}"}`, escapeJSON, `{"n":"say \"hi\"","id":"a/b c"}`},
		{"{{missing}}", escapeNone, ""},
	}
	for _, c := range cases {
		tm, err := compileTemplate(c.src, c.mode)
		if err != nil {
			t.Errorf("compile %q: %v", c.src, err)
			continue
		}
		if got := tm.Render(vals); got != c.want {
			t.Errorf("render %q = %q, want %q", c.src, got, c.want)
		}
	}
	tm, _ := compileTemplate("{{$env PELT_TEST_ENV}}/x", escapeURL)
	if !tm.Static() {
		t.Error("an $env-only template should be static after compiling")
	}
}

func TestCaptures(t *testing.T) {
	r := &response{
		status: 201,
		header: http.Header{"Location": {"/orders/9"}},
		body:   []byte(`{"items":[{"id":5,"tags":["a","b"]},{"id":"six"}],"data":{"user":{"name":"Nam","ok":true,"none":null}}} trailing ignored`),
	}
	cases := []struct {
		kind, expr, want string
		fail             bool
	}{
		{"json", "items[0].id", "5", false},
		{"json", "items[1].id", "six", false},
		{"json", "items[0].tags[1]", "b", false},
		{"json", "items[0].tags", `["a","b"]`, false},
		{"json", "data.user.name", "Nam", false},
		{"json", "data.user.ok", "true", false},
		{"json", "data.user.none", "", true},
		{"json", "items[2].id", "", true},
		{"json", "nope", "", true},
		{"header", "Location", "/orders/9", false},
		{"header", "X-Missing", "", true},
		{"regex", `"name":"(\w+)"`, "Nam", false},
		{"regex", `tags`, "tags", false},
		{"regex", `zzz`, "", true},
		{"status", "status", "201", false},
	}
	for _, c := range cases {
		cp := &Capture{Kind: c.kind, Expr: c.expr, header: c.expr}
		var err error
		switch c.kind {
		case "json":
			cp.path, err = parsePath(c.expr)
		case "regex":
			cp.re, err = regexp.Compile(c.expr)
		}
		if err != nil {
			t.Fatalf("%s %s: %v", c.kind, c.expr, err)
		}
		got, err := cp.extract(r)
		if (err != nil) != c.fail || got != c.want {
			t.Errorf("%s %s = %q, %v; want %q (fail=%v)", c.kind, c.expr, got, err, c.want, c.fail)
		}
	}
	notJSON := &Capture{Kind: "json", Expr: "a"}
	notJSON.path, _ = parsePath("a")
	if _, err := notJSON.extract(&response{body: []byte("<html>")}); err == nil {
		t.Error("a JSON capture on HTML should fail")
	}
}

func TestParsePath(t *testing.T) {
	good := map[string]int{"a": 1, "a.b": 2, "items[0].id": 3, "[1]": 1, "m[0][2]": 3}
	for p, n := range good {
		got, err := parsePath(p)
		if err != nil || len(got) != n {
			t.Errorf("parsePath(%q) = %v, %v; want %d elements", p, got, err, n)
		}
	}
	for _, p := range []string{"", "a..b", "a[", "a[x]", "a[-1]", "a[0]b"} {
		if _, err := parsePath(p); err == nil {
			t.Errorf("parsePath(%q) should fail", p)
		}
	}
}

func TestParseThink(t *testing.T) {
	th, err := parseThink("500ms-2s")
	if err != nil || th.Min.Milliseconds() != 500 || th.Max.Milliseconds() != 2000 {
		t.Errorf("range = %+v, %v", th, err)
	}
	th, err = parseThink("1s")
	if err != nil || th.Min != th.Max {
		t.Errorf("fixed = %+v, %v", th, err)
	}
}

// TestWeightedPick checks that, with a fixed seed, 10,000 picks land within
// 2 percentage points of the weights.
func TestWeightedPick(t *testing.T) {
	sc, err := Parse([]byte(`{"base_url": "http://x", "load": {"iterations": 1}, "flows": [
	  {"name": "a", "weight": 7, "steps": [{"url": "/a"}]},
	  {"name": "b", "weight": 2, "steps": [{"url": "/b"}]},
	  {"name": "c", "steps": [{"url": "/c"}]}]}`), ".", "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(sc, sc.Options())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	counts := map[string]int{}
	const n = 10000
	for range n {
		counts[r.pick(rng.IntN(10)).Name]++
	}
	for name, w := range map[string]float64{"a": 0.7, "b": 0.2, "c": 0.1} {
		got := float64(counts[name]) / n
		if got < w-0.02 || got > w+0.02 {
			t.Errorf("flow %s picked %.3f of the time, want %.2f", name, got, w)
		}
	}
}
