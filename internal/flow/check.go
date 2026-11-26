package flow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Checks are what a step's response must satisfy, beyond the status code.
// Each is optional; a step fails when any of them fails.
type Checks struct {
	// BodyContains are texts the body must contain, each rendered with the
	// user's values first.
	BodyContains []*Template
	// JSON are values that paths in a JSON body must equal.
	JSON []*JSONCheck
	// MaxLatency fails the step when the response took longer; 0 is off.
	MaxLatency time.Duration
}

// JSONCheck is one "path": value entry of check.json.
type JSONCheck struct {
	Path string // as written, e.g. items[0].status
	path []pathElem
	// want is the expected value, decoded with UseNumber. When the
	// expected value is a string with placeholders, text holds it instead
	// and the found value's text form must match the rendered string.
	want any
	text *Template
}

func (c *Checks) needsBody() bool { return len(c.BodyContains) > 0 || len(c.JSON) > 0 }

func (c *Checks) empty() bool { return !c.needsBody() && c.MaxLatency == 0 }

// stringList accepts "text" or ["a", "b"].
type stringList []string

func (s *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = stringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("check.body_contains must be a string or a list of strings")
	}
	*s = many
	return nil
}

// compileChecks compiles the body, JSON and latency checks of fc. note
// records the variables the checks use.
func compileChecks(fc *fileCheck, note func(*Template)) (Checks, error) {
	var c Checks
	for _, s := range fc.BodyContains {
		if s == "" {
			return c, errors.New("check.body_contains: empty text matches every body")
		}
		t, err := compileTemplate(s, escapeNone)
		if err != nil {
			return c, fmt.Errorf("check.body_contains: %w", err)
		}
		note(t)
		c.BodyContains = append(c.BodyContains, t)
	}
	for _, p := range sortedKeys(fc.JSON) {
		jc := &JSONCheck{Path: p}
		var err error
		if jc.path, err = parsePath(p); err != nil {
			return c, fmt.Errorf("check.json: %w", err)
		}
		dec := json.NewDecoder(bytes.NewReader(fc.JSON[p]))
		dec.UseNumber()
		if err := dec.Decode(&jc.want); err != nil {
			return c, fmt.Errorf("check.json %q: %w", p, err)
		}
		if s, ok := jc.want.(string); ok && strings.Contains(s, "{{") {
			if jc.text, err = compileTemplate(s, escapeNone); err != nil {
				return c, fmt.Errorf("check.json %q: %w", p, err)
			}
			if jc.text.Static() { // only $env placeholders: a plain string now
				jc.want, jc.text = jc.text.Render(nil), nil
			} else {
				note(jc.text)
			}
		}
		c.JSON = append(c.JSON, jc)
	}
	if fc.MaxLatency != "" {
		d, err := time.ParseDuration(fc.MaxLatency)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("check.max_latency %q: want a duration like 500ms", fc.MaxLatency)
		}
		c.MaxLatency = d
	}
	return c, nil
}

// run evaluates the checks against a response that took latency, and
// returns why each failed one failed.
func (c *Checks) run(r *response, latency time.Duration, v Values) []string {
	var fails []string
	if c.MaxLatency > 0 && latency > c.MaxLatency {
		fails = append(fails, fmt.Sprintf("took %s, max %s", latency.Round(time.Microsecond), c.MaxLatency))
	}
	for _, t := range c.BodyContains {
		s := t.Render(v)
		if !bytes.Contains(r.body, []byte(s)) {
			fails = append(fails, fmt.Sprintf("body does not contain %q", clip(s, 60)))
		}
	}
	if len(c.JSON) == 0 {
		return fails
	}
	doc, err := r.json()
	if err != nil {
		return append(fails, "body is not JSON")
	}
	for _, jc := range c.JSON {
		got, ok := walkPath(doc, jc.path)
		if !ok {
			fails = append(fails, fmt.Sprintf("JSON path %s not found", jc.Path))
			continue
		}
		if jc.text != nil {
			want := jc.text.Render(v)
			if s, ok := jsonValueString(got); !ok || s != want {
				fails = append(fails, fmt.Sprintf("%s is %s, want %q", jc.Path, compact(got), want))
			}
			continue
		}
		if !jsonEqual(got, jc.want) {
			fails = append(fails, fmt.Sprintf("%s is %s, want %s", jc.Path, compact(got), compact(jc.want)))
		}
	}
	return fails
}

// describe says what the checks look for, for the dry run.
func (c *Checks) describe() []string {
	var out []string
	if c.MaxLatency > 0 {
		out = append(out, "max latency "+c.MaxLatency.String())
	}
	for _, t := range c.BodyContains {
		out = append(out, fmt.Sprintf("body contains %q", clip(t.Source(), 60)))
	}
	for _, jc := range c.JSON {
		if jc.text != nil {
			out = append(out, fmt.Sprintf("%s = %q", jc.Path, jc.text.Source()))
		} else {
			out = append(out, fmt.Sprintf("%s = %s", jc.Path, compact(jc.want)))
		}
	}
	return out
}

// jsonEqual compares two values decoded with UseNumber. Numbers compare by
// value, so 1, 1.0 and 1e0 are equal.
func jsonEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		rx, ok1 := new(big.Rat).SetString(x.String())
		ry, ok2 := new(big.Rat).SetString(y.String())
		if !ok1 || !ok2 {
			return x == y
		}
		return rx.Cmp(ry) == 0
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !jsonEqual(xv, yv) {
				return false
			}
		}
		return true
	default: // string, bool, nil
		return a == b
	}
}

// compact writes a decoded JSON value back as short JSON for messages.
func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return clip(string(b), 60)
}
