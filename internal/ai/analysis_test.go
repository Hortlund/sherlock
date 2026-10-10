package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const validReport = `{"summary":"Incomplete evidence; cause unknown.","observations":[{"text":"Requests observed.","evidence_ids":["e1"]}],"hypotheses":[{"text":"A dependency may be slow; unconfirmed.","supporting_ids":["e1"],"contradicting_ids":[]}],"unknowns":["Dependency health is unknown."],"checks":["Inspect dependency health manually."]}`

func TestReportScenarios(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"bounded valid", validReport, true},
		{"cross scope", strings.ReplaceAll(validReport, `"e1"`, `"foreign"`), false},
		{"no support", strings.Replace(validReport, `"supporting_ids":["e1"]`, `"supporting_ids":[]`, 1), false},
		{"no unknowns", strings.Replace(validReport, `["Dependency health is unknown."]`, `[]`, 1), false},
		{"extra authority", strings.Replace(validReport, `"summary":`, `"tool":"send_slack","summary":`, 1), false},
		{"truncated", validReport[:40], false},
		{"trailing document", validReport + `{}`, false},
		{"null contract", strings.Replace(validReport, `"contradicting_ids":[]`, `"contradicting_ids":null`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := Validate([]byte(tc.body), []string{"e1"}, nil)
			if (e == nil) != tc.ok {
				t.Fatalf("valid=%v err=%v", tc.ok, e)
			}
		})
	}
	raw := strings.Replace(validReport, "Requests observed.", "Authorization: Bearer secret-token", 1)
	r, e := Validate([]byte(raw), []string{"e1"}, nil)
	if e != nil || strings.Contains(r.Observations[0].Text, "secret-token") {
		t.Fatal("output secret was retained", e)
	}
	if strings.Contains(Clean("customer-123", []string{`customer-\d+`}), "123") {
		t.Fatal("custom redaction failed")
	}
	if !strings.Contains(Clean("text", []string{"["}), "withheld") {
		t.Fatal("invalid redaction must fail closed")
	}
	if Cost(1, 1, 1, 1) != 2 || Cost(100, 50, 1000000, 2000000) != 200 {
		t.Fatal("cost rounding")
	}
}
func TestProviderContracts(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			c := &Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				var b map[string]any
				if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
					t.Fatal(e)
				}
				if b["model"] != "fixture-model" || b["tools"] != nil {
					t.Fatal("unexpected model/tools")
				}
				response := map[string]any{"usage": map[string]int{"input_tokens": 100, "output_tokens": 50}}
				if kind == "openai" {
					if r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer fixture-only" || b["store"] != false || b["max_output_tokens"] != float64(OutputLimit) {
						t.Fatal("OpenAI wire contract")
					}
					format := b["text"].(map[string]any)["format"].(map[string]any)
					if format["strict"] != true || format["type"] != "json_schema" {
						t.Fatal("missing strict schema")
					}
					response["status"] = "completed"
					response["output"] = []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": validReport}}}}
				} else {
					if r.URL.String() != "https://api.anthropic.com/v1/messages" || r.Header.Get("x-api-key") != "fixture-only" || r.Header.Get("anthropic-version") != "2023-06-01" || b["max_tokens"] != float64(OutputLimit) {
						t.Fatal("Anthropic wire contract")
					}
					if b["output_config"].(map[string]any)["format"].(map[string]any)["type"] != "json_schema" {
						t.Fatal("missing schema")
					}
					response["stop_reason"] = "end_turn"
					response["content"] = []any{map[string]any{"type": "text", "text": validReport}}
				}
				raw, _ := json.Marshal(response)
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"req_fixture"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}}
			got := c.Analyze(context.Background(), kind, "fixture-model", "fixture-only", json.RawMessage(`{"evidence":[]}`))
			if got.State != "completed" || !got.UsageKnown || got.Input != 100 || got.Output != 50 || got.RequestID != "req_fixture" {
				t.Fatalf("unexpected %+v", got)
			}
			if _, e := Validate(got.Text, []string{"e1"}, nil); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestProviderFailuresAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, kind, raw, state string
		status                 int
		known                  bool
	}{
		{"refusal", "openai", `{"status":"completed","usage":{"input_tokens":10,"output_tokens":2},"output":[{"type":"message","content":[{"type":"refusal"}]}]}`, "refused", 200, true},
		{"truncated", "openai", `{"status":"incomplete","usage":{"input_tokens":10,"output_tokens":2048}}`, "truncated", 200, true},
		{"claude refusal", "anthropic", `{"stop_reason":"refusal","usage":{"input_tokens":10,"output_tokens":2}}`, "refused", 200, true},
		{"claude truncated", "anthropic", `{"stop_reason":"max_tokens","usage":{"input_tokens":10,"output_tokens":2048}}`, "truncated", 200, true},
		{"missing usage", "openai", `{"status":"completed"}`, "unknown", 200, false},
		{"negative component", "anthropic", `{"stop_reason":"end_turn","usage":{"input_tokens":10,"cache_read_input_tokens":-2,"output_tokens":3}}`, "unknown", 200, false},
		{"overflow", "anthropic", `{"usage":{"input_tokens":9223372036854775807,"cache_read_input_tokens":9223372036854775807,"output_tokens":0}}`, "unknown", 200, false},
		{"bad JSON", "openai", `secret-error`, "unknown", 200, false},
		{"rate limit", "anthropic", `secret-error`, "failed", 429, true},
		{"credentials", "openai", `secret-error`, "failed", 401, true},
		{"server error", "openai", `secret-error`, "unknown", 503, false},
		{"request timeout", "openai", `secret-error`, "unknown", 408, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.raw))}, nil
			})}}
			got := c.Analyze(context.Background(), tc.kind, "fixture-model", "fixture-only", json.RawMessage(`{}`))
			if got.State != tc.state || got.UsageKnown != tc.known || len(got.Text) > 0 || strings.Contains(got.Reason, "secret") {
				t.Fatalf("unexpected %+v", got)
			}
		})
	}
	calls := 0
	c := &Client{HTTP: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) { calls++; return nil, errors.New("connection lost") })}}
	if got := c.Analyze(context.Background(), "openai", "fixture", "key", json.RawMessage(`{}`)); got.State != "unknown" || got.UsageKnown || calls != 1 {
		t.Fatal("ambiguous outcome replayed or released")
	}
	c.Analyze(context.Background(), "openai", "fixture", "key", json.RawMessage(`"`+strings.Repeat("x", InputLimit)+`"`))
	if calls != 1 {
		t.Fatal("oversized request dispatched")
	}
	if NewClient().HTTP.CheckRedirect == nil {
		t.Fatal("redirect policy absent")
	}
}
