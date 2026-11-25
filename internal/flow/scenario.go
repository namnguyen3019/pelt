// Package flow is Pelt's flow mode: scripted user journeys read from a JSON
// scenario file and run by virtual users (VUs), each with its own cookie jar
// and variables, sharing Pelt's tuned connection pool.
//
// The package splits into the file model and loader (this file), templates
// (template.go), captures (capture.go), the CSV feed (data.go), the engine
// (runner.go, vu.go) and the raw results (result.go). Rendering the results
// lives in internal/report.
package flow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Scenario is a loaded, checked and compiled scenario file.
type Scenario struct {
	Name    string
	Path    string
	BaseURL string // may be empty if every step URL is absolute
	Load    Load
	Cookies bool
	Vars    map[string]string
	Data    *Data // nil without a data block
	Setup   []*Step
	Flows   []*Flow
	Steps   []*Step // every step, setup first; Steps[i].ID == i
}

// Load holds the run settings from the file's load block. Command-line
// flags override them.
type Load struct {
	Users      int
	Duration   time.Duration
	Iterations int
	Timeout    time.Duration
}

// Flow is a weighted list of steps run in order.
type Flow struct {
	ID     int
	Name   string
	Weight int
	Steps  []*Step
}

// Step is one request.
type Step struct {
	ID     int
	Flow   string // flow name, or "setup"
	Name   string
	Method string
	URL    *Template
	Header []HeaderTemplate
	Body   *Template // nil for no body
	Status []int     // allowed status codes; empty means no status check
	Capt   []*Capture
	Think  Think
	// ContinueOnFail keeps the iteration going after a failed check or
	// capture on this step instead of aborting it.
	ContinueOnFail bool
	needsBody      bool
}

// HeaderTemplate is one request header whose value may hold placeholders.
type HeaderTemplate struct {
	Name  string
	Value *Template
}

// Think is a pause after a step: fixed when Min == Max, else uniform.
type Think struct{ Min, Max time.Duration }

// --- file model ---

type fileScenario struct {
	Name     string           `json:"name"`
	BaseURL  string           `json:"base_url"`
	Load     fileLoad         `json:"load"`
	Defaults fileDefaults     `json:"defaults"`
	Vars     map[string]value `json:"vars"`
	Data     *fileData        `json:"data"`
	Setup    []fileStep       `json:"setup"`
	Flows    []fileFlow       `json:"flows"`
}

type fileLoad struct {
	Users      int    `json:"users"`
	Duration   string `json:"duration"`
	Iterations int    `json:"iterations"`
	Timeout    string `json:"timeout"`
}

type fileDefaults struct {
	Headers map[string]string `json:"headers"`
	Think   string            `json:"think"`
	Cookies *bool             `json:"cookies"`
}

type fileData struct {
	File string `json:"file"`
	Mode string `json:"mode"`
}

type fileFlow struct {
	Name   string     `json:"name"`
	Weight int        `json:"weight"`
	Steps  []fileStep `json:"steps"`
}

type fileStep struct {
	Name     string            `json:"name"`
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Body     *string           `json:"body"`
	BodyFile string            `json:"body_file"`
	JSON     json.RawMessage   `json:"json"`
	Check    *fileCheck        `json:"check"`
	Capture  []fileCapture     `json:"capture"`
	Think    *string           `json:"think"`
	OnFail   string            `json:"on_fail"`
}

type fileCheck struct {
	Status statusList `json:"status"`
}

type fileCapture struct {
	As     string `json:"as"`
	JSON   string `json:"json"`
	Header string `json:"header"`
	Regex  string `json:"regex"`
	Status bool   `json:"status"`
}

// statusList accepts 200 or [200, 201].
type statusList []int

func (s *statusList) UnmarshalJSON(b []byte) error {
	var one int
	if err := json.Unmarshal(b, &one); err == nil {
		*s = statusList{one}
		return nil
	}
	var many []int
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("check.status must be a status code or a list of them")
	}
	*s = many
	return nil
}

// value is a var given as a string, number or boolean.
type value string

func (v *value) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*v = value(s)
		return nil
	}
	b = bytes.TrimSpace(b)
	if len(b) > 0 && (b[0] == '{' || b[0] == '[' || bytes.Equal(b, []byte("null"))) {
		return errors.New("vars values must be strings, numbers or booleans")
	}
	*v = value(b)
	return nil
}

// LoadFile reads, checks and compiles a scenario file. A non-empty baseURL
// replaces the file's base_url (the -base flag).
func LoadFile(path, baseURL string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sc, err := Parse(raw, filepath.Dir(path), baseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	sc.Path = path
	return sc, nil
}

// Parse checks and compiles a scenario. dir resolves relative data and
// body_file paths; a non-empty baseURL replaces the file's base_url.
func Parse(raw []byte, dir, baseURL string) (*Scenario, error) {
	var f fileScenario
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, describeJSONError(raw, err)
	}
	if dec.More() {
		return nil, errors.New("extra data after the scenario object")
	}
	if baseURL != "" {
		f.BaseURL = baseURL
	}
	return compile(&f, dir)
}

// describeJSONError adds a line and column to decoding errors when the
// decoder gives an offset.
func describeJSONError(raw []byte, err error) error {
	var off int64 = -1
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		off = se.Offset
	case errors.As(err, &te):
		off = te.Offset
		if te.Field != "" {
			err = fmt.Errorf("field %s: want %s, got %s", te.Field, te.Type, te.Value)
		}
	}
	msg := strings.TrimPrefix(err.Error(), "json: ")
	if off >= 0 && off <= int64(len(raw)) {
		line := 1 + bytes.Count(raw[:off], []byte("\n"))
		col := int(off) - bytes.LastIndexByte(raw[:off], '\n')
		return fmt.Errorf("line %d, column %d: %s", line, col, msg)
	}
	return errors.New(msg)
}

func compile(f *fileScenario, dir string) (*Scenario, error) {
	sc := &Scenario{
		Name:    f.Name,
		BaseURL: strings.TrimRight(f.BaseURL, "/"),
		Cookies: f.Defaults.Cookies == nil || *f.Defaults.Cookies,
		Vars:    make(map[string]string, len(f.Vars)),
	}
	if sc.Name == "" {
		sc.Name = "scenario"
	}
	if sc.BaseURL != "" {
		if err := checkHTTPURL(sc.BaseURL); err != nil {
			return nil, fmt.Errorf("base_url: %w", err)
		}
	}
	for k, v := range f.Vars {
		if !varName.MatchString(k) {
			return nil, fmt.Errorf("vars: %q is not a valid variable name", k)
		}
		sc.Vars[k] = string(v)
	}

	// load
	var err error
	sc.Load.Users = f.Load.Users
	sc.Load.Iterations = f.Load.Iterations
	if sc.Load.Users < 0 || sc.Load.Iterations < 0 {
		return nil, errors.New("load: users and iterations cannot be negative")
	}
	if sc.Load.Duration, err = parseDuration(f.Load.Duration); err != nil {
		return nil, fmt.Errorf("load.duration: %w", err)
	}
	if sc.Load.Timeout, err = parseDuration(f.Load.Timeout); err != nil {
		return nil, fmt.Errorf("load.timeout: %w", err)
	}

	// data
	if f.Data != nil {
		if f.Data.File == "" {
			return nil, errors.New("data.file is required")
		}
		switch f.Data.Mode {
		case "", "unique":
		case "sequential", "random":
			return nil, fmt.Errorf("data.mode %q is not supported yet; only \"unique\" is", f.Data.Mode)
		default:
			return nil, fmt.Errorf("data.mode %q: want \"unique\"", f.Data.Mode)
		}
		p := f.Data.File
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if sc.Data, err = readCSV(p); err != nil {
			return nil, fmt.Errorf("data.file: %w", err)
		}
	}

	defThink := Think{}
	if f.Defaults.Think != "" {
		if defThink, err = parseThink(f.Defaults.Think); err != nil {
			return nil, fmt.Errorf("defaults.think: %w", err)
		}
	}

	// Names every step can use: vars and data columns.
	base := map[string]bool{}
	for k := range sc.Vars {
		base[k] = true
	}
	if sc.Data != nil {
		for _, c := range sc.Data.Columns {
			base[c] = true
		}
	}

	c := &compiler{sc: sc, dir: dir, defHeaders: f.Defaults.Headers, defThink: defThink}

	// Setup captures are visible to every flow.
	known := copySet(base)
	for i := range f.Setup {
		st, err := c.step(&f.Setup[i], "setup", fmt.Sprintf("setup[%d]", i), known)
		if err != nil {
			return nil, err
		}
		sc.Setup = append(sc.Setup, st)
	}
	afterSetup := known

	if len(f.Flows) == 0 {
		return nil, errors.New("flows: need at least one flow")
	}
	flowNames := map[string]bool{"setup": true}
	for i, ff := range f.Flows {
		where := fmt.Sprintf("flows[%d]", i)
		if ff.Name == "" {
			return nil, fmt.Errorf("%s: name is required", where)
		}
		if flowNames[ff.Name] {
			return nil, fmt.Errorf("%s: flow name %q is used twice (or is reserved)", where, ff.Name)
		}
		flowNames[ff.Name] = true
		if ff.Weight < 0 {
			return nil, fmt.Errorf("%s (%s): weight cannot be negative", where, ff.Name)
		}
		if len(ff.Steps) == 0 {
			return nil, fmt.Errorf("%s (%s): needs at least one step", where, ff.Name)
		}
		fl := &Flow{ID: i, Name: ff.Name, Weight: max(ff.Weight, 1)}
		known := copySet(afterSetup)
		for j := range ff.Steps {
			st, err := c.step(&ff.Steps[j], ff.Name, fmt.Sprintf("%s.steps[%d]", where, j), known)
			if err != nil {
				return nil, err
			}
			fl.Steps = append(fl.Steps, st)
		}
		sc.Flows = append(sc.Flows, fl)
	}
	return sc, nil
}

type compiler struct {
	sc         *Scenario
	dir        string
	defHeaders map[string]string
	defThink   Think
	names      map[string]bool // flow/step keys already used
}

// step compiles one step. known holds the variable names set so far on this
// path (vars, data columns, setup captures, earlier captures in the flow);
// the step's own captures are added to it.
func (c *compiler) step(fs *fileStep, flowName, where string, known map[string]bool) (*Step, error) {
	st := &Step{ID: len(c.sc.Steps), Flow: flowName, Method: strings.ToUpper(fs.Method)}
	if st.Method == "" {
		st.Method = http.MethodGet
	}
	if fs.URL == "" {
		return nil, fmt.Errorf("%s: url is required", where)
	}
	st.Name = fs.Name
	if st.Name == "" {
		st.Name = st.Method + " " + fs.URL
	}
	where = fmt.Sprintf("%s (%s)", where, st.Name)

	if c.names == nil {
		c.names = map[string]bool{}
	}
	key := flowName + "\x00" + st.Name
	if c.names[key] {
		return nil, fmt.Errorf("%s: two steps in %s share this name; give each a distinct name", where, flowName)
	}
	c.names[key] = true

	var err error
	used := map[string]bool{}
	note := func(t *Template) {
		for _, v := range t.Vars() {
			used[v] = true
		}
	}

	if st.URL, err = compileTemplate(fs.URL, escapeURL); err != nil {
		return nil, fmt.Errorf("%s: url: %w", where, err)
	}
	note(st.URL)
	if c.sc.BaseURL == "" && len(st.URL.parts) > 0 && st.URL.parts[0].ref == nil && !isAbsolute(st.URL.parts[0].lit) {
		return nil, fmt.Errorf("%s: url %q is relative but the scenario has no base_url", where, fs.URL)
	}

	// Headers: defaults first, step headers override by name.
	hdr := map[string]string{}
	for k, v := range c.defHeaders {
		hdr[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range fs.Headers {
		hdr[http.CanonicalHeaderKey(k)] = v
	}

	bodies := 0
	if fs.Body != nil {
		bodies++
	}
	if fs.BodyFile != "" {
		bodies++
	}
	if len(fs.JSON) > 0 {
		bodies++
	}
	if bodies > 1 {
		return nil, fmt.Errorf("%s: use only one of body, body_file and json", where)
	}
	switch {
	case fs.Body != nil:
		st.Body, err = compileTemplate(*fs.Body, escapeNone)
	case fs.BodyFile != "":
		p := fs.BodyFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.dir, p)
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil, fmt.Errorf("%s: body_file: %w", where, rerr)
		}
		st.Body, err = compileTemplate(string(b), escapeNone)
	case len(fs.JSON) > 0:
		var buf bytes.Buffer
		if cerr := json.Compact(&buf, fs.JSON); cerr != nil {
			return nil, fmt.Errorf("%s: json: %w", where, cerr)
		}
		st.Body, err = compileTemplate(buf.String(), escapeJSON)
		if _, ok := hdr["Content-Type"]; !ok {
			hdr["Content-Type"] = "application/json"
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: body: %w", where, err)
	}
	if st.Body != nil {
		note(st.Body)
	}

	for _, k := range sortedKeys(hdr) {
		t, err := compileTemplate(hdr[k], escapeNone)
		if err != nil {
			return nil, fmt.Errorf("%s: header %s: %w", where, k, err)
		}
		note(t)
		st.Header = append(st.Header, HeaderTemplate{Name: k, Value: t})
	}

	// Every variable must be set before this step runs.
	for _, v := range sortedKeys(used) {
		if !known[v] {
			return nil, fmt.Errorf("%s: {{%s}} is not set by vars, the data file, or a capture in setup or an earlier step", where, v)
		}
	}

	if fs.Check != nil {
		for _, code := range fs.Check.Status {
			if code < 100 || code > 599 {
				return nil, fmt.Errorf("%s: check.status %d is not an HTTP status code", where, code)
			}
		}
		st.Status = fs.Check.Status
	}

	for i, fc := range fs.Capture {
		cw := fmt.Sprintf("%s: capture[%d]", where, i)
		if fc.As == "" {
			return nil, fmt.Errorf("%s: \"as\" is required", cw)
		}
		if !varName.MatchString(fc.As) {
			return nil, fmt.Errorf("%s: %q is not a valid variable name", cw, fc.As)
		}
		cp := &Capture{As: fc.As}
		n := 0
		if fc.JSON != "" {
			n++
			cp.Kind, cp.Expr = "json", fc.JSON
			if cp.path, err = parsePath(fc.JSON); err != nil {
				return nil, fmt.Errorf("%s: %w", cw, err)
			}
		}
		if fc.Header != "" {
			n++
			cp.Kind, cp.Expr, cp.header = "header", fc.Header, fc.Header
		}
		if fc.Regex != "" {
			n++
			cp.Kind, cp.Expr = "regex", fc.Regex
			if cp.re, err = regexp.Compile(fc.Regex); err != nil {
				return nil, fmt.Errorf("%s: regex: %w", cw, err)
			}
		}
		if fc.Status {
			n++
			cp.Kind, cp.Expr = "status", "status"
		}
		if n != 1 {
			return nil, fmt.Errorf("%s: give exactly one of json, header, regex or status", cw)
		}
		if cp.needsBody() {
			st.needsBody = true
		}
		st.Capt = append(st.Capt, cp)
		known[fc.As] = true
	}

	st.Think = c.defThink
	if fs.Think != nil {
		if st.Think, err = parseThink(*fs.Think); err != nil {
			return nil, fmt.Errorf("%s: think: %w", where, err)
		}
	}

	switch fs.OnFail {
	case "", "abort":
	case "continue":
		st.ContinueOnFail = true
	default:
		return nil, fmt.Errorf("%s: on_fail %q: want \"abort\" or \"continue\"", where, fs.OnFail)
	}

	c.sc.Steps = append(c.sc.Steps, st)
	return st, nil
}

// parseThink parses "1s", "0s" or a range "500ms-2s".
func parseThink(s string) (Think, error) {
	lo, hi, isRange := strings.Cut(strings.TrimSpace(s), "-")
	a, err := time.ParseDuration(strings.TrimSpace(lo))
	if err != nil {
		return Think{}, fmt.Errorf("%q: want a duration like 1s or a range like 500ms-2s", s)
	}
	b := a
	if isRange {
		if b, err = time.ParseDuration(strings.TrimSpace(hi)); err != nil {
			return Think{}, fmt.Errorf("%q: want a range like 500ms-2s", s)
		}
	}
	if a < 0 || b < a {
		return Think{}, fmt.Errorf("%q: durations must be positive, low then high", s)
	}
	return Think{Min: a, Max: b}, nil
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q is not a duration like 30s or 2m", s)
	}
	return d, nil
}

func isAbsolute(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func checkHTTPURL(u string) error {
	if !isAbsolute(u) || len(strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")) == 0 {
		return fmt.Errorf("%q must start with http:// or https:// and name a host", u)
	}
	return nil
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
