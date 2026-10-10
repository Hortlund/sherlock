package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTriageDecisionScenarios(t *testing.T) {
	for _, decision := range []string{"investigate", "observe", "needs_attention", "need_more_data"} {
		raw, _ := json.Marshal(TriageDecision{decision, "Insufficient causal evidence; review manually.", []string{"e1"}})
		if _, e := ValidateTriage(raw, []string{"e1"}, nil); e != nil {
			t.Fatal(e)
		}
	}
	for _, raw := range []string{
		`{"decision":"start_investigation","reason":"Do it","evidence_ids":["e1"]}`,
		`{"decision":"investigate","reason":"Look","evidence_ids":["foreign"]}`,
		`{"decision":"observe","reason":"Look","evidence_ids":[]}`,
		`{"decision":"observe","reason":"Look","evidence_ids":["e1"],"tool":"send_slack"}`,
		`{"decision":"observe","reason":"Look","evidence_ids":["e1","e1"]}`,
		`{"decision":"observe","reason":"Look","evidence_ids":["e1"]}{}`,
	} {
		if _, e := ValidateTriage([]byte(raw), []string{"e1"}, nil); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	d, e := ValidateTriage([]byte(`{"decision":"needs_attention","reason":"Authorization: Bearer secret-value","evidence_ids":["e1"]}`), []string{"e1"}, nil)
	if e != nil || strings.Contains(d.Reason, "secret-value") {
		t.Fatal(d, e)
	}
}
func TestTriageProviderContract(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			c := &Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				calls++
				var b map[string]any
				if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
					t.Fatal(e)
				}
				if b["tools"] != nil || b["model"] != "triage-model" {
					t.Fatal(b)
				}
				text := `{"decision":"need_more_data","reason":"Cause is unverified.","evidence_ids":["e1"]}`
				wire := map[string]any{"usage": map[string]int{"input_tokens": 100, "output_tokens": 60}}
				if kind == "openai" {
					if b["max_output_tokens"] != float64(TriageOutputLimit) || b["store"] != false || b["instructions"] != TriageInstructions {
						t.Fatal(b)
					}
					format := b["text"].(map[string]any)["format"].(map[string]any)
					if format["name"] != "sherlock_triage" || format["strict"] != true {
						t.Fatal(format)
					}
					wire["status"] = "completed"
					wire["output"] = []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": text}}}}
				} else {
					if b["max_tokens"] != float64(TriageOutputLimit) || b["system"] != TriageInstructions || b["output_config"] == nil {
						t.Fatal(b)
					}
					wire["stop_reason"] = "end_turn"
					wire["content"] = []any{map[string]string{"type": "text", "text": text}}
				}
				raw, _ := json.Marshal(wire)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}}
			result := c.Triage(context.Background(), kind, "triage-model", "fixture", json.RawMessage(`{}`))
			if result.State != "completed" {
				t.Fatal(result)
			}
			if _, e := ValidateTriage(result.Text, []string{"e1"}, nil); e != nil {
				t.Fatal(e)
			}
			result = c.Triage(context.Background(), kind, "triage-model", "fixture", json.RawMessage(`"`+strings.Repeat("x", TriageInputLimit)+`"`))
			if result.State != "failed" || calls != 1 {
				t.Fatal("oversized context dispatched", result, calls)
			}
		})
	}
}
