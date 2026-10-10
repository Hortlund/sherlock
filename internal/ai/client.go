package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sherlock/internal/connectors"
	"time"
)

type Client struct{ HTTP *http.Client }

func NewClient() *Client {
	p, _ := connectors.NewPolicy("", "")
	return &Client{HTTP: p.HTTPClient(150 * time.Second)}
}

type Result struct {
	Text                     []byte
	State, Reason, RequestID string
	Input, Output            int64
	UsageKnown               bool
	HTTPStatus               int
}

var safeRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

func RequestBody(kind, model string, payload json.RawMessage) ([]byte, error) {
	if kind == "openai" {
		return json.Marshal(map[string]any{"model": model, "store": false, "max_output_tokens": OutputLimit, "instructions": Instructions, "input": string(payload), "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "sherlock_analysis", "strict": true, "schema": Schema()}}})
	}
	return json.Marshal(map[string]any{"model": model, "max_tokens": OutputLimit, "system": Instructions, "messages": []any{map[string]any{"role": "user", "content": string(payload)}}, "output_config": map[string]any{"format": map[string]any{"type": "json_schema", "schema": Schema()}}})
}
func InputBound(kind, model string, payload json.RawMessage) (int, error) {
	b, e := RequestBody(kind, model, payload)
	return len(b) + 1024, e
}
func (c *Client) Analyze(ctx context.Context, kind, model, key string, payload json.RawMessage) Result {
	out := Result{State: "unknown", Reason: "No complete acknowledgement. Provider charges may have occurred; reservation retained."}
	body, e := RequestBody(kind, model, payload)
	if e != nil || len(body)+1024 > InputLimit {
		out.State = "failed"
		out.Reason = "Input exceeds the bounded analysis context."
		out.UsageKnown = true
		return out
	}
	endpoint := "https://api.openai.com/v1/responses"
	if kind == "anthropic" {
		endpoint = "https://api.anthropic.com/v1/messages"
	} else if kind != "openai" {
		out.State = "failed"
		out.UsageKnown = true
		return out
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if e != nil {
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sherlock/0.2")
	if kind == "openai" {
		req.Header.Set("Authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return out
	}
	defer resp.Body.Close()
	out.HTTPStatus = resp.StatusCode
	id := resp.Header.Get("x-request-id")
	if id == "" {
		id = resp.Header.Get("request-id")
	}
	if safeRequestID.MatchString(id) {
		out.RequestID = id
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 {
			out.State = "failed"
			out.UsageKnown = true
			out.Reason = "Provider rejected the request. Check model access, credentials and schema support."
			if resp.StatusCode == 429 {
				out.Reason = "Provider rate limited this request. No automatic paid retry; try again later."
			}
		}
		return out
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if e != nil || len(raw) > 256*1024 {
		return out
	}
	var wire struct {
		Status     string `json:"status"`
		StopReason string `json:"stop_reason"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			Input       *int64 `json:"input_tokens"`
			Output      *int64 `json:"output_tokens"`
			CacheCreate int64  `json:"cache_creation_input_tokens"`
			CacheRead   int64  `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return out
	}
	if wire.Usage != nil && wire.Usage.Input != nil && wire.Usage.Output != nil {
		in, outTokens := *wire.Usage.Input+wire.Usage.CacheCreate+wire.Usage.CacheRead, *wire.Usage.Output
		if *wire.Usage.Input >= 0 && *wire.Usage.Input <= 10000000 && wire.Usage.CacheCreate >= 0 && wire.Usage.CacheCreate <= 10000000 && wire.Usage.CacheRead >= 0 && wire.Usage.CacheRead <= 10000000 && in >= 0 && outTokens >= 0 && in <= 10000000 && outTokens <= 10000000 {
			out.Input = in
			out.Output = outTokens
			out.UsageKnown = true
		}
	}
	if !out.UsageKnown {
		out.Reason = "Provider response lacks reliable usage; reservation retained and report withheld."
		return out
	}
	out.State = "completed"
	out.Reason = "Structured response received; evidence validation required."
	if kind == "openai" {
		if wire.Status != "completed" {
			out.State = "truncated"
			out.Reason = "Provider did not complete the response."
			return out
		}
		for _, item := range wire.Output {
			if item.Type != "message" {
				continue
			}
			for _, block := range item.Content {
				if block.Type == "refusal" {
					out.State = "refused"
				}
				if block.Type == "output_text" {
					out.Text = append(out.Text, []byte(block.Text)...)
				}
			}
		}
	} else {
		if wire.StopReason == "refusal" {
			out.State = "refused"
		} else if wire.StopReason != "end_turn" {
			out.State = "truncated"
		}
		for _, block := range wire.Content {
			if block.Type == "text" {
				out.Text = append(out.Text, []byte(block.Text)...)
			}
		}
	}
	if out.State != "completed" {
		out.Text = nil
		out.Reason = "Provider refused or truncated the analysis; deterministic report remains available."
	}
	return out
}
