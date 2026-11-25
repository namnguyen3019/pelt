package flow

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// DryRun runs setup once and then each flow once, in file order, as a
// single user with no think time, and prints every request, its response
// and what each capture and check found. It is for debugging a scenario
// file, not for measuring. It reports whether every step passed.
func DryRun(ctx context.Context, sc *Scenario, opts Options, w io.Writer) bool {
	opts.Users, opts.Iterations, opts.Duration = 1, 1, 0
	r, err := New(sc, opts)
	if err != nil {
		fmt.Fprintf(w, "error: %v\n", err)
		return false
	}
	v := r.newVU(1, newRecorder(sc))
	ok := true
	v.trace = func(t *StepTrace) {
		writeTrace(w, t)
		if t.Err != nil || t.Aborted || t.Failed() {
			ok = false
		}
	}

	name := sc.Name
	if sc.Path != "" {
		name = sc.Path
	}
	fmt.Fprintf(w, "Dry run of %s: 1 user, setup and then each flow once, no think time.\n", name)

	if len(sc.Setup) > 0 {
		fmt.Fprintf(w, "\nsetup\n")
		v.saveTo = v.setupVars
		if out, _ := v.runSteps(ctx, ctx, sc.Setup, false); out != finished {
			fmt.Fprintf(w, "\nSetup did not finish, so the flows were not run.\n")
			return false
		}
	}
	for _, fl := range sc.Flows {
		if ctx.Err() != nil {
			break
		}
		v.iteration++
		fmt.Fprintf(w, "\n%s (weight %d)\n", fl.Name, fl.Weight)
		if out := v.runFlow(ctx, ctx, fl, false); out == failed {
			fmt.Fprintf(w, "  (rest of %s skipped after the failure)\n", fl.Name)
		}
	}
	if ok {
		fmt.Fprintf(w, "\nAll steps passed.\n")
	} else {
		fmt.Fprintf(w, "\nSome steps failed; see above.\n")
	}
	return ok
}

const dryBodyMax = 300

func writeTrace(w io.Writer, t *StepTrace) {
	fmt.Fprintf(w, "  %s\n", t.Step.Name)
	fmt.Fprintf(w, "    %s %s\n", t.Method, t.URL)
	for _, name := range sortedKeys(t.Header) {
		fmt.Fprintf(w, "    %s: %s\n", name, strings.Join(t.Header[name], ", "))
	}
	if t.Body != "" {
		fmt.Fprintf(w, "    body %s\n", clip(t.Body, dryBodyMax))
	}
	switch {
	case t.Aborted:
		fmt.Fprintf(w, "    -> interrupted\n")
		return
	case t.Err != nil:
		fmt.Fprintf(w, "    -> error: %v\n", t.Err)
		return
	}
	fmt.Fprintf(w, "    -> %d %s in %s, %d bytes\n", t.Status, http.StatusText(t.Status), t.Latency.Round(10*time.Microsecond), t.Bytes)
	if len(t.RespBody) > 0 {
		fmt.Fprintf(w, "    response %s\n", clip(string(t.RespBody), dryBodyMax))
	}
	if len(t.Step.Status) > 0 {
		if t.CheckErr != nil {
			fmt.Fprintf(w, "    check FAILED: %v\n", t.CheckErr)
		} else {
			fmt.Fprintf(w, "    check ok: status %s\n", statusWant(t.Step.Status))
		}
	}
	for _, c := range t.Captures {
		if c.Err != nil {
			fmt.Fprintf(w, "    capture %s FAILED: %v\n", c.As, c.Err)
		} else {
			fmt.Fprintf(w, "    capture %s = %q\n", c.As, clip(c.Value, 80))
		}
	}
}

func clip(s string, n int) string {
	s = strings.TrimRight(s, "\r\n")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("... (%d more bytes)", len(s)-cut)
}
