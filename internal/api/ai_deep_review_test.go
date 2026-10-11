package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"sherlock/internal/ai"
)

func TestDeepReviewAutomaticManualReuse(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, h, c, csrf, ws, _, p, batch := automaticFixture(t, kind, false)
			ctx := context.Background()
			policy, e := loadAutomationPolicy(ctx, s.pool, ws)
			if e != nil {
				t.Fatal(e)
			}
			policy.ToolsEnabled = true
			policy = saveAutomationTest(t, s, h, c, csrf, policy)
			if _, e = s.pool.Exec(ctx, `UPDATE automation_batches SET policy_revision=$2 WHERE id=$1`, batch, policy.Revision); e != nil {
				t.Fatal(e)
			}
			if e = s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			id := automaticState(t, s, batch, "launched")
			var run string
			if e = s.pool.QueryRow(ctx, `SELECT run_id::text FROM ai_runs WHERE id=$1`, id).Scan(&run); e != nil {
				t.Fatal(e)
			}
			for _, fresh := range []bool{false, true} {
				requireCode(t, launchDeep(t, s, h, c, csrf, run, aiStart{ProviderID: p.ID, Tools: true, Fresh: fresh}), 409)
			}
			var calls atomic.Int32
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return fixtureAIResponse(r, "success")
			})}}
			deepStep(t, s, ws, id, 1)
			policy.Mode, policy.AutomaticConsent, policy.ToolsEnabled = "off", false, false
			saveAutomationTest(t, s, h, c, csrf, policy)
			before := readReuseLedger(t, s)
			w := launchDeep(t, s, h, c, csrf, run, aiStart{ProviderID: p.ID, Tools: true})
			requireCode(t, w, 202)
			if bodyMap(t, w)["id"] != id || bodyMap(t, w)["reused"] != true {
				t.Fatal(w.Body.String())
			}
			deepStep(t, s, ws, id, 1)
			if after := readReuseLedger(t, s); before != after || calls.Load() != 1 {
				t.Fatal(before, after, calls.Load())
			}
			w = launchDeep(t, s, h, c, csrf, run, aiStart{ProviderID: p.ID, Tools: true, Fresh: true})
			requireCode(t, w, 202)
			freshID := bodyMap(t, w)["id"].(string)
			if freshID == id {
				t.Fatal("fresh request reused automatic result")
			}
			deepStep(t, s, ws, freshID, 1)
			assertAIState(t, s, freshID, "completed")
			policy, e = loadAutomationPolicy(ctx, s.pool, ws)
			if e != nil || policy.Mode != "off" || policy.AutomaticConsent || policy.ToolsEnabled {
				t.Fatal(policy, e)
			}
		})
	}
}

func TestDeepReviewStalePricePreview(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, h, c, csrf, ws, _, p, run := deepFixture(t, kind)
			in := aiStart{ProviderID: p.ID, Tools: true, Confirm: true}
			w := call(h, "POST", "/api/v1/investigations/"+run+"/ai/preview", in, c, csrf, s.cfg.Origin)
			requireCode(t, w, 200)
			in.Digest = bodyMap(t, w)["preview_digest"].(string)
			p.InputPrice *= 2
			requireCode(t, call(h, "POST", "/api/v1/ai/providers/"+p.ID, map[string]any{"provider": p}, c, csrf, s.cfg.Origin), 200)
			runAI(t, s, ws, queueAITest(t, s, h, c, csrf, p.ID), "completed")
			before := readReuseLedger(t, s)
			w = call(h, "POST", "/api/v1/investigations/"+run+"/ai", in, c, csrf, s.cfg.Origin)
			requireCode(t, w, 409)
			if after := readReuseLedger(t, s); after != before {
				t.Fatal("stale preview queued work", before, after)
			}
			requireCode(t, launchDeep(t, s, h, c, csrf, run, in), 202)
		})
	}
}

func TestDeepReviewToolFailureProgress(t *testing.T) {
	s, h, c, csrf, ws, _, p, run := deepFixture(t, "openai")
	w := launchDeep(t, s, h, c, csrf, run, aiStart{ProviderID: p.ID, Tools: true})
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { return fixtureAIResponse(r, "tool:loki_patterns") })}}
	deepStep(t, s, ws, id, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer upstream.Close()
	if _, e := s.pool.Exec(context.Background(), `UPDATE connections SET base_url=$1 WHERE kind='loki'`, upstream.URL); e != nil {
		t.Fatal(e)
	}
	deepStep(t, s, ws, id, 2)
	assertAIState(t, s, id, "insufficient_evidence")
	var state, reason string
	if e := s.pool.QueryRow(context.Background(), `SELECT state,reason FROM ai_steps WHERE ai_run_id=$1 AND sequence=2`, id).Scan(&state, &reason); e != nil {
		t.Fatal(e)
	}
	if state != "failed" || reason == "" {
		t.Fatalf("failed collection misreported: %s %q", state, reason)
	}
}

func TestDeepReviewCancelDuringCollection(t *testing.T) {
	s, h, c, csrf, ws, _, p, run := deepFixture(t, "openai")
	w := launchDeep(t, s, h, c, csrf, run, aiStart{ProviderID: p.ID, Tools: true})
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { return fixtureAIResponse(r, "tool:loki_patterns") })}}
	deepStep(t, s, ws, id, 1)
	entered := make(chan struct{}, 2)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, e := s.pool.Exec(ctx, `UPDATE connections SET base_url=$1 WHERE kind='loki'`, upstream.URL); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.executeDeep(ctx, deepArgs{ws, id, 2}) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	reqDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		reqDone <- call(h, "POST", "/api/v1/ai/runs/"+id+"/delete", map[string]any{}, c, csrf, s.cfg.Origin)
	}()
	select {
	case response := <-reqDone:
		requireCode(t, response, 200)
	case <-ctx.Done():
		t.Fatal("cancellation deadlocked", ctx.Err())
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("worker did not stop", ctx.Err())
	}
	assertAIState(t, s, id, "cancelled")
	deepStep(t, s, ws, id, 2)
	var charged int64
	var account string
	var steps, contents, attempts int
	if e := s.pool.QueryRow(ctx, `SELECT charged,accounting,telemetry_attempts,(SELECT count(*) FROM ai_steps WHERE ai_run_id=$1),(SELECT count(*) FROM ai_steps WHERE ai_run_id=$1 AND (result IS NOT NULL OR payload IS NOT NULL)) FROM ai_runs WHERE id=$1`, id).Scan(&charged, &account, &attempts, &steps, &contents); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 || attempts != 1 || charged != 260 || account != "settled" || steps != 2 || contents != 0 {
		t.Fatal(calls.Load(), attempts, charged, account, steps, contents)
	}
}
