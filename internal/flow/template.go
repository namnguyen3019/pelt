package flow

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// A Template is a string with {{name}} placeholders, parsed once when the
// scenario loads so that rendering is a plain loop with no parsing.
//
// Placeholders are either variables ({{token}}) or built-ins starting with
// $: {{$vu}}, {{$iteration}}, {{$uuid}}, {{$rand 1 100}} and {{$env NAME}}.
// {{$env NAME}} is read once at load time and becomes literal text.
type Template struct {
	src   string
	parts []part
}

type part struct {
	lit string // used when ref is nil
	ref *ref
}

type refKind int

const (
	refVar refKind = iota
	refVU
	refIteration
	refUUID
	refRand
)

type ref struct {
	kind   refKind
	name   string // refVar
	lo, hi int    // refRand, inclusive
	escape func(string) string
}

// Values supplies what placeholders render to.
type Values interface {
	Lookup(name string) (string, bool)
	VU() int
	Iteration() int
	RandIntN(n int) int
	UUID() string
}

var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// escapeMode chooses how inserted values are escaped.
type escapeMode int

const (
	escapeNone escapeMode = iota // header values, plain bodies
	escapeURL                    // path-escaped before '?', query-escaped after
	escapeJSON                   // inside JSON string literals
)

// compileTemplate parses s. Every placeholder value gets escaped per mode.
func compileTemplate(s string, mode escapeMode) (*Template, error) {
	t := &Template{src: s}
	rest := s
	inQuery := false
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			t.addLit(rest)
			break
		}
		lit := rest[:i]
		t.addLit(lit)
		if strings.Contains(lit, "?") {
			inQuery = true
		}
		end := strings.Index(rest[i+2:], "}}")
		if end < 0 {
			return nil, fmt.Errorf("unclosed {{ in %q", s)
		}
		inner := strings.TrimSpace(rest[i+2 : i+2+end])
		r, lit2, err := parseRef(inner)
		if err != nil {
			return nil, err
		}
		if r == nil {
			t.addLit(lit2) // $env, resolved now
		} else {
			switch mode {
			case escapeURL:
				switch {
				case len(t.parts) == 0:
					// A placeholder at the very start of a URL is inserted as
					// is, so it can hold a whole address or base path.
				case inQuery:
					r.escape = url.QueryEscape
				default:
					r.escape = url.PathEscape
				}
			case escapeJSON:
				r.escape = jsonEscape
			}
			t.parts = append(t.parts, part{ref: r})
		}
		rest = rest[i+2+end+2:]
	}
	return t, nil
}

func (t *Template) addLit(s string) {
	if s == "" {
		return
	}
	if n := len(t.parts); n > 0 && t.parts[n-1].ref == nil {
		t.parts[n-1].lit += s
		return
	}
	t.parts = append(t.parts, part{lit: s})
}

// parseRef parses the inside of {{...}}. For $env it returns the value as
// literal text and a nil ref.
func parseRef(inner string) (*ref, string, error) {
	if !strings.HasPrefix(inner, "$") {
		if !varName.MatchString(inner) {
			return nil, "", fmt.Errorf("{{%s}} is not a valid variable name", inner)
		}
		return &ref{kind: refVar, name: inner}, "", nil
	}
	f := strings.Fields(inner)
	switch f[0] {
	case "$vu", "$iteration", "$uuid":
		if len(f) != 1 {
			return nil, "", fmt.Errorf("{{%s}} takes no arguments", f[0])
		}
		return &ref{kind: map[string]refKind{"$vu": refVU, "$iteration": refIteration, "$uuid": refUUID}[f[0]]}, "", nil
	case "$rand":
		if len(f) != 3 {
			return nil, "", fmt.Errorf("{{$rand}} needs two numbers, like {{$rand 1 100}}")
		}
		lo, err1 := strconv.Atoi(f[1])
		hi, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || hi < lo {
			return nil, "", fmt.Errorf("{{%s}}: want two whole numbers, low then high", inner)
		}
		return &ref{kind: refRand, lo: lo, hi: hi}, "", nil
	case "$env":
		if len(f) != 2 {
			return nil, "", fmt.Errorf("{{$env}} needs a variable name, like {{$env API_KEY}}")
		}
		v, ok := os.LookupEnv(f[1])
		if !ok {
			return nil, "", fmt.Errorf("{{$env %s}}: environment variable %s is not set", f[1], f[1])
		}
		return nil, v, nil
	default:
		return nil, "", fmt.Errorf("unknown built-in {{%s}} (have $vu, $iteration, $uuid, $rand, $env)", f[0])
	}
}

// Vars lists the variable names the template refers to.
func (t *Template) Vars() []string {
	var out []string
	for _, p := range t.parts {
		if p.ref != nil && p.ref.kind == refVar {
			out = append(out, p.ref.name)
		}
	}
	return out
}

// Static reports whether the template has no placeholders left.
func (t *Template) Static() bool {
	return len(t.parts) == 0 || (len(t.parts) == 1 && t.parts[0].ref == nil)
}

// Source returns the template text as written.
func (t *Template) Source() string { return t.src }

// Render fills in the placeholders. A variable with no value renders as
// an empty string; the loader makes sure every variable is set somewhere.
func (t *Template) Render(v Values) string {
	if t == nil {
		return ""
	}
	if t.Static() {
		if len(t.parts) == 0 {
			return ""
		}
		return t.parts[0].lit
	}
	var b strings.Builder
	for _, p := range t.parts {
		if p.ref == nil {
			b.WriteString(p.lit)
			continue
		}
		var s string
		switch p.ref.kind {
		case refVar:
			s, _ = v.Lookup(p.ref.name)
		case refVU:
			s = strconv.Itoa(v.VU())
		case refIteration:
			s = strconv.Itoa(v.Iteration())
		case refUUID:
			s = v.UUID()
		case refRand:
			s = strconv.Itoa(p.ref.lo + v.RandIntN(p.ref.hi-p.ref.lo+1))
		}
		if p.ref.escape != nil {
			s = p.ref.escape(s)
		}
		b.WriteString(s)
	}
	return b.String()
}

// jsonEscape escapes s for use inside a JSON string literal (without the
// surrounding quotes).
func jsonEscape(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s) // cannot fail for a string
	out := b.String()
	return out[1 : len(out)-2] // drop quotes and the trailing newline
}
