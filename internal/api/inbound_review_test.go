package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sherlock/internal/ai"
	"sherlock/internal/inbound"
)

func TestInboundMonitoringRevocationDoesNotReplay(t *testing.T) {
	for _, phase := range []string{"queued", "collecting", "collected", "ai_queued"} {
		t.Run(phase, func(t *testing.T) {
			s, h, c, csrf, ws, v, p, _ := deepFixture(t, "openai")
			ctx := context.Background()
			enableInboundAutomation(t, s, h, c, csrf, ws, v, p, false)
			b := createInboundTest(t, s, h, c, csrf, v, true)
			w := sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "monitoring-revoked"))
			requireCode(t, w, 202)
			id := bodyMap(t, w)["id"].(string)
			run, aid := "", ""
			if phase != "queued" {
				if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
					t.Fatal(e)
				}
				run = inboundState(t, s, id, "collecting")
			}
			if phase == "collected" || phase == "ai_queued" {
				if e := s.execute(ctx, runArgs{ws, run}); e != nil {
					t.Fatal(e)
				}
			}
			if phase == "ai_queued" {
				if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
					t.Fatal(e)
				}
				if e := s.pool.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE inbound_receipt_id=$1`, id).Scan(&aid); e != nil {
					t.Fatal(e)
				}
			}
			for _, enabled := range []bool{false, true} {
				v.Monitoring = enabled
				requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), c, csrf, s.cfg.Origin), 200)
			}
			// No worker/watcher or retention pass ran between the two saves.
			inboundState(t, s, id, "cancelled")
			s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
				t.Error("revoked receipt dispatched provider HTTP")
				return fixtureAIResponse(r, "success")
			})}}
			if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
				t.Fatal(e)
			}
			if run != "" {
				if e := s.execute(ctx, runArgs{ws, run}); e != nil {
					t.Fatal(e)
				}
			}
			if aid != "" {
				deepStep(t, s, ws, aid, 1)
				assertAIState(t, s, aid, "cancelled")
			}
			inboundState(t, s, id, "cancelled")
		})
	}
}

type inboundDispatchBarrier struct {
	entered, release chan struct{}
	prefix           string
	once             sync.Once
	pid              atomic.Int32
}

func (b *inboundDispatchBarrier) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	prefix := b.prefix
	if prefix == "" {
		prefix = `SELECT state='analyzing' AND NOT cancel_requested`
	}
	if strings.HasPrefix(data.SQL, prefix) {
		b.once.Do(func() {
			b.pid.Store(int32(conn.PgConn().PID()))
			close(b.entered)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*inboundDispatchBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestInboundMonitoringRevocationBeforeProviderHTTP(t *testing.T) {
	s, h, c, csrf, ws, v, p, _ := deepFixture(t, "openai")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	enableInboundAutomation(t, s, h, c, csrf, ws, v, p, false)
	b := createInboundTest(t, s, h, c, csrf, v, true)
	w := sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "dispatch-revocation"))
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	run := inboundState(t, s, id, "collecting")
	if e := s.execute(ctx, runArgs{ws, run}); e != nil {
		t.Fatal(e)
	}
	if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	var aid string
	if e := s.pool.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE inbound_receipt_id=$1`, id).Scan(&aid); e != nil {
		t.Fatal(e)
	}
	barrier := &inboundDispatchBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = barrier
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	worker := *s
	worker.pool = pool
	var calls atomic.Int32
	worker.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return fixtureAIResponse(r, "success") })}}
	done := make(chan error, 1)
	go func() { done <- worker.executeDeep(ctx, deepArgs{ws, aid, 1}) }()
	defer func() {
		close(barrier.release)
		if e := <-done; e != nil {
			t.Error(e)
		}
		if calls.Load() != 0 {
			t.Error("monitoring revocation committed before final check, but provider HTTP was sent")
		}
		assertAIState(t, s, aid, "cancelled")
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, enabled := range []bool{false, true} {
		v.Monitoring = enabled
		requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), c, csrf, s.cfg.Origin), 200)
	}
}

func TestInboundInvalidTitleIsClientError(t *testing.T) {
	s, h, c, csrf, _, v := reviewFixture(t, 0)
	b := createInboundTest(t, s, h, c, csrf, v, true)
	var a inbound.Alert
	if e := json.Unmarshal(inboundBody(t, "invalid-title"), &a); e != nil {
		t.Fatal(e)
	}
	a.Title = "embedded\x00NUL"
	body, e := json.Marshal(a)
	if e != nil {
		t.Fatal(e)
	}
	requireCode(t, sendInbound(h, b.ID, inboundSecret, "", body), 400)
}

func TestInboundMonitoringRevocationBeforeTelemetryHTTP(t *testing.T) {
	s, h, c, csrf, ws, v, p, _ := deepFixture(t, "openai")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	enableInboundAutomation(t, s, h, c, csrf, ws, v, p, false)
	b := createInboundTest(t, s, h, c, csrf, v, true)
	w := sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "telemetry-revocation"))
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	run := inboundState(t, s, id, "collecting")
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer source.Close()
	if _, e := s.pool.Exec(ctx, `UPDATE connections SET base_url=$1 WHERE workspace_id=$2`, source.URL, ws); e != nil {
		t.Fatal(e)
	}
	barrier := &inboundDispatchBarrier{entered: make(chan struct{}), release: make(chan struct{}), prefix: `UPDATE runs SET requests=requests+1`}
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = barrier
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	worker := *s
	worker.pool = pool
	done := make(chan error, 1)
	go func() { done <- worker.execute(ctx, runArgs{ws, run}) }()
	defer func() {
		close(barrier.release)
		if e := <-done; e != nil {
			t.Error(e)
		}
		if calls.Load() != 0 {
			t.Error("telemetry HTTP dispatched after revocation")
		}
		inboundState(t, s, id, "cancelled")
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, enabled := range []bool{false, true} {
		v.Monitoring = enabled
		requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), c, csrf, s.cfg.Origin), 200)
	}
}

func TestInboundMonitoringCancellationLockOrder(t *testing.T) {
	s, h, c, csrf, ws, v, p, _ := deepFixture(t, "openai")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	enableInboundAutomation(t, s, h, c, csrf, ws, v, p, false)
	b := createInboundTest(t, s, h, c, csrf, v, true)
	w := sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "lock-order"))
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	// A provider editor can own accounting before it inserts an audit row whose
	// FK needs workspace KEY SHARE. Monitoring owns workspace FOR UPDATE first.
	accounting, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer accounting.Rollback(context.Background())
	if e = aiWorkspaceLock(ctx, accounting, ws); e != nil {
		t.Fatal(e)
	}
	barrier := &inboundDispatchBarrier{entered: make(chan struct{}), release: make(chan struct{}), prefix: `UPDATE inbound_receipts x SET state='cancelled'`}
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = barrier
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	editor := *s
	editor.pool = pool
	v.Monitoring = false
	raw, _ := json.Marshal(serviceBody(v))
	r := httptest.NewRequest("POST", "/api/v1/services/"+v.ID, strings.NewReader(string(raw))).WithContext(ctx)
	r.AddCookie(c)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", s.cfg.Origin)
	r.Header.Set("X-CSRF-Token", csrf)
	saved := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); editor.Handler().ServeHTTP(w, r); saved <- w }()
	var release sync.Once
	savedDone := false
	defer func() {
		release.Do(func() { close(barrier.release) })
		if !savedDone {
			requireCode(t, <-saved, 200)
		}
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	audit := make(chan error, 1)
	go func() {
		_, e := accounting.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'inbound.review_accounting')`, ws)
		audit <- e
	}()
	waitForDBWaiter(t, ctx, s, int(barrier.pid.Load()))
	// Release the workspace edit while accounting remains held. There must be no
	// reverse dependency from receipt cancellation back to accounting/run rows.
	release.Do(func() { close(barrier.release) })
	response := <-saved
	savedDone = true
	requireCode(t, response, 200)
	if e := <-audit; e != nil {
		t.Fatal(e)
	}
	if e := accounting.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	inboundState(t, s, id, "cancelled")
}

func TestInboundAIConsentRevocationBeforeTelemetryHTTP(t *testing.T) {
	for _, change := range []string{"policy_off", "policy_off_on", "provider_disabled", "provider_deleted"} {
		t.Run(change, func(t *testing.T) {
			s, h, c, csrf, ws, v, p, _ := deepFixture(t, "openai")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			enableInboundAutomation(t, s, h, c, csrf, ws, v, p, false)
			b := createInboundTest(t, s, h, c, csrf, v, true)
			w := sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "ai-consent-revocation"))
			requireCode(t, w, 202)
			id := bodyMap(t, w)["id"].(string)
			if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
				t.Fatal(e)
			}
			run := inboundState(t, s, id, "collecting")
			var calls atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer source.Close()
			if _, e := s.pool.Exec(ctx, `UPDATE connections SET base_url=$1 WHERE workspace_id=$2`, source.URL, ws); e != nil {
				t.Fatal(e)
			}
			// Pause after inboundScope succeeds, immediately before the final SQL
			// request guard. Revoke through the real configuration endpoints.
			barrier := &inboundDispatchBarrier{entered: make(chan struct{}), release: make(chan struct{}), prefix: `UPDATE runs SET requests=requests+1`}
			cfg := s.pool.Config()
			cfg.ConnConfig.Tracer = barrier
			pool, e := pgxpool.NewWithConfig(ctx, cfg)
			if e != nil {
				t.Fatal(e)
			}
			defer pool.Close()
			worker := *s
			worker.pool = pool
			done := make(chan error, 1)
			go func() { done <- worker.execute(ctx, runArgs{ws, run}) }()
			defer func() {
				close(barrier.release)
				if e := <-done; e != nil {
					t.Error(e)
				}
				if calls.Load() != 0 {
					t.Error("telemetry HTTP dispatched after AI consent revocation")
				}
				var requests, aiRuns int
				if e := s.pool.QueryRow(ctx, `SELECT requests,(SELECT count(*) FROM ai_runs WHERE inbound_receipt_id=$2) FROM runs WHERE id=$1`, run, id).Scan(&requests, &aiRuns); e != nil || requests != 0 || aiRuns != 0 {
					t.Error("revoked collection consumed attempts or admitted AI", requests, aiRuns, e)
				}
				inboundState(t, s, id, "cancelled")
			}()
			select {
			case <-barrier.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			switch change {
			case "policy_off", "policy_off_on":
				ap, e := loadAIPolicy(ctx, s.pool, ws)
				if e != nil {
					t.Fatal(e)
				}
				ap.Enabled = false
				w := call(h, "POST", "/api/v1/ai/policy", ap, c, csrf, s.cfg.Origin)
				requireCode(t, w, 200)
				if change == "policy_off_on" {
					if e := json.Unmarshal(w.Body.Bytes(), &ap); e != nil {
						t.Fatal(e)
					}
					ap.Enabled = true
					requireCode(t, call(h, "POST", "/api/v1/ai/policy", ap, c, csrf, s.cfg.Origin), 200)
				}
			case "provider_disabled":
				p.Enabled = false
				requireCode(t, call(h, "POST", "/api/v1/ai/providers/"+p.ID, map[string]any{"provider": p}, c, csrf, s.cfg.Origin), 200)
			case "provider_deleted":
				requireCode(t, call(h, "POST", "/api/v1/ai/providers/"+p.ID+"/delete", map[string]any{}, c, csrf, s.cfg.Origin), 200)
			}
		})
	}
}

func TestInboundMonitoringStopPreservesCompletedResults(t *testing.T) {
	s, h, c, csrf, ws, v, p, _ := deepFixture(t, "openai")
	ctx := context.Background()
	enableInboundAutomation(t, s, h, c, csrf, ws, v, p, false)
	b := createInboundTest(t, s, h, c, csrf, v, true)
	w := sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "completed-before-stop"))
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	run := inboundState(t, s, id, "collecting")
	if e := s.execute(ctx, runArgs{ws, run}); e != nil {
		t.Fatal(e)
	}
	if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	var aid string
	if e := s.pool.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE inbound_receipt_id=$1`, id).Scan(&aid); e != nil {
		t.Fatal(e)
	}
	deepStep(t, s, ws, aid, 1)
	assertAIState(t, s, aid, "completed")
	v.Monitoring = false
	requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), c, csrf, s.cfg.Origin), 200)
	inboundState(t, s, id, "ai_queued")
	assertAIState(t, s, aid, "completed")
	var retained bool
	if e := s.pool.QueryRow(ctx, `SELECT report IS NOT NULL AND charged=260 AND accounting='settled' FROM ai_runs WHERE id=$1`, aid).Scan(&retained); e != nil || !retained {
		t.Fatal(retained, e)
	}
	w = sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "arrived-while-monitoring-off"))
	requireCode(t, w, 202)
	offID := bodyMap(t, w)["id"].(string)
	v.Monitoring = true
	requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), c, csrf, s.cfg.Origin), 200)
	if e := s.admitInbound(ctx, inboundArgs{ws, offID}); e != nil {
		t.Fatal(e)
	}
	inboundState(t, s, offID, "skipped")
}

func TestInboundMonitoringRevocationBeforeToolHTTP(t *testing.T) {
	s, h, c, csrf, ws, v, p, _ := deepFixture(t, "openai")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	enableInboundAutomation(t, s, h, c, csrf, ws, v, p, true)
	b := createInboundTest(t, s, h, c, csrf, v, true)
	w := sendInbound(h, b.ID, inboundSecret, "", inboundBody(t, "tool-revocation"))
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	run := inboundState(t, s, id, "collecting")
	if e := s.execute(ctx, runArgs{ws, run}); e != nil {
		t.Fatal(e)
	}
	if e := s.admitInbound(ctx, inboundArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	var aid string
	if e := s.pool.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE inbound_receipt_id=$1`, id).Scan(&aid); e != nil {
		t.Fatal(e)
	}
	s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) { return fixtureAIResponse(r, "tool:loki_patterns") })}}
	deepStep(t, s, ws, aid, 1)
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer source.Close()
	if _, e := s.pool.Exec(ctx, `UPDATE connections SET base_url=$1 WHERE workspace_id=$2`, source.URL, ws); e != nil {
		t.Fatal(e)
	}
	barrier := &inboundDispatchBarrier{entered: make(chan struct{}), release: make(chan struct{}), prefix: `UPDATE ai_runs SET telemetry_attempts=telemetry_attempts+1`}
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = barrier
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	worker := *s
	worker.pool = pool
	done := make(chan error, 1)
	go func() { done <- worker.executeDeep(ctx, deepArgs{ws, aid, 2}) }()
	defer func() {
		close(barrier.release)
		if e := <-done; e != nil {
			t.Error(e)
		}
		if calls.Load() != 0 {
			t.Error("automatic tool HTTP dispatched after monitoring revocation")
		}
		assertAIState(t, s, aid, "cancelled")
		var retained bool
		if e := s.pool.QueryRow(ctx, `SELECT charged=260 AND accounting='settled' AND telemetry_attempts=0 FROM ai_runs WHERE id=$1`, aid).Scan(&retained); e != nil || !retained {
			t.Error("usage or denied telemetry count changed", retained, e)
		}
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, enabled := range []bool{false, true} {
		v.Monitoring = enabled
		requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), c, csrf, s.cfg.Origin), 200)
	}
}
