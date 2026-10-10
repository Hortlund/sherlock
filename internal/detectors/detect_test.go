package detectors

import (
	"sherlock/internal/connectors"
	"testing"
	"time"
)

func fixture(kind string, value float64, start, end time.Time) Input {
	return Input{ID: kind, Observation: connectors.Observation{Kind: kind, Complete: true, Value: value, SampleCount: 20, CoverageFraction: .9, Start: start, End: end}}
}
func TestGuardsAndScenarios(t *testing.T) {
	end := time.Now()
	start := end.Add(-15 * time.Minute)
	bs := start.Add(-time.Hour)
	now := []Input{fixture("request_total", 1000, start, end), fixture("request_errors", 120, start, end), fixture("log_patterns", 0, start, end)}
	base := []Input{fixture("request_total", 4000, bs, start), fixture("request_errors", 4, bs, start), fixture("log_patterns", 0, bs, start)}
	now[2].Observation.Errors = []connectors.Pattern{{Fingerprint: "timeout", Count: 12}}
	r := Evaluate(now, base, Defaults())
	if len(r.Candidates) != 2 {
		t.Fatalf("timeout scenario: %+v", r)
	}
	for _, test := range []string{"truncated", "empty", "low_volume", "stale", "warmup", "disabled", "healthy", "warning"} {
		t.Run(test, func(t *testing.T) {
			n, b := append([]Input{}, now...), append([]Input{}, base...)
			c := Defaults()
			want := 0
			switch test {
			case "truncated":
				for i := range n {
					n[i].Observation.Complete = false
				}
			case "empty":
				n = nil
			case "low_volume":
				n[0].Observation.Value = 1
				n[2].Observation.SampleCount = 1
			case "stale":
				n[0].Observation.Complete = false
				n[2].Observation.CoverageFraction = .1
			case "warmup":
				b = nil
			case "disabled":
				c.Disabled = []string{"new_exception", "warning_burst", "error_rate"}
			case "healthy":
				n[1].Observation.Value = 1
				n[2].Observation.Errors = nil
			case "warning":
				n[1].Observation.Value = 1
				n[2].Observation.Errors = nil
				n[2].Observation.Warnings = []connectors.Pattern{{Count: 15}}
				want = 1
			}
			result := Evaluate(n, b, c)
			if len(result.Candidates) != want {
				t.Fatalf("got %d candidates: %+v", len(result.Candidates), result)
			}
		})
	}
}
