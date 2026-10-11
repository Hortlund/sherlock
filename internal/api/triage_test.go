package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sherlock/internal/ai"
	"sherlock/internal/detectors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func triageFixture(t *testing.T, kind string) (*Server, http.Handler, *http.Cookie, string, string, Service, AIProvider, string) {
	t.Helper()
	s, h, c, csrf, ws, v, policy, batch := automationFixture(t)
	p := configureAI(t, s, h, c, csrf, kind, v.ID)
	p.TriageModel = "fixture-triage"
	p.TriageInputPrice = 1000000
	p.TriageOutputPrice = 2000000
	w := call(h, "POST", "/api/v1/ai/providers/"+p.ID, map[string]any{"provider": p}, c, csrf, s.cfg.Origin)
	requireCode(t, w, 200)
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { return fixtureAIResponse(r, "success") })}}
	requireCode(t, call(h, "POST", "/api/v1/ai/providers/"+p.ID+"/triage-test", map[string]bool{"confirm": false}, c, csrf, s.cfg.Origin), 400)
	w = call(h, "POST", "/api/v1/ai/providers/"+p.ID+"/triage-preview", map[string]any{}, c, csrf, s.cfg.Origin)
	requireCode(t, w, 200)
	if bodyMap(t, w)["sends_data"] != false {
		t.Fatal("preview sent data")
	}
	w = call(h, "POST", "/api/v1/ai/providers/"+p.ID+"/triage-test", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin)
	requireCode(t, w, 202)
	runAI(t, s, ws, bodyMap(t, w)["id"].(string), "completed")
	var e error
	p, e = loadAIProvider(context.Background(), s.pool, ws, p.ID)
	if e != nil || p.TriageCapability != "ready" || p.Capability != "untested" {
		t.Fatal(p, e)
	}
	policy.TriageProviderID = p.ID
	policy = saveAutomationTest(t, s, h, c, csrf, policy)
	// Align this pre-existing test batch with the explicitly saved policy. Production
	// policy edits invalidate existing batches; a subsequent new occurrence is used.
	if _, e = s.pool.Exec(context.Background(), `UPDATE automation_batches SET policy_revision=$2 WHERE id=$1`, batch, policy.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(context.Background(), `UPDATE automation_checkpoints SET policy_revision=$1`, policy.Revision); e != nil {
		t.Fatal(e)
	}
	return s, h, c, csrf, ws, v, p, batch
}
func admitTriage(t *testing.T, s *Server, ws, batch string) string {
	t.Helper()
	automationDue(t, s, batch)
	if e := s.evaluateAutomation(context.Background(), automationArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	var id *string
	var status, reason string
	if e := s.pool.QueryRow(context.Background(), `SELECT ai_run_id::text,triage_status,triage_reason FROM automation_batches WHERE id=$1`, batch).Scan(&id, &status, &reason); e != nil || id == nil {
		t.Fatal("triage not queued", status, reason, e)
	}
	return *id
}
func TestTriageProvidersAndManualBoundary(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, h, c, _, ws, _, _, batch := triageFixture(t, kind)
			var calls atomic.Int32
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return fixtureAIResponse(r, "observe") })}}
			id := admitTriage(t, s, ws, batch)
			runAI(t, s, ws, id, "completed")
			runAI(t, s, ws, id, "completed")
			if calls.Load() != 1 {
				t.Fatal("paid replay", calls.Load())
			}
			var report []byte
			var bound, limit int
			var purpose, accounting string
			if e := s.pool.QueryRow(context.Background(), `SELECT report,input_bound,output_limit,purpose,accounting FROM ai_runs WHERE id=$1`, id).Scan(&report, &bound, &limit, &purpose, &accounting); e != nil {
				t.Fatal(e)
			}
			var decision ai.TriageDecision
			if json.Unmarshal(report, &decision) != nil || decision.Decision != "observe" || bound > 4000 || limit != 1000 || purpose != "triage" || accounting != "settled" {
				t.Fatal(string(report), bound, limit, purpose, accounting)
			}
			automationState(t, s, batch, "recommended")
			var runs, events int
			if e := s.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM runs WHERE kind='investigation'),(SELECT count(*) FROM notification_events)`).Scan(&runs, &events); e != nil || runs != 0 || events == 0 {
				t.Fatal("manual/notification boundary", runs, events, e)
			}
			w := call(h, "GET", "/api/v1/automation/activity", nil, c, "", "")
			requireCode(t, w, 200)
			if !strings.Contains(w.Body.String(), `"decision":"observe"`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}
func TestTriageDispatchRevocation(t *testing.T) {
	for _, scenario := range []string{"monitoring", "mapping", "detectors", "snooze", "policy", "source", "recovery", "deadline", "privacy"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, _, ws, v, _, batch := triageFixture(t, "openai")
			id := admitTriage(t, s, ws, batch)
			ctx := context.Background()
			statements := map[string]string{
				"monitoring": `UPDATE services SET monitoring=false`, "mapping": `UPDATE services SET revision=revision+1`,
				"detectors": `INSERT INTO workspace_settings(workspace_id,settings) SELECT workspace_id,jsonb_set(details->'thresholds','{disabled}','["error_rate","new_exception","warning_burst"]') FROM findings LIMIT 1 ON CONFLICT(workspace_id) DO UPDATE SET settings=EXCLUDED.settings`,
				"snooze":    `UPDATE findings SET state='snoozed'`, "policy": `UPDATE automation_policy SET revision=revision+1`, "source": `UPDATE connections SET enabled=false`, "deadline": `UPDATE ai_runs SET deadline=now()-interval '1 second' WHERE purpose='triage'`, "privacy": `UPDATE ai_policy SET revision=revision+1,enabled=false`,
			}
			if scenario == "recovery" {
				_, at := automationCandidate(t, s, v)
				result := healthyAutomationResult(at.Add(time.Minute), true)
				if e := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, at.Add(time.Minute), detectors.Defaults(), result.Candidates, result.Coverage...); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e := s.pool.Exec(ctx, statements[scenario]); e != nil {
					t.Fatal(e)
				}
			}
			var calls atomic.Int32
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return fixtureAIResponse(r, "success") })}}
			runAI(t, s, ws, id, "cancelled")
			if calls.Load() != 0 {
				t.Fatal("revoked data dispatched")
			}
			automationState(t, s, batch, "recommended")
		})
	}
}
func TestTriageFallbackAccountingAndRetention(t *testing.T) {
	for _, mode := range []string{"refused", "foreign", "authority", "unknown", "overusage"} {
		t.Run(mode, func(t *testing.T) {
			s, h, c, csrf, ws, _, p, batch := triageFixture(t, "anthropic")
			var calls atomic.Int32
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if mode == "unknown" {
					return nil, errors.New("fixture interrupted")
				}
				return fixtureAIResponse(r, mode)
			})}}
			id := admitTriage(t, s, ws, batch)
			want := "fallback"
			if mode == "refused" {
				want = "refused"
			}
			if mode == "unknown" {
				want = "unknown"
			}
			runAI(t, s, ws, id, want)
			runAI(t, s, ws, id, want)
			if calls.Load() != 1 {
				t.Fatal("replayed call")
			}
			var accounting string
			var content bool
			if e := s.pool.QueryRow(context.Background(), `SELECT accounting,report IS NOT NULL FROM ai_runs WHERE id=$1`, id).Scan(&accounting, &content); e != nil || content {
				t.Fatal(accounting, content, e)
			}
			if mode == "unknown" && accounting != "unknown" {
				t.Fatal("lost uncertain reservation")
			}
			requireCode(t, call(h, "POST", "/api/v1/ai/providers/"+p.ID+"/delete", map[string]any{}, c, csrf, s.cfg.Origin), 200)
			automationState(t, s, batch, "recommended")
		})
	}
}

func TestTriageBudgetConsentAndCache(t *testing.T) {
	for _, scenario := range []string{"budget", "old unknown reservation", "consent", "cache"} {
		t.Run(scenario, func(t *testing.T) {
			s, h, c, csrf, ws, _, p, batch := triageFixture(t, "openai")
			ctx := context.Background()
			if scenario == "cache" {
				id := admitTriage(t, s, ws, batch)
				runAI(t, s, ws, id, "completed")
				tx, e := s.pool.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer tx.Rollback(ctx)
				policy, e := loadAutomationPolicy(ctx, tx, ws)
				if e != nil {
					t.Fatal(e)
				}
				if e = s.enqueueTriage(ctx, tx, ws, batch, policy); e != nil {
					t.Fatal(e)
				}
				if e = tx.Commit(ctx); e != nil {
					t.Fatal(e)
				}
				var status, reused string
				var count int
				if e = s.pool.QueryRow(ctx, `SELECT triage_status,ai_run_id::text FROM automation_batches WHERE id=$1`, batch).Scan(&status, &reused); e != nil || status != "cached" || reused != id {
					t.Fatal(status, reused, e)
				}
				if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM ai_runs WHERE purpose='triage'`).Scan(&count); e != nil || count != 1 {
					t.Fatal("cache charged again", count, e)
				}
				requireCode(t, call(h, "POST", "/api/v1/ai/runs/"+id+"/delete", map[string]any{}, c, csrf, s.cfg.Origin), 200)
				w := call(h, "GET", "/api/v1/automation/activity", nil, c, "", "")
				requireCode(t, w, 200)
				if strings.Contains(w.Body.String(), `"decision"`) || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
					t.Fatal("deleted content retained", w.Body.String())
				}
				return
			}
			switch scenario {
			case "budget":
				_, _ = s.pool.Exec(ctx, `UPDATE ai_policy SET daily_limit=1,run_limit=1`)
			case "old unknown reservation":
				_, _ = s.pool.Exec(ctx, `UPDATE ai_runs SET accounting='unknown',state='unknown',reserved=6000,created_at=now()-interval '1 day' WHERE purpose='triage_test'`)
				_, _ = s.pool.Exec(ctx, `UPDATE ai_policy SET daily_limit=10000,run_limit=10000`)
			case "consent":
				_, _ = s.pool.Exec(ctx, `UPDATE ai_providers SET config=jsonb_set(config,'{services}','[]') WHERE id=$1`, p.ID)
			}
			automationDue(t, s, batch)
			if e := s.evaluateAutomation(ctx, automationArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			automationState(t, s, batch, "recommended")
			var status string
			var id *string
			if e := s.pool.QueryRow(ctx, `SELECT triage_status,ai_run_id::text FROM automation_batches WHERE id=$1`, batch).Scan(&status, &id); e != nil || id != nil || status != "fallback" {
				t.Fatal(status, id, e)
			}
		})
	}
}

func TestTriageInFlightRevocationAndWorker(t *testing.T) {
	s, h, c, csrf, ws, _, _, batch := triageFixture(t, "openai")
	id := admitTriage(t, s, ws, batch)
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
		return nil, r.Context().Err()
	})}}
	ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	if e := s.StartJobs(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := s.StopJobs(c); e != nil {
			t.Error(e)
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("AI worker did not dispatch")
	}
	requireCode(t, call(h, "POST", "/api/v1/ai/runs/"+id+"/cancel", map[string]any{}, c, csrf, s.cfg.Origin), 200)
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("provider transport did not cancel")
	}
	for {
		var state, accounting string
		var payload bool
		if e := s.pool.QueryRow(ctx, `SELECT state,accounting,payload IS NOT NULL FROM ai_runs WHERE id=$1`, id).Scan(&state, &accounting, &payload); e != nil {
			t.Fatal(e)
		}
		if state == "cancelled" && accounting == "unknown" && !payload {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("cancelled request did not settle conservatively")
		case <-time.After(30 * time.Millisecond):
		}
	}
	automationState(t, s, batch, "recommended")
	_ = ws
}
