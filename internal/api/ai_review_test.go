package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sherlock/internal/ai"
	"sherlock/internal/connectors"
	"strings"
	"testing"
)

func TestAIHistoryPagesRetainReconciliationAccess(t *testing.T) {
	s, h, c, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	p := configureAI(t, s, h, c, csrf, "openai", v.ID)
	s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })}}
	id := queueAITest(t, s, h, c, csrf, p.ID)
	runAI(t, s, ws, id, "unknown")
	if _, e := s.pool.Exec(ctx, `UPDATE ai_runs SET created_at=now()-interval '10 minutes',started_at=now()-interval '9 minutes' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	// Equal timestamps exercise the ID tie-breaker as well as the page boundary.
	if _, e := s.pool.Exec(ctx, `INSERT INTO ai_runs(workspace_id,provider_id,provider_revision,policy_revision,purpose,model,origin,digest,state,accounting,reserved,input_bound,input_price,output_price,price_date,expires_at,deadline)
 SELECT workspace_id,provider_id,provider_revision,policy_revision,purpose,model,origin,digest,'failed','settled',0,input_bound,input_price,output_price,price_date,expires_at,deadline FROM ai_runs CROSS JOIN generate_series(1,51) WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	type page struct {
		Items []struct{ ID, Accounting string }
		Next  string `json:"next_cursor"`
	}
	get := func(path string) page {
		t.Helper()
		w := call(h, "GET", path, nil, c, "", "")
		requireCode(t, w, 200)
		var out page
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	first := get("/api/v1/ai/runs")
	if len(first.Items) != 50 || first.Next == "" {
		t.Fatal("first page not bounded/cursored")
	}
	second := get("/api/v1/ai/runs?cursor=" + first.Next)
	if len(second.Items) != 2 || second.Next != "" {
		t.Fatal("incorrect final page")
	}
	seen := map[string]bool{}
	for _, v := range append(first.Items, second.Items...) {
		if seen[v.ID] {
			t.Fatal("duplicate across pages")
		}
		seen[v.ID] = true
	}
	if !seen[id] || second.Items[1].Accounting != "unknown" {
		t.Fatal("old unresolved invocation unreachable")
	}
	// Investigation filters must still apply to both page selection and cursor lookup.
	if got := get("/api/v1/ai/runs?investigation_id=00000000-0000-0000-0000-000000000001&cursor=" + first.Next); len(got.Items) != 0 {
		t.Fatal("cursor widened investigation scope")
	}
	if got := get("/api/v1/ai/runs?cursor=not-a-run"); len(got.Items) != 0 {
		t.Fatal("invalid cursor widened scope")
	}
	requireCode(t, call(h, "POST", "/api/v1/ai/runs/"+id+"/reconcile", map[string]any{"confirm": true, "cost": 42}, c, csrf, s.cfg.Origin), 200)
	settings := bodyMap(t, call(h, "GET", "/api/v1/ai/settings", nil, c, "", ""))
	if settings["reserved"] != float64(0) || settings["spent_today"] != float64(42) {
		t.Fatal("old reservation was not reconciled", settings)
	}
}

func TestAIDispatchMatchesAdmittedPayloadAfterJSONB(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, h, c, csrf, ws, v := reviewFixture(t, 0)
			ctx := context.Background()
			p := configureAI(t, s, h, c, csrf, kind, v.ID)
			p.AllowExcerpts = true
			w := call(h, "POST", "/api/v1/ai/providers/"+p.ID, map[string]any{"provider": p}, c, csrf, s.cfg.Origin)
			requireCode(t, w, 200)
			if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
				t.Fatal(e)
			}
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { return fixtureAIResponse(r, "success") })}}
			runAI(t, s, ws, queueAITest(t, s, h, c, csrf, p.ID), "completed")
			rid := completedInvestigation(t, s, h, c, csrf, ws, v.ID)
			if !v.Mapping.StoreExcerpts {
				t.Fatal("fixture requires excerpt consent")
			}
			// Find an accepted dense excerpt whose JSONB formatting crosses the input bound.
			var expected aiInput
			found := false
			for n := 350; n <= 512; n++ {
				sample := strings.Repeat(`\`, n) + strings.Repeat("x", 512-n)
				patterns, _ := json.Marshal([]connectors.Pattern{{Count: 1, Sample: sample}, {Count: 1, Sample: sample}})
				if _, e := s.pool.Exec(ctx, `UPDATE evidence SET document=jsonb_set(jsonb_set(document,'{errors}',$2::jsonb),'{warnings}','[]'::jsonb) WHERE service_id=$1 AND kind='log_patterns'`, v.ID, patterns); e != nil {
					t.Fatal(e)
				}
				input, e := s.buildAIInput(ctx, s.pool, ws, rid, p)
				if e != nil {
					t.Fatal(e)
				}
				var normalized []byte
				if e = s.pool.QueryRow(ctx, `SELECT $1::jsonb`, []byte(input.Payload)).Scan(&normalized); e != nil {
					t.Fatal(e)
				}
				bound, e := ai.InputBound(p.Kind, p.Model, normalized)
				if e != nil {
					t.Fatal(e)
				}
				if input.Bound <= ai.InputLimit && bound > ai.InputLimit {
					expected = input
					found = true
					break
				}
			}
			if !found {
				t.Fatal("failed to construct admitted near-limit JSONB case")
			}
			preview := call(h, "POST", "/api/v1/investigations/"+rid+"/ai/preview", aiStart{ProviderID: p.ID}, c, csrf, s.cfg.Origin)
			requireCode(t, preview, 200)
			var view struct {
				Payload json.RawMessage `json:"payload"`
				Bound   int             `json:"input_bound"`
			}
			if e := json.Unmarshal(preview.Body.Bytes(), &view); e != nil {
				t.Fatal(e)
			}
			expectedWire, e := ai.RequestBody(kind, p.Model, expected.Payload)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(view.Payload, expectedWire) || view.Bound != expected.Bound {
				t.Fatal("preview differs from admission")
			}
			sent := false
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
				raw, e := io.ReadAll(r.Body)
				if e != nil {
					t.Fatal(e)
				}
				if !bytes.Equal(raw, expectedWire) {
					t.Fatal("dispatch changed preview bytes")
				}
				sent = true
				r.Body = io.NopCloser(bytes.NewReader(raw))
				return fixtureAIResponse(r, "success")
			})}}
			launched := launchAI(t, s, h, c, csrf, p.ID, rid, false)
			requireCode(t, launched, 202)
			id := bodyMap(t, launched)["id"].(string)
			runAI(t, s, ws, id, "completed")
			if !sent {
				t.Fatal("accepted request was rejected before transport")
			}
		})
	}
}
