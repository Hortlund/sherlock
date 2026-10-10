package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"sherlock/internal/ai"
	"sync"
	"testing"
	"time"
)

// Reproduce the three-way cycle through real admission/worker and service-edit
// paths. The accounting transaction models an AI settings save's final audit
// insert, including its workspace FK. Observe actual lock waits before closing
// the cycle so this cannot pass merely because the goroutines ran sequentially.
func TestTriageScopeMappingAccountingConcurrency(t *testing.T) {
	for _, phase := range []string{"admission", "dispatch", "publication"} {
		t.Run(phase, func(t *testing.T) {
			s, h, c, csrf, ws, v, _, batch := triageFixture(t, "openai")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			var accounting pgx.Tx
			var workers sync.WaitGroup
			defer func() {
				cancel()
				if accounting != nil {
					_ = accounting.Rollback(context.Background())
				}
				workers.Wait()
			}()
			id := ""
			if phase == "admission" {
				automationDue(t, s, batch)
			} else {
				id = admitTriage(t, s, ws, batch)
			}
			done := make(chan error, 1)
			startWorker := func() {
				workers.Add(1)
				go func() {
					defer workers.Done()
					if phase == "admission" {
						done <- s.evaluateAutomation(ctx, automationArgs{ws, batch})
					} else {
						done <- s.executeAI(ctx, aiArgs{ws, id})
					}
				}()
			}
			entered, release := make(chan struct{}), make(chan struct{})
			if phase == "publication" {
				s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
					close(entered)
					select {
					case <-release:
						return fixtureAIResponse(r, "success")
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				})}}
				startWorker()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			var e error
			accounting, e = s.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if e = aiWorkspaceLock(ctx, accounting, ws); e != nil {
				t.Fatal(e)
			}
			var accountingPID int
			if e = accounting.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&accountingPID); e != nil {
				t.Fatal(e)
			}
			if phase == "publication" {
				close(release)
			} else {
				startWorker()
			}
			waitForWaiter := func(blocker int) int {
				t.Helper()
				for {
					var pid int
					if e := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, blocker).Scan(&pid); e != nil {
						t.Fatal(e)
					}
					if pid != 0 {
						return pid
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			workerPID := waitForWaiter(accountingPID)
			v.Name = "Renamed during triage"
			raw, _ := json.Marshal(serviceBody(v))
			req := httptest.NewRequest("POST", "/api/v1/services/"+v.ID, bytes.NewReader(raw)).WithContext(ctx)
			req.AddCookie(c)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", s.cfg.Origin)
			req.Header.Set("X-CSRF-Token", csrf)
			saved := make(chan *httptest.ResponseRecorder, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				saved <- w
			}()
			waitForWaiter(workerPID)
			if _, e = accounting.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.policy_saved')`, ws); e != nil {
				t.Fatal(e)
			}
			if e = accounting.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			requireCode(t, <-saved, 200)
			automationState(t, s, batch, "recommended")
			if phase == "admission" {
				if e = s.pool.QueryRow(ctx, `SELECT ai_run_id::text FROM automation_batches WHERE id=$1`, batch).Scan(&id); e != nil {
					t.Fatal(e)
				}
			}
			// Admission must still queue exactly one task, and a completed worker
			// must retain its report rather than become an unknown paid outcome.
			runAI(t, s, ws, id, "completed")
			var valid bool
			if e = s.pool.QueryRow(ctx, `SELECT report IS NOT NULL AND accounting='settled' FROM ai_runs WHERE id=$1`, id).Scan(&valid); e != nil || !valid {
				t.Fatal("triage result lost", valid, e)
			}
		})
	}
}

// Pause real worker transactions immediately after their validation, then prove
// a mapping/privacy save waits for the transaction to publish. This exercises
// both sides of provider HTTP without depending on a narrow timing window.
func TestAIManualScopeSerializesPrivacyEdits(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		for _, phase := range []string{"dispatch", "publication", "edit_first"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				s, h, c, csrf, ws, v := reviewFixture(t, 0)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				p := configureAI(t, s, h, c, csrf, kind, v.ID)
				s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { return fixtureAIResponse(r, "success") })}}
				rid := completedInvestigation(t, s, h, c, csrf, ws, v.ID)
				runAI(t, s, ws, queueAITest(t, s, h, c, csrf, p.ID), "completed")
				w := launchAI(t, s, h, c, csrf, p.ID, rid, false)
				requireCode(t, w, 202)
				id := bodyMap(t, w)["id"].(string)
				mapping := v.Mapping
				mapping.SensitivePatterns = []string{"review-private-pattern"}
				v.Mapping = mapping
				saveMapping := func() *httptest.ResponseRecorder {
					raw, _ := json.Marshal(serviceBody(v))
					req := httptest.NewRequest("POST", "/api/v1/services/"+v.ID, bytes.NewReader(raw)).WithContext(ctx)
					req.AddCookie(c)
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Origin", s.cfg.Origin)
					req.Header.Set("X-CSRF-Token", csrf)
					result := httptest.NewRecorder()
					h.ServeHTTP(result, req)
					return result
				}
				if phase == "edit_first" {
					// The edit commits while HTTP is running, before publication validation.
					s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
						requireCode(t, saveMapping(), 200)
						return fixtureAIResponse(r, "success")
					})}}
					runAI(t, s, ws, id, "cancelled")
					var content bool
					if e := s.pool.QueryRow(ctx, `SELECT report IS NOT NULL FROM ai_runs WHERE id=$1`, id).Scan(&content); e != nil || content {
						t.Fatal("revoked result published", content, e)
					}
					return
				}
				condition := "NEW.report IS NOT NULL"
				if phase == "dispatch" {
					condition = "OLD.state='queued' AND NEW.state='analyzing'"
				}
				_, e := s.pool.Exec(ctx, `CREATE FUNCTION scope_test_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.purpose='analysis' AND `+condition+` THEN PERFORM pg_advisory_xact_lock(hashtextextended(NEW.id::text,731909)); END IF; RETURN NEW; END $$; CREATE TRIGGER scope_test_barrier BEFORE UPDATE ON ai_runs FOR EACH ROW EXECUTE FUNCTION scope_test_barrier()`)
				if e != nil {
					t.Fatal(e)
				}
				guard, e := s.pool.Acquire(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer guard.Release()
				if _, e = guard.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,731909))`, id); e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() { done <- s.executeAI(ctx, aiArgs{ws, id}) }()
				var saved chan *httptest.ResponseRecorder
				// Release the barrier and drain both goroutines even if an assertion fails.
				defer func() {
					_, _ = guard.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1,731909))`, id)
					if e := <-done; e != nil {
						t.Error(e)
					}
					if saved != nil {
						requireCode(t, <-saved, 200)
					}
				}()
				var workerPID int
				for workerPID == 0 {
					e = s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, int(guard.Conn().PgConn().PID())).Scan(&workerPID)
					if e != nil {
						t.Fatal(e)
					}
					if workerPID == 0 {
						select {
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				saved = make(chan *httptest.ResponseRecorder, 1)
				go func() { saved <- saveMapping() }()
				for {
					var blocked bool
					e = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, workerPID).Scan(&blocked)
					if e != nil {
						t.Fatal(e)
					}
					if blocked {
						break
					}
					select {
					case result := <-saved:
						saved <- result
						t.Fatalf("privacy edit completed before %s transaction committed: HTTP %d", phase, result.Code)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
			})
		}
	}
}
