package api

import (
	"context"
	"net/http"
	"sherlock/internal/ai"
	"sync/atomic"
	"testing"
)

type reuseLedger struct {
	Runs, Jobs           int
	Charged, Outstanding int64
}

func readReuseLedger(t *testing.T, s *Server) reuseLedger {
	t.Helper()
	var ledger reuseLedger
	e := s.pool.QueryRow(context.Background(), `SELECT count(*),(SELECT count(*) FROM river_job WHERE kind='ai_analysis'),COALESCE(sum(charged),0),COALESCE(sum(reserved) FILTER(WHERE accounting IN ('reserved','unknown')),0) FROM ai_runs`).Scan(&ledger.Runs, &ledger.Jobs, &ledger.Charged, &ledger.Outstanding)
	if e != nil {
		t.Fatal(e)
	}
	return ledger
}

func TestAutomaticResultManualReuse(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, h, c, csrf, ws, _, p, batch := automaticFixture(t, kind, false)
			ctx := context.Background()
			if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			id := automaticState(t, s, batch, "launched")
			var investigation string
			if e := s.pool.QueryRow(ctx, `SELECT run_id::text FROM ai_runs WHERE id=$1`, id).Scan(&investigation); e != nil {
				t.Fatal(e)
			}
			// A manual request cannot take over active automatic work or queue a
			// second paid invocation, even when fresh analysis was requested.
			for _, fresh := range []bool{false, true} {
				requireCode(t, launchAI(t, s, h, c, csrf, p.ID, investigation, fresh), 409)
			}
			var calls atomic.Int32
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return fixtureAIResponse(r, "success")
			})}}
			runAI(t, s, ws, id, "completed")
			before := readReuseLedger(t, s)
			assertReuse := func() {
				t.Helper()
				w := launchAI(t, s, h, c, csrf, p.ID, investigation, false)
				requireCode(t, w, 202)
				body := bodyMap(t, w)
				if body["id"] != id || body["reused"] != true {
					t.Fatalf("automatic result was not reused: %s", w.Body.String())
				}
				runAI(t, s, ws, body["id"].(string), "completed")
				if after := readReuseLedger(t, s); after != before || calls.Load() != 1 {
					t.Fatalf("reuse changed accounting/jobs or sent HTTP: before=%+v after=%+v calls=%d", before, after, calls.Load())
				}
			}
			assertReuse()
			// Manual reuse remains available after automatic consent is revoked.
			// It must neither enable automation again nor alter the original run.
			policy, e := loadAutomationPolicy(ctx, s.pool, ws)
			if e != nil {
				t.Fatal(e)
			}
			policy.Mode, policy.AutomaticConsent = "off", false
			saveAutomationTest(t, s, h, c, csrf, policy)
			assertReuse()
			w := launchAI(t, s, h, c, csrf, p.ID, investigation, true)
			requireCode(t, w, 202)
			body := bodyMap(t, w)
			freshID := body["id"].(string)
			if freshID == id || body["reused"] != false {
				t.Fatal("explicit fresh request reused automatic result", w.Body.String())
			}
			runAI(t, s, ws, freshID, "completed")
			after := readReuseLedger(t, s)
			if after.Runs != before.Runs+1 || after.Jobs != before.Jobs+1 || after.Charged <= before.Charged || after.Outstanding != 0 || calls.Load() != 2 {
				t.Fatalf("fresh request did not execute exactly once: before=%+v after=%+v calls=%d", before, after, calls.Load())
			}
			var unchanged bool
			if e = s.pool.QueryRow(ctx, `SELECT (SELECT purpose='automatic' FROM ai_runs WHERE id=$1) AND (SELECT purpose='analysis' FROM ai_runs WHERE id=$2) AND (SELECT count(*)=1 FROM ai_runs WHERE purpose='automatic')`, id, freshID).Scan(&unchanged); e != nil {
				t.Fatal(e)
			}
			if !unchanged {
				t.Fatal("manual request changed automatic provenance or queued automatic work")
			}
			policy, e = loadAutomationPolicy(ctx, s.pool, ws)
			if e != nil || policy.Mode != "off" || policy.AutomaticConsent {
				t.Fatal("manual request enabled automation", policy, e)
			}
		})
	}
}

func TestAutomaticResultManualReuseEligibility(t *testing.T) {
	for _, scenario := range []string{"expired", "deleted", "policy_changed", "provider_changed", "evidence_expired", "service_excluded"} {
		t.Run(scenario, func(t *testing.T) {
			s, h, c, csrf, ws, _, p, batch := automaticFixture(t, "openai", false)
			ctx := context.Background()
			if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			id := automaticState(t, s, batch, "launched")
			runAI(t, s, ws, id, "completed")
			var investigation string
			if e := s.pool.QueryRow(ctx, `SELECT run_id::text FROM ai_runs WHERE id=$1`, id).Scan(&investigation); e != nil {
				t.Fatal(e)
			}
			// Keep a real preview so evidence/egress revocation is tested by the
			// admission endpoint as well as by result-cache eligibility.
			w := call(h, "POST", "/api/v1/investigations/"+investigation+"/ai/preview", map[string]string{"provider_id": p.ID}, c, csrf, s.cfg.Origin)
			requireCode(t, w, 200)
			digest := bodyMap(t, w)["preview_digest"].(string)
			switch scenario {
			case "expired":
				if _, e := s.pool.Exec(ctx, `UPDATE ai_runs SET expires_at=now()-interval '1 second' WHERE id=$1`, id); e != nil {
					t.Fatal(e)
				}
			case "deleted":
				requireCode(t, call(h, "POST", "/api/v1/ai/runs/"+id+"/delete", map[string]any{}, c, csrf, s.cfg.Origin), 200)
			case "policy_changed":
				policy, e := loadAIPolicy(ctx, s.pool, ws)
				if e != nil {
					t.Fatal(e)
				}
				requireCode(t, call(h, "POST", "/api/v1/ai/policy", policy, c, csrf, s.cfg.Origin), 200)
			case "provider_changed", "service_excluded":
				if scenario == "provider_changed" {
					p.Model += "-updated"
				} else {
					p.Services = []string{}
				}
				requireCode(t, call(h, "POST", "/api/v1/ai/providers/"+p.ID, map[string]any{"provider": p}, c, csrf, s.cfg.Origin), 200)
				if scenario == "provider_changed" {
					runAI(t, s, ws, queueAITest(t, s, h, c, csrf, p.ID), "completed")
				}
			case "evidence_expired":
				if _, e := s.pool.Exec(ctx, `UPDATE evidence SET expires_at=now()-interval '1 second' WHERE id::text IN (SELECT unnest(evidence_ids) FROM ai_runs WHERE id=$1)`, id); e != nil {
					t.Fatal(e)
				}
			}
			before := readReuseLedger(t, s)
			if scenario == "evidence_expired" || scenario == "service_excluded" {
				w = call(h, "POST", "/api/v1/investigations/"+investigation+"/ai", map[string]any{"provider_id": p.ID, "preview_digest": digest, "confirm": true, "fresh": false}, c, csrf, s.cfg.Origin)
				want := 409
				if scenario == "service_excluded" {
					want = 403
				}
				requireCode(t, w, want)
				if after := readReuseLedger(t, s); after != before {
					t.Fatal("ineligible request changed the ledger", before, after)
				}
				return
			}
			w = launchAI(t, s, h, c, csrf, p.ID, investigation, false)
			requireCode(t, w, 202)
			body := bodyMap(t, w)
			if body["id"] == id || body["reused"] != false {
				t.Fatal("ineligible automatic result was reused", w.Body.String())
			}
			if after := readReuseLedger(t, s); after.Runs != before.Runs+1 || after.Jobs != before.Jobs+1 {
				t.Fatal("replacement analysis was not queued once", before, after)
			}
		})
	}
}
