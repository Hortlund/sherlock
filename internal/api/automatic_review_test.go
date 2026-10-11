package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"sherlock/internal/ai"
	"sync"
	"testing"
	"time"
)

func TestAutomaticLateTriageCancellation(t *testing.T) {
	for _, action := range []string{"cancel", "delete"} {
		for _, stage := range []string{"pending", "admitted", "inflight"} {
			t.Run(action+"/"+stage, func(t *testing.T) {
				s, h, c, csrf, ws, _, _, batch := automaticFixture(t, "openai", true)
				ctx := context.Background()
				var triage string
				if e := s.pool.QueryRow(ctx, `SELECT ai_run_id::text FROM automation_batches WHERE id=$1`, batch).Scan(&triage); e != nil {
					t.Fatal(e)
				}
				runAI(t, s, ws, triage, "completed")
				id := ""
				if stage != "pending" {
					if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
						t.Fatal(e)
					}
					id = automaticState(t, s, batch, "launched")
				}
				if stage == "inflight" {
					s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
						requireCode(t, call(h, "POST", "/api/v1/ai/runs/"+triage+"/"+action, map[string]any{}, c, csrf, s.cfg.Origin), 200)
						return fixtureAIResponse(r, "success")
					})}}
					runAI(t, s, ws, id, "cancelled")
					var withheld bool
					if e := s.pool.QueryRow(ctx, `SELECT report IS NULL AND accounting='settled' AND charged>0 FROM ai_runs WHERE id=$1`, id).Scan(&withheld); e != nil || !withheld {
						t.Fatal("cancelled result published or usage lost", withheld, e)
					}
					return
				}
				requireCode(t, call(h, "POST", "/api/v1/ai/runs/"+triage+"/"+action, map[string]any{}, c, csrf, s.cfg.Origin), 200)
				s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(*http.Request) (*http.Response, error) {
					t.Error("cancelled triage dispatched downstream automatic work")
					return nil, context.Canceled
				})}}
				if stage == "admitted" {
					assertAIState(t, s, id, "cancelled")
					runAI(t, s, ws, id, "cancelled")
				} else {
					if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
						t.Fatal(e)
					}
					automaticState(t, s, batch, "skipped")
				}
			})
		}
	}
}

// Queue actual admission and HTTP revocation behind a held accounting lock.
// Observed database waits force both orders, including policy-before-accounting.
func TestAutomaticAdmissionRevocationConcurrency(t *testing.T) {
	for _, action := range []string{"cancel", "off"} {
		for _, first := range []string{"admission", "revocation"} {
			t.Run(action+"/"+first+"_first", func(t *testing.T) {
				s, h, c, csrf, ws, _, _, batch := automaticFixture(t, "openai", true)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				var triage string
				if e := s.pool.QueryRow(ctx, `SELECT ai_run_id::text FROM automation_batches WHERE id=$1`, batch).Scan(&triage); e != nil {
					t.Fatal(e)
				}
				runAI(t, s, ws, triage, "completed")
				p, e := loadAutomationPolicy(ctx, s.pool, ws)
				if e != nil {
					t.Fatal(e)
				}
				p.Mode = "off"
				guard, e := s.pool.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer guard.Rollback(context.Background())
				if e = aiWorkspaceLock(ctx, guard, ws); e != nil {
					t.Fatal(e)
				}
				var pid int
				if e = guard.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				admitted := make(chan error, 1)
				revoked := make(chan *httptest.ResponseRecorder, 1)
				startAdmission := func() {
					go func() { admitted <- s.admitAutomatic(ctx, automaticArgs{ws, batch}) }()
					t.Cleanup(func() {
						_ = guard.Rollback(context.Background())
						if err := <-admitted; err != nil {
							t.Error(err)
						}
					})
				}
				startRevocation := func() {
					go func() {
						if action == "off" {
							revoked <- call(h, "POST", "/api/v1/automation/policy", p, c, csrf, s.cfg.Origin)
						} else {
							revoked <- call(h, "POST", "/api/v1/ai/runs/"+triage+"/cancel", map[string]any{}, c, csrf, s.cfg.Origin)
						}
					}()
					t.Cleanup(func() {
						_ = guard.Rollback(context.Background())
						requireCode(t, <-revoked, 200)
					})
				}
				if first == "admission" {
					startAdmission()
				} else {
					startRevocation()
				}
				worker := waitForDBWaiter(t, ctx, s, pid)
				if first == "admission" {
					startRevocation()
				} else {
					startAdmission()
				}
				waitForDBWaiter(t, ctx, s, worker)
				if e = guard.Commit(ctx); e != nil {
					t.Fatal(e)
				}
				// Observe completion before assertions; cleanup still drains on failure.
				err := <-admitted
				admitted <- err
				response := <-revoked
				revoked <- response
				if err != nil {
					t.Fatal(err)
				}
				requireCode(t, response, 200)
				if first == "revocation" {
					automaticState(t, s, batch, "skipped")
				} else {
					assertAIState(t, s, automaticState(t, s, batch, "launched"), "cancelled")
				}
			})
		}
	}
}

type automaticDispatchBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *automaticDispatchBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == `SELECT state='analyzing' FROM ai_runs WHERE workspace_id=$1 AND id=$2` {
		b.once.Do(func() {
			close(b.entered)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*automaticDispatchBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestAutomaticOffBetweenIntentAndHTTP(t *testing.T) {
	s, h, c, csrf, ws, _, _, batch := automaticFixture(t, "openai", false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	id := automaticState(t, s, batch, "launched")
	barrier := &automaticDispatchBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	config := s.pool.Config()
	config.ConnConfig.Tracer = barrier
	pool, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	worker := *s
	worker.pool = pool
	worker.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(*http.Request) (*http.Response, error) {
		t.Error("Off committed before final dispatch check but provider was called")
		return nil, context.Canceled
	})}}
	done := make(chan error, 1)
	go func() { done <- worker.executeAI(ctx, aiArgs{ws, id}) }()
	defer func() {
		close(barrier.release)
		if err := <-done; err != nil {
			t.Error(err)
		}
		assertAIState(t, s, id, "cancelled")
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertAIState(t, s, id, "analyzing")
	p, e := loadAutomationPolicy(ctx, s.pool, ws)
	if e != nil {
		t.Fatal(e)
	}
	p.Mode = "off"
	saveAutomationTest(t, s, h, c, csrf, p)
	assertAIState(t, s, id, "cancelled")
}

func waitForDBWaiter(t *testing.T, ctx context.Context, s *Server, blocker int) int {
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

func TestAutomaticConcurrentRetention(t *testing.T) {
	s, _, _, _, ws, _, _, batch := automaticFixture(t, "openai", false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	id := automaticState(t, s, batch, "launched")
	runAI(t, s, ws, id, "completed")
	if _, e := s.pool.Exec(ctx, `UPDATE automation_batches SET created_at=now()-interval '31 days'; UPDATE ai_runs SET created_at=now()-interval '31 days',expires_at=now()+interval '1 hour',payload=NULL`); e != nil {
		t.Fatal(e)
	}
	// Pause cleanup while it owns a batch row. A second replica starts AI
	// cleanup; circular SET NULL foreign keys used to deadlock these deletes.
	if _, e := s.pool.Exec(ctx, `CREATE FUNCTION retention_test_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(OLD.id::text,731909)); RETURN OLD; END $$; CREATE TRIGGER retention_test_barrier BEFORE DELETE ON automation_batches FOR EACH ROW EXECUTE FUNCTION retention_test_barrier()`); e != nil {
		t.Fatal(e)
	}
	guard, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer guard.Rollback(context.Background())
	if _, e = guard.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,731909))`, batch); e != nil {
		t.Fatal(e)
	}
	var pid int
	if e = guard.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	automationDone, aiDone := make(chan error, 1), make(chan error, 1)
	go func() { automationDone <- s.retainAutomation(ctx) }()
	defer func() {
		_ = guard.Rollback(context.Background())
		if err := <-automationDone; err != nil {
			t.Error(err)
		}
	}()
	worker := waitForDBWaiter(t, ctx, s, pid)
	go func() { aiDone <- s.retainAI(ctx) }()
	defer func() {
		_ = guard.Rollback(context.Background())
		if err := <-aiDone; err != nil {
			t.Error(err)
		}
	}()
	waitForDBWaiter(t, ctx, s, worker)
	if e = guard.Commit(ctx); e != nil {
		t.Fatal(e)
	}
}

func TestAutomaticPolicySaveCancelsImmediately(t *testing.T) {
	for _, state := range []string{"queued", "analyzing"} {
		t.Run(state, func(t *testing.T) {
			s, h, c, csrf, ws, _, _, batch := automaticFixture(t, "openai", false)
			ctx := context.Background()
			if e := s.admitAutomatic(ctx, automaticArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			id := automaticState(t, s, batch, "launched")
			if state == "analyzing" {
				// Persisted dispatch intent: the worker has committed and may be about to send HTTP.
				if _, e := s.pool.Exec(ctx, `UPDATE ai_runs SET state='analyzing',started_at=now(),accounting='unknown' WHERE id=$1`, id); e != nil {
					t.Fatal(e)
				}
			}
			p, e := loadAutomationPolicy(ctx, s.pool, ws)
			if e != nil {
				t.Fatal(e)
			}
			p.Mode = "off"
			saveAutomationTest(t, s, h, c, csrf, p)
			assertAIState(t, s, id, "cancelled")
			var accounting string
			if e = s.pool.QueryRow(ctx, `SELECT accounting FROM ai_runs WHERE id=$1`, id).Scan(&accounting); e != nil {
				t.Fatal(e)
			}
			want := "settled"
			if state == "analyzing" {
				want = "unknown"
			}
			if accounting != want {
				t.Fatalf("accounting=%s, want %s", accounting, want)
			}
		})
	}
}
