package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sherlock/internal/ai"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func automaticFixture(t *testing.T, kind string, triage bool) (*Server, http.Handler, *http.Cookie, string, string, Service, AIProvider, string) {
	t.Helper()
	s, h, c, csrf, ws, v, p, batch := triageFixture(t, kind)
	w := call(h, "POST", "/api/v1/ai/providers/"+p.ID+"/test", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin)
	requireCode(t, w, 202)
	runAI(t, s, ws, bodyMap(t, w)["id"].(string), "completed")
	policy, e := loadAutomationPolicy(context.Background(), s.pool, ws)
	if e != nil {
		t.Fatal(e)
	}
	policy.Mode = "auto_investigate"
	policy.InvestigationProviderID = p.ID
	policy.AutomaticConsent = true
	if !triage {
		policy.TriageProviderID = ""
	}
	policy = saveAutomationTest(t, s, h, c, csrf, policy)
	if _, e = s.pool.Exec(context.Background(), `UPDATE automation_batches SET policy_revision=$2 WHERE id=$1`, batch, policy.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(context.Background(), `UPDATE automation_checkpoints SET policy_revision=$1`, policy.Revision); e != nil {
		t.Fatal(e)
	}
	automationDue(t, s, batch)
	if e = s.evaluateAutomation(context.Background(), automationArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	return s, h, c, csrf, ws, v, p, batch
}
func automaticState(t *testing.T, s *Server, batch, want string) string {
	t.Helper()
	var state, reason, id string
	if e := s.pool.QueryRow(context.Background(), `SELECT admission_status,admission_reason,COALESCE(automatic_ai_run_id::text,'') FROM automation_batches WHERE id=$1`, batch).Scan(&state, &reason, &id); e != nil {
		t.Fatal(e)
	}
	if state != want {
		t.Fatalf("admission=%s: %s; want %s", state, reason, want)
	}
	return id
}
func TestAutomaticAdmissionProvidersReplayAndAccounting(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, h, c, _, ws, _, _, batch := automaticFixture(t, kind, false)
			ctx := context.Background()
			// Concurrent workers, then a new server instance, must reuse the durable decision.
			var wg sync.WaitGroup
			errs := make(chan error, 3)
			for range 3 {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- s.admitAutomatic(ctx, automaticArgs{ws, batch}) }()
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil {
					t.Fatal(e)
				}
			}
			id := automaticState(t, s, batch, "launched")
			restarted := *s
			if e := restarted.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			var runs, analyses, jobs int
			var next *time.Time
			if e := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runs WHERE kind='investigation'),(SELECT count(*) FROM ai_runs WHERE purpose='automatic'),(SELECT count(*) FROM river_job WHERE kind='ai_analysis' AND args->>'id'=$1),(SELECT next_investigation_at FROM automation_services LIMIT 1)`, id).Scan(&runs, &analyses, &jobs, &next); e != nil || runs != 1 || analyses != 1 || jobs != 1 || next == nil {
				t.Fatal(runs, analyses, jobs, next, e)
			}
			var calls atomic.Int32
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return fixtureAIResponse(r, "success") })}}
			runAI(t, s, ws, id, "completed")
			runAI(t, s, ws, id, "completed")
			if calls.Load() != 1 {
				t.Fatal("paid replay", calls.Load())
			}
			w := call(h, "GET", "/api/v1/automation/activity", nil, c, "", "")
			requireCode(t, w, 200)
			if !strings.Contains(w.Body.String(), `"status":"launched"`) || !strings.Contains(w.Body.String(), `"accounting":"settled"`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}
func TestAutomaticAdmissionTriagePolicy(t *testing.T) {
	for _, mode := range []string{"success", "observe", "refused", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, _, ws, _, _, batch := automaticFixture(t, "openai", true)
			ctx := context.Background()
			if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e == nil {
				t.Fatal("must wait for triage")
			}
			automaticState(t, s, batch, "pending")
			var id string
			if e := s.pool.QueryRow(ctx, `SELECT ai_run_id::text FROM automation_batches WHERE id=$1`, batch).Scan(&id); e != nil {
				t.Fatal(e)
			}
			if mode == "cancelled" {
				if _, e := s.pool.Exec(ctx, `UPDATE ai_runs SET state='cancelled',accounting='settled' WHERE id=$1`, id); e != nil {
					t.Fatal(e)
				}
			} else {
				s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { return fixtureAIResponse(r, mode) })}}
				if e := s.executeAI(ctx, aiArgs{ws, id}); e != nil {
					t.Fatal(e)
				}
			}
			if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			want := "launched"
			if mode == "observe" || mode == "cancelled" {
				want = "skipped"
			}
			automaticState(t, s, batch, want)
			var events int
			if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events WHERE kind IN ('finding.created','finding.escalated')`).Scan(&events); e != nil || events == 0 {
				t.Fatal("factual alerts suppressed", events, e)
			}
		})
	}
}
func TestAutomaticRevocationAndAdmissionGates(t *testing.T) {
	for _, boundary := range []string{"admission", "dispatch"} {
		for _, scenario := range []string{"policy", "consent", "monitoring", "mapping", "snooze", "dismiss", "source", "evidence", "recovery", "privacy", "provider", "detector", "demo", "service"} {
			t.Run(boundary+"/"+scenario, func(t *testing.T) {
				s, _, _, _, ws, _, _, batch := automaticFixture(t, "openai", false)
				ctx := context.Background()
				id := ""
				if boundary == "dispatch" {
					if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
						t.Fatal(e)
					}
					id = automaticState(t, s, batch, "launched")
				}
				statements := map[string]string{
					"policy":     `UPDATE automation_policy SET revision=revision+1`,
					"consent":    `UPDATE automation_policy SET config=jsonb_set(config,'{automatic_consent}','false')`,
					"monitoring": `UPDATE services SET monitoring=false`,
					"mapping":    `UPDATE services SET revision=revision+1`,
					"snooze":     `UPDATE findings SET state='snoozed'`,
					"dismiss":    `UPDATE findings SET state='dismissed'`,
					"source":     `UPDATE connections SET enabled=false`,
					"evidence":   `UPDATE evidence SET expires_at=now()-interval '1 second'`,
					"recovery":   `UPDATE automation_items SET cleared_at=now()`,
					"privacy":    `UPDATE ai_policy SET enabled=false,revision=revision+1`,
					"provider":   `UPDATE ai_providers SET config=jsonb_set(config,'{enabled}','false')`,
					"detector":   `INSERT INTO workspace_settings(workspace_id,settings) SELECT workspace_id,jsonb_set(details->'thresholds','{disabled}','["error_rate","new_exception","warning_burst"]') FROM findings LIMIT 1 ON CONFLICT(workspace_id) DO UPDATE SET settings=EXCLUDED.settings`,
					"demo":       `UPDATE automation_policy SET config=jsonb_set(config,'{allow_demo}','false')`,
					"service":    `UPDATE automation_policy SET config=jsonb_set(config,'{services}','[]')`,
				}
				if _, e := s.pool.Exec(ctx, statements[scenario]); e != nil {
					t.Fatal(e)
				}
				s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(*http.Request) (*http.Response, error) {
					t.Error("revoked automatic work reached provider")
					return nil, context.Canceled
				})}}
				if boundary == "admission" {
					if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
						t.Fatal(e)
					}
					automaticState(t, s, batch, "skipped")
				} else {
					runAI(t, s, ws, id, "cancelled")
				}
			})
		}
	}
}
func TestAutomaticBudgetCooldownAndNoOptIn(t *testing.T) {
	for _, scenario := range []string{"budget", "unknown", "cooldown", "active", "expired", "exhausted", "off", "triage_only"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, _, ws, v, p, batch := automaticFixture(t, "openai", false)
			ctx := context.Background()
			query := map[string]string{
				"budget":      `UPDATE ai_policy SET daily_limit=1`,
				"unknown":     `UPDATE ai_runs SET accounting='unknown',reserved=100000000000 WHERE purpose='test'`,
				"cooldown":    `UPDATE automation_services SET next_investigation_at=now()+interval '1 hour'`,
				"active":      `UPDATE ai_runs SET state='queued',service_id=$1::uuid WHERE purpose='test' AND provider_id=$2::uuid`,
				"expired":     `UPDATE automation_batches SET created_at=now()-interval '16 minutes'`,
				"exhausted":   `UPDATE river_job SET state='discarded',finalized_at=now() WHERE kind='automatic_admission'`,
				"off":         `UPDATE automation_policy SET config=jsonb_set(config,'{mode}','"off"')`,
				"triage_only": `UPDATE automation_policy SET config=jsonb_set(config,'{mode}','"triage_only"')`,
			}[scenario]
			var e error
			if scenario == "active" {
				_, e = s.pool.Exec(ctx, query, v.ID, p.ID)
			} else {
				_, e = s.pool.Exec(ctx, query)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			automaticState(t, s, batch, "skipped")
			var n int
			if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE kind='investigation'`).Scan(&n); e != nil || n != 0 {
				t.Fatal("partial admission", n, e)
			}
		})
	}
}
func TestAutomaticPolicyRequiresExplicitConsent(t *testing.T) {
	s, h, c, csrf, ws, _, _, _ := triageFixture(t, "openai")
	p, e := loadAutomationPolicy(context.Background(), s.pool, ws)
	if e != nil {
		t.Fatal(e)
	}
	p.Mode = "auto_investigate"
	p.InvestigationProviderID = p.TriageProviderID
	requireCode(t, call(h, "POST", "/api/v1/automation/policy", p, c, csrf, s.cfg.Origin), 400)
	p.AutomaticConsent = true
	// Triage capability alone is insufficient.
	requireCode(t, call(h, "POST", "/api/v1/automation/policy", p, c, csrf, s.cfg.Origin), 400)
	var before map[string]any
	w := call(h, "GET", "/api/v1/automation/policy", nil, c, "", "")
	requireCode(t, w, 200)
	if e = json.Unmarshal(w.Body.Bytes(), &before); e != nil {
		t.Fatal(e)
	}
	if before["mode"] != "triage_only" {
		t.Fatal("invalid opt-in persisted")
	}
}

func TestAutomaticPublicationRevocation(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, h, c, csrf, ws, _, _, batch := automaticFixture(t, kind, false)
			ctx := context.Background()
			if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			id := automaticState(t, s, batch, "launched")
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
				policy, e := loadAutomationPolicy(ctx, s.pool, ws)
				if e != nil {
					return nil, e
				}
				policy.Mode = "triage_only"
				saveAutomationTest(t, s, h, c, csrf, policy)
				return fixtureAIResponse(r, "success")
			})}}
			runAI(t, s, ws, id, "cancelled")
			var report []byte
			var accounting string
			if e := s.pool.QueryRow(ctx, `SELECT report,accounting FROM ai_runs WHERE id=$1`, id).Scan(&report, &accounting); e != nil || len(report) != 0 || accounting != "settled" {
				t.Fatal("revoked content published or usage lost", string(report), accounting, e)
			}
		})
	}
}

func TestAutomaticWorkerAndRollback(t *testing.T) {
	s, _, _, _, ws, _, _, batch := automaticFixture(t, "openai", false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A failure after report creation must leave no report, reservation, or cooldown.
	_, e := s.pool.Exec(ctx, `CREATE FUNCTION automatic_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.purpose='automatic' THEN RAISE EXCEPTION 'test rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER automatic_test_failure BEFORE INSERT ON ai_runs FOR EACH ROW EXECUTE FUNCTION automatic_test_failure()`)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.admitAutomatic(ctx, automaticArgs{ws, batch}); e == nil {
		t.Fatal("injected failure not returned")
	}
	automaticState(t, s, batch, "pending")
	var count int
	if e = s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runs WHERE kind='investigation')+(SELECT count(*) FROM automation_services WHERE next_investigation_at IS NOT NULL)`).Scan(&count); e != nil || count != 0 {
		t.Fatal("partial transaction", count, e)
	}
	if _, e = s.pool.Exec(ctx, "DROP TRIGGER automatic_test_failure ON ai_runs; DROP FUNCTION automatic_test_failure()"); e != nil {
		t.Fatal(e)
	}
	if e = s.admitAutomatic(ctx, automaticArgs{"00000000-0000-0000-0000-000000000001", batch}); e != nil {
		t.Fatal(e)
	}
	automaticState(t, s, batch, "pending")
	if e = s.StartJobs(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() {
		stop, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if e := s.StopJobs(stop); e != nil {
			t.Error(e)
		}
	}()
	for {
		var state string
		if e = s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT state FROM ai_runs WHERE id=b.automatic_ai_run_id),'') FROM automation_batches b WHERE id=$1`, batch).Scan(&state); e != nil {
			t.Fatal(e)
		}
		if state == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("River automatic pipeline did not complete", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	automaticState(t, s, batch, "launched")
}

func TestAutomaticRecoveryAndSkippedEvaluation(t *testing.T) {
	for _, boundary := range []string{"pending", "queued"} {
		t.Run(boundary, func(t *testing.T) {
			s, _, _, _, ws, v, _, batch := automaticFixture(t, "openai", false)
			ctx := context.Background()
			id := ""
			if boundary == "queued" {
				if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
					t.Fatal(e)
				}
				id = automaticState(t, s, batch, "launched")
			}
			candidate, at := automationCandidate(t, s, v)
			result := healthyAutomationResult(at.Add(time.Minute), true)
			if e := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, at.Add(time.Minute), candidate.Thresholds, result.Candidates, result.Coverage...); e != nil {
				t.Fatal(e)
			}
			if boundary == "pending" {
				if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
					t.Fatal(e)
				}
				automaticState(t, s, batch, "skipped")
				var reason string
				if e := s.pool.QueryRow(ctx, `SELECT admission_reason FROM automation_batches WHERE id=$1`, batch).Scan(&reason); e != nil || !strings.Contains(reason, "newer complete scan") {
					t.Fatal(reason, e)
				}
			} else {
				runAI(t, s, ws, id, "cancelled")
			}
			// A new meaningful change inside the recommendation cooldown is visibly
			// skipped, not mislabeled as automatic mode being disabled.
			candidate.Current *= 2
			publishAutomation(t, s, ws, v, candidate, at.Add(2*time.Minute))
			newer := pendingAutomation(t, s)
			automationDue(t, s, newer)
			if e := s.evaluateAutomation(ctx, automationArgs{ws, newer}); e != nil {
				t.Fatal(e)
			}
			automaticState(t, s, newer, "skipped")
		})
	}
}
