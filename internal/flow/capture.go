package flow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Capture pulls one value out of a response and saves it under As.
type Capture struct {
	As     string
	Kind   string // "json", "header", "regex" or "status"
	Expr   string // the path, header name or pattern as written
	path   []pathElem
	re     *regexp.Regexp
	header string
}

// pathElem is one step of a JSON dot path: a key, or an index if key is "".
type pathElem struct {
	key   string
	index int
	isIdx bool
}

// parsePath parses a dot path such as items[0].id or data.user.name.
func parsePath(s string) ([]pathElem, error) {
	bad := func(why string) ([]pathElem, error) {
		return nil, fmt.Errorf("bad JSON path %q: %s", s, why)
	}
	if s == "" {
		return nil, fmt.Errorf("empty JSON path")
	}
	var out []pathElem
	for _, seg := range strings.Split(s, ".") {
		key, rest, hasIdx := strings.Cut(seg, "[")
		if key == "" && !hasIdx {
			return bad("empty segment")
		}
		if key != "" {
			out = append(out, pathElem{key: key})
		}
		for hasIdx {
			idx, after, ok := strings.Cut(rest, "]")
			n, err := strconv.Atoi(idx)
			if !ok || err != nil || n < 0 {
				return bad("indexes look like [0]")
			}
			out = append(out, pathElem{index: n, isIdx: true})
			if after == "" {
				break
			}
			if !strings.HasPrefix(after, "[") {
				return bad("unexpected text after ]")
			}
			rest = after[1:]
		}
	}
	return out, nil
}

// walkPath follows path through a value decoded with UseNumber.
func walkPath(v any, path []pathElem) (any, bool) {
	for _, p := range path {
		if p.isIdx {
			a, ok := v.([]any)
			if !ok || p.index >= len(a) {
				return nil, false
			}
			v = a[p.index]
		} else {
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[p.key]; !ok {
				return nil, false
			}
		}
	}
	return v, true
}

// jsonValueString turns a found JSON value into the text a placeholder
// inserts. Strings come out unquoted; objects and arrays as compact JSON.
func jsonValueString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	default:
		b, err := json.Marshal(x)
		return string(b), err == nil
	}
}

// needsBody reports whether the capture reads the response body.
func (c *Capture) needsBody() bool { return c.Kind == "json" || c.Kind == "regex" }

// response is what captures and checks look at.
type response struct {
	status int
	header http.Header
	body   []byte // only read when a capture or the dry run needs it

	decoded    any
	decodeDone bool
	decodeErr  error
}

func (r *response) json() (any, error) {
	if !r.decodeDone {
		r.decodeDone = true
		dec := json.NewDecoder(bytes.NewReader(r.body))
		dec.UseNumber()
		r.decodeErr = dec.Decode(&r.decoded)
	}
	return r.decoded, r.decodeErr
}

// extract runs the capture. The error says why nothing was found.
func (c *Capture) extract(r *response) (string, error) {
	switch c.Kind {
	case "status":
		return strconv.Itoa(r.status), nil
	case "header":
		if v := r.header.Get(c.header); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("no %s header", c.header)
	case "regex":
		m := c.re.FindSubmatch(r.body)
		if m == nil {
			return "", fmt.Errorf("regex %q did not match", c.Expr)
		}
		if len(m) > 1 {
			return string(m[1]), nil
		}
		return string(m[0]), nil
	case "json":
		doc, err := r.json()
		if err != nil {
			return "", fmt.Errorf("body is not JSON")
		}
		v, ok := walkPath(doc, c.path)
		if !ok {
			return "", fmt.Errorf("JSON path %s not found", c.Expr)
		}
		s, ok := jsonValueString(v)
		if !ok {
			return "", fmt.Errorf("JSON path %s is null", c.Expr)
		}
		return s, nil
	}
	return "", fmt.Errorf("unknown capture kind %q", c.Kind)
}
