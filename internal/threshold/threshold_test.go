package threshold

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in     string
		scope  string
		metric string
		kind   Kind
		op     Op
		value  float64
		unit   Unit
		str    string
	}{
		{"p95<300ms", "", "p95", Latency, LT, 300, UnitMS, "p95 < 300ms"},
		{"  p95 < 300ms ", "", "p95", Latency, LT, 300, UnitMS, "p95 < 300ms"},
		{"p99<1.5s", "", "p99", Latency, LT, 1500, UnitMS, "p99 < 1.5s"},
		{"p99.9<=250", "", "p99.9", Latency, LE, 250, UnitMS, "p99.9 <= 250ms"},
		{"P50<500us", "", "p50", Latency, LT, 0.5, UnitMS, "p50 < 500us"},
		{"p100<2s", "", "p100", Latency, LT, 2000, UnitMS, "p100 < 2s"},
		{"mean<100ms", "", "mean", Latency, LT, 100, UnitMS, "mean < 100ms"},
		{"avg<100ms", "", "mean", Latency, LT, 100, UnitMS, "mean < 100ms"},
		{"max<=2s", "", "max", Latency, LE, 2000, UnitMS, "max <= 2s"},
		{"errors<1%", "", "errors", Rate, LT, 1, UnitPercent, "errors < 1%"},
		{"errors <= 0.5%", "", "errors", Rate, LE, 0.5, UnitPercent, "errors <= 0.5%"},
		{"errors<5", "", "errors", Rate, LT, 5, UnitCount, "errors < 5"},
		{"status:5xx<0.1%", "", "status:5xx", Rate, LT, 0.1, UnitPercent, "status:5xx < 0.1%"},
		{"status:503<=10", "", "status:503", Rate, LE, 10, UnitCount, "status:503 <= 10"},
		{"status:2xx>99%", "", "status:2xx", Rate, GT, 99, UnitPercent, "status:2xx > 99%"},
		{"rps>500", "", "rps", Number, GT, 500, UnitNumber, "rps > 500"},
		{"rps>=1234.5", "", "rps", Number, GE, 1234.5, UnitNumber, "rps >= 1234.5"},
		{"requests>=10000", "", "requests", Count, GE, 10000, UnitCount, "requests >= 10000"},
		{"checkout: p95<800ms", "checkout", "p95", Latency, LT, 800, UnitMS, "checkout: p95 < 800ms"},
		{"checkout/add to cart: status:5xx<1%", "checkout/add to cart", "status:5xx", Rate, LT, 1, UnitPercent, "checkout/add to cart: status:5xx < 1%"},
		{"checks<0.5%", "", "checks", Rate, LT, 0.5, UnitPercent, "checks < 0.5%"},
		{"wall_p95<3s", "", "wall_p95", Latency, LT, 3000, UnitMS, "wall_p95 < 3s"},
	} {
		got, err := Parse(tc.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got.Scope != tc.scope || got.Metric != tc.metric || got.Kind != tc.kind ||
			got.Op != tc.op || got.Value != tc.value || got.Unit != tc.unit {
			t.Errorf("Parse(%q) = scope %q metric %q kind %v op %q value %v unit %q, want %q %q %v %q %v %q",
				tc.in, got.Scope, got.Metric, got.Kind, got.Op, got.Value, got.Unit,
				tc.scope, tc.metric, tc.kind, tc.op, tc.value, tc.unit)
		}
		if s := got.String(); s != tc.str {
			t.Errorf("Parse(%q).String() = %q, want %q", tc.in, s, tc.str)
		}
	}
}

func TestParseStatusRange(t *testing.T) {
	th, err := Parse("status:4xx<1%")
	if err != nil {
		t.Fatal(err)
	}
	if th.StatusLow != 400 || th.StatusHigh != 499 {
		t.Errorf("status:4xx range = %d-%d, want 400-499", th.StatusLow, th.StatusHigh)
	}
	th, _ = Parse("status:429<1")
	if th.StatusLow != 429 || th.StatusHigh != 429 {
		t.Errorf("status:429 range = %d-%d", th.StatusLow, th.StatusHigh)
	}
}

func TestParseErrors(t *testing.T) {
	for in, wantSub := range map[string]string{
		"":                "empty",
		"p95":             "no comparison",
		"p95=300ms":       "= alone",
		"p95==300ms":      "= alone",
		"<300ms":          "no metric",
		"p95<":            "no value",
		"p95<300ms<400ms": "one comparison",
		"p95<1%":          "not a percentage",
		"p95<fast":        "bad duration",
		"p95<-5ms":        "negative",
		"p0<300ms":        "out of range",
		"p101<300ms":      "out of range",
		"latency<300ms":   "unknown metric",
		"errors<150%":     "between 0% and 100%",
		"errors<-1%":      "between 0% and 100%",
		"errors<1.5":      "whole number",
		"errors<300ms":    "whole number",
		"status:6xx<1%":   "class like 5xx",
		"status:50<1%":    "class like 5xx",
		"status:abc<1%":   "class like 5xx",
		"rps>50%":         "plain number",
		"rps>5ms":         "plain number",
		"requests>1.5":    "whole number",
		": p95<300ms":     "empty scope",
	} {
		_, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) succeeded, want an error containing %q", in, wantSub)
			continue
		}
		if !strings.Contains(err.Error(), wantSub) {
			t.Errorf("Parse(%q) error %q, want it to contain %q", in, err, wantSub)
		}
	}
}

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		expr string
		m    Measured
		pass bool
	}{
		{"p95<300ms", Measured{Value: 299.9}, true},
		{"p95<300ms", Measured{Value: 300}, false}, // equal is not below
		{"p95<=300ms", Measured{Value: 300}, true},
		{"rps>500", Measured{Value: 500}, false},
		{"rps>=500", Measured{Value: 500}, true},
		{"rps>500", Measured{Value: 500.1}, true},
		{"errors<1%", Measured{Value: 0.99, Count: 99, Of: 10000}, true},
		{"errors<1%", Measured{Value: 1, Count: 100, Of: 10000}, false},
		{"errors<1%", Measured{NoSamples: true}, false}, // nothing measured fails
		{"requests>=100", Measured{Value: 100}, true},
	} {
		th, err := Parse(tc.expr)
		if err != nil {
			t.Fatal(err)
		}
		if got := Check(th, tc.m); got.Pass != tc.pass {
			t.Errorf("Check(%s, %+v).Pass = %v, want %v", tc.expr, tc.m, got.Pass, tc.pass)
		}
	}
}

func TestFlowOnly(t *testing.T) {
	for expr, want := range map[string]bool{
		"p95<1s": false, "errors<1%": false, "rps>1": false, "requests>1": false, "status:5xx<1%": false,
		"checks<1%": true, "aborted<1%": true, "ips>1": true, "setup_failed<1": true, "wall_p95<1s": true,
	} {
		th, err := Parse(expr)
		if err != nil {
			t.Fatal(err)
		}
		if th.FlowOnly() != want {
			t.Errorf("%s FlowOnly = %v, want %v", expr, th.FlowOnly(), want)
		}
	}
}
