package automation

import (
	"math"
	"sherlock/internal/detectors"
	"testing"
)

func TestMaterialChangeRules(t *testing.T) {
	c := detectors.Candidate{Detector: "error_rate", Current: .1, Severity: "warning", Thresholds: detectors.Defaults()}
	before, ok := Measure(c)
	if !ok {
		t.Fatal("valid measurement rejected")
	}
	for _, tc := range []struct {
		name     string
		value    float64
		severity string
		minutes  int
		want     string
	}{
		{"unchanged", .1, "warning", 15, ""}, {"small growth", .15, "warning", 15, ""}, {"doubling", .2, "warning", 15, "growth"}, {"recovery", .05, "warning", 15, ""}, {"severity", .1, "notice", 15, "severity_changed"}, {"window changed", .2, "warning", 30, "comparison_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := c
			next.Current = tc.value
			next.Severity = tc.severity
			next.Thresholds.CurrentMinutes = tc.minutes
			m, _ := Measure(next)
			if got := Change(before, m, 2); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	c.Current = .001
	before, _ = Measure(c)
	c.Current = .002
	after, _ := Measure(c)
	if Change(before, after, 2) != "" {
		t.Fatal("relative gate bypassed absolute minimum")
	}
	c.Current = math.NaN()
	if _, ok = Measure(c); ok {
		t.Fatal("NaN accepted")
	}
	c.Current = 2
	if _, ok = Measure(c); ok {
		t.Fatal("invalid proportion accepted")
	}
}
func TestSampleRatesAndComparability(t *testing.T) {
	c := detectors.Candidate{Detector: "new_exception", Current: 30, Severity: "warning", Thresholds: detectors.Defaults()}
	m, ok := Measure(c)
	if !ok || m.Value != 2 || m.MinimumDelta != .2 || m.Unit != "sampled_lines_per_minute" {
		t.Fatalf("incorrect normalization: %+v", m)
	}
	c.Thresholds.CadenceMinutes = 10
	c.Thresholds.RetentionDays = 14
	c.Thresholds.CooldownHours = 48
	n, _ := Measure(c)
	if m.Key != n.Key {
		t.Fatal("operational config changed comparable measurement")
	}
	c.Thresholds.MinOccurrences = 6
	n, _ = Measure(c)
	if Change(m, n, 2) != "comparison_changed" {
		t.Fatal("new thresholds compared to old thresholds")
	}
	c.Detector = "warning_burst"
	n, _ = Measure(c)
	if n.MinimumDelta != 10.0/15 {
		t.Fatal("wrong warning gate")
	}
}
func TestPolicyBounds(t *testing.T) {
	p := Defaults()
	if !p.Valid() {
		t.Fatal("invalid defaults")
	}
	p.Mode = "auto_investigate"
	if p.Valid() {
		t.Fatal("unimplemented automatic mode accepted")
	}
	p.Mode = "triage_only"
	p.CooldownMinutes = 0
	if p.Valid() {
		t.Fatal("zero cooldown accepted")
	}
	p.CooldownMinutes = 30
	p.GrowthMultiplier = math.Inf(1)
	if p.Valid() {
		t.Fatal("unbounded growth accepted")
	}
}
