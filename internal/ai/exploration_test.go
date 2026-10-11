package ai

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestExplorationAuthorityAndCeilings(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		raw, e := ExplorationBody(kind, "fixture", json.RawMessage(`{"evidence":[],"tools_enabled":true}`), 1856)
		if e != nil {
			t.Fatal(e)
		}
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		key := "max_output_tokens"
		if kind == "anthropic" {
			key = "max_tokens"
		}
		if b[key] != float64(1856) || !strings.Contains(string(raw), ExplorationInstructions) {
			t.Fatal("unbounded request", string(raw))
		}
	}
	for _, raw := range []string{
		`{"action":"tool","tool":"shell","report":null}`,
		`{"action":"tool","tool":"loki_patterns","report":null,"query":"{job=~\".*\"}"}`,
		`{"action":"tool","tool":"prometheus_counts","report":null,"service_id":"foreign"}`,
		`{"action":"tool","tool":"evidence","report":null,"start":"2000-01-01"}`,
		`{"action":"tool","tool":"notify","report":null}`,
	} {
		if _, e := ValidateAction([]byte(raw), []string{"e1"}, nil, true); e == nil {
			t.Fatal("accepted injected authority", raw)
		}
	}
	if _, e := ValidateAction([]byte(`{"action":"tool","tool":"loki_patterns","report":null}`), nil, nil, false); e == nil {
		t.Fatal("followup enabled tools implicitly")
	}
}

// Versioned expected outputs test the contract and labeled factual distinctions.
// These fixtures are not a live model quality or incident-recall measurement.
func TestExplorationScenarioEvaluationV1(t *testing.T) {
	raw, e := os.ReadFile("testdata/exploration-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name          string
		IDs           []string
		Contradiction string
		Input         string
		Output        json.RawMessage
		Valid         bool
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			action, e := ValidateAction(c.Output, c.IDs, nil, true)
			if (e == nil) != c.Valid {
				t.Fatal("contract classification", e)
			}
			if !c.Valid {
				return
			}
			if action.Report == nil {
				t.Fatal("expected evidence report")
			}
			if !strings.Contains(strings.ToLower(action.Report.Summary), "unknown") {
				t.Fatal("fixture asserts an unsupported cause")
			}
			if c.Contradiction != "" {
				found := false
				for _, h := range action.Report.Hypotheses {
					for _, id := range h.ContradictingIDs {
						found = found || id == c.Contradiction
					}
				}
				if !found {
					t.Fatal("contradiction omitted")
				}
			}
			wire, _ := json.Marshal(action)
			if strings.Contains(string(wire), "secret-value") {
				t.Fatal("secret not redacted")
			}
		})
	}
}
