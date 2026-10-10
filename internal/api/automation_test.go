package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sherlock/internal/automation"
	"sherlock/internal/detectors"
	"strings"
	"testing"
	"time"
)

func automationFixture(t *testing.T, configure ...func(*automation.Policy)) (*Server, http.Handler, *http.Cookie, string, string, Service, automation.Policy, string) {
	t.Helper()
	s, h, c, csrf, ws, v := reviewFixture(t, 0)
	if _, e := s.pool.Exec(context.Background(), `UPDATE services SET monitoring=true WHERE id=$1`, v.ID); e != nil {
		t.Fatal(e)
	}
	v.Monitoring = true
	p := automation.Defaults()
	p.Mode = "triage_only"
	p.Services = []string{v.ID}
	p.AllowDemo = true
	for _, change := range configure {
		change(&p)
	}
	p = saveAutomationTest(t, s, h, c, csrf, p)
	w := call(h, "POST", "/api/v1/services/"+v.ID+"/scans", map[string]any{}, c, csrf, s.cfg.Origin)
	requireCode(t, w, 202)
	if e := s.execute(context.Background(), runArgs{ws, bodyMap(t, w)["id"].(string)}); e != nil {
		t.Fatal(e)
	}
	var batch string
	if e := s.pool.QueryRow(context.Background(), `SELECT id::text FROM automation_batches WHERE service_id=$1`, v.ID).Scan(&batch); e != nil {
		t.Fatal(e)
	}
	return s, h, c, csrf, ws, v, p, batch
}
func saveAutomationTest(t *testing.T, s *Server, h http.Handler, c *http.Cookie, csrf string, p automation.Policy) automation.Policy {
	t.Helper()
	w := call(h, "POST", "/api/v1/automation/policy", p, c, csrf, s.cfg.Origin)
	requireCode(t, w, 200)
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	return p
}
func automationDue(t *testing.T, s *Server, batch string) {
	t.Helper()
	if _, e := s.pool.Exec(context.Background(), `UPDATE automation_batches SET due_at=now()-interval '1 second' WHERE id=$1`, batch); e != nil {
		t.Fatal(e)
	}
}
func automationState(t *testing.T, s *Server, batch string, want string) {
	t.Helper()
	var state string
	if e := s.pool.QueryRow(context.Background(), `SELECT state FROM automation_batches WHERE id=$1`, batch).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state != want {
		t.Fatalf("state=%s, want %s", state, want)
	}
}
func automationCandidate(t *testing.T, s *Server, v Service) (detectors.Candidate, time.Time) {
	t.Helper()
	var raw []byte
	var at time.Time
	if e := s.pool.QueryRow(context.Background(), `SELECT details,last_seen FROM findings WHERE service_id=$1 AND detector='error_rate'`, v.ID).Scan(&raw, &at); e != nil {
		t.Fatal(e)
	}
	var c detectors.Candidate
	if e := json.Unmarshal(raw, &c); e != nil {
		t.Fatal(e)
	}
	return c, at
}
func publishAutomation(t *testing.T, s *Server, ws string, v Service, c detectors.Candidate, at time.Time) {
	t.Helper()
	if e := s.persistFindings(context.Background(), runArgs{WorkspaceID: ws}, v, false, at, c.Thresholds, []detectors.Candidate{c}); e != nil {
		t.Fatal(e)
	}
}
func pendingAutomation(t *testing.T, s *Server) string {
	t.Helper()
	var id string
	if e := s.pool.QueryRow(context.Background(), `SELECT id::text FROM automation_batches WHERE state='pending' ORDER BY created_at DESC LIMIT 1`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func TestAutomationGroupingReplayAndCooldown(t *testing.T) {
	s, _, _, _, ws, v, _, batch := automationFixture(t)
	ctx := context.Background()
	var count int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_items WHERE batch_id=$1`, batch).Scan(&count); e != nil || count < 2 {
		t.Fatalf("coalescing: count=%d err=%v", count, e)
	}
	if e := s.evaluateAutomation(ctx, automationArgs{ws, batch}); e == nil {
		t.Fatal("grouping window bypassed")
	}
	if e := s.evaluateAutomation(ctx, automationArgs{"00000000-0000-0000-0000-000000000001", batch}); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, batch, "pending")
	automationDue(t, s, batch)
	// Reconstruct the server as after a restart, then deliver the same job concurrently.
	restarted, e := New(ctx, s.pool, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- restarted.evaluateAutomation(ctx, automationArgs{ws, batch}) }()
	}
	for range 2 {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	automationState(t, s, batch, "recommended")
	c, at := automationCandidate(t, s, v)
	publishAutomation(t, s, ws, v, c, at.Add(5*time.Minute))
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_batches`).Scan(&count); e != nil || count != 1 {
		t.Fatal("unchanged scan created work", count, e)
	}
	c.Current *= 2
	publishAutomation(t, s, ws, v, c, at.Add(10*time.Minute))
	second := pendingAutomation(t, s)
	publishAutomation(t, s, ws, v, c, at.Add(15*time.Minute))
	automationDue(t, s, second)
	if e = s.evaluateAutomation(ctx, automationArgs{ws, second}); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, second, "skipped")
	var reason string
	s.pool.QueryRow(ctx, `SELECT reason FROM automation_items WHERE batch_id=$1`, second).Scan(&reason)
	if !strings.Contains(reason, "cooldown") {
		t.Fatal(reason)
	}
	if _, e = s.pool.Exec(ctx, `UPDATE automation_services SET next_eligible_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	publishAutomation(t, s, ws, v, c, at.Add(20*time.Minute))
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_batches`).Scan(&count); e != nil || count != 2 {
		t.Fatal("cooldown replayed unchanged work", count, e)
	}
	c.Current *= 2
	publishAutomation(t, s, ws, v, c, at.Add(25*time.Minute))
	third := pendingAutomation(t, s)
	automationDue(t, s, third)
	if e = s.evaluateAutomation(ctx, automationArgs{ws, third}); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, third, "recommended")
	if e = s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ai_runs)+(SELECT count(*) FROM runs WHERE kind='investigation')`).Scan(&count); e != nil || count != 0 {
		t.Fatal("recommendation launched work", count, e)
	}
	if e = s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events`).Scan(&count); e != nil || count < 2 {
		t.Fatal("automation disrupted factual events", count, e)
	}
}
func TestAutomationDispatchGates(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"monitoring", `UPDATE services SET monitoring=false`, "monitoring"},
		{"mapping", `UPDATE services SET revision=revision+1`, "mapping"},
		{"snoozed", `UPDATE findings SET state='snoozed'`, "snoozed"},
		{"resolved", `UPDATE findings SET state='resolved'`, "resolved"},
		{"expired evidence", `UPDATE evidence SET expires_at=now()-interval '1 second'`, "evidence"},
		{"incomplete evidence", `UPDATE evidence SET document=jsonb_set(document,'{complete}','false')`, "incomplete"},
		{"stale batch", `UPDATE automation_batches SET created_at=now()-interval '16 minutes'`, "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, ws, _, _, batch := automationFixture(t)
			ctx := context.Background()
			if _, e := s.pool.Exec(ctx, tc.sql); e != nil {
				t.Fatal(e)
			}
			automationDue(t, s, batch)
			if e := s.evaluateAutomation(ctx, automationArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			automationState(t, s, batch, "skipped")
			var reasons string
			s.pool.QueryRow(ctx, `SELECT string_agg(reason,' ') FROM automation_items WHERE batch_id=$1`, batch).Scan(&reasons)
			if !strings.Contains(reasons, tc.want) {
				t.Fatal(reasons)
			}
		})
	}
}
func TestAutomationPolicyVersionNoBacklogAndValidation(t *testing.T) {
	s, h, cookie, csrf, ws, v, p, batch := automationFixture(t)
	ctx := context.Background()
	requireCode(t, call(h, "POST", "/api/v1/automation/policy", p, cookie, "", s.cfg.Origin), 403)
	invalid := p
	invalid.Mode = "auto_investigate"
	requireCode(t, call(h, "POST", "/api/v1/automation/policy", invalid, cookie, csrf, s.cfg.Origin), 400)
	invalid = p
	invalid.Services = []string{"00000000-0000-0000-0000-000000000001"}
	requireCode(t, call(h, "POST", "/api/v1/automation/policy", invalid, cookie, csrf, s.cfg.Origin), 400)
	old := p
	p.Mode = "off"
	p = saveAutomationTest(t, s, h, cookie, csrf, p)
	requireCode(t, call(h, "POST", "/api/v1/automation/policy", old, cookie, csrf, s.cfg.Origin), 409)
	automationDue(t, s, batch)
	if e := s.evaluateAutomation(ctx, automationArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, batch, "skipped")
	candidate, at := automationCandidate(t, s, v)
	candidate.Current *= 2
	publishAutomation(t, s, ws, v, candidate, at.Add(5*time.Minute))
	p.Mode = "triage_only"
	saveAutomationTest(t, s, h, cookie, csrf, p)
	// The existing growth becomes the new policy's baseline, not a replayed event.
	publishAutomation(t, s, ws, v, candidate, at.Add(10*time.Minute))
	var count int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_batches`).Scan(&count)
	if count != 1 {
		t.Fatal("policy change replayed backlog")
	}
	candidate.Current *= 2
	publishAutomation(t, s, ws, v, candidate, at.Add(15*time.Minute))
	if pendingAutomation(t, s) == batch {
		t.Fatal("new change was not queued")
	}
}
func TestAutomationPendingRecoveryAndRollback(t *testing.T) {
	s, _, _, _, ws, v, _, batch := automationFixture(t)
	ctx := context.Background()
	automationDue(t, s, batch)
	if e := s.evaluateAutomation(ctx, automationArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	candidate, at := automationCandidate(t, s, v)
	original := candidate.Current
	candidate.Current *= 2
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var finding string
	tx.QueryRow(ctx, `SELECT id::text FROM findings WHERE detector='error_rate'`).Scan(&finding)
	if e = s.automationObservation(ctx, tx, ws, v, finding, false, at.Add(5*time.Minute), candidate); e != nil {
		t.Fatal(e)
	}
	tx.Rollback(ctx)
	var count int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_batches`).Scan(&count)
	if count != 1 {
		t.Fatal("rolled-back work leaked")
	}
	publishAutomation(t, s, ws, v, candidate, at.Add(10*time.Minute))
	pending := pendingAutomation(t, s)
	candidate.Current = original
	publishAutomation(t, s, ws, v, candidate, at.Add(15*time.Minute))
	automationDue(t, s, pending)
	if e = s.evaluateAutomation(ctx, automationArgs{ws, pending}); e != nil {
		t.Fatal(e)
	}
	var reason string
	s.pool.QueryRow(ctx, `SELECT reason FROM automation_items WHERE batch_id=$1`, pending).Scan(&reason)
	if !strings.Contains(reason, "no longer") {
		t.Fatal(reason)
	}
}

func TestAutomationOptInAndComparisonReset(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*automation.Policy)
		want   string
	}{
		{"off", func(p *automation.Policy) { p.Mode = "off" }, "off"},
		{"excluded", func(p *automation.Policy) { p.Services = []string{} }, "not selected"},
		{"demo", func(p *automation.Policy) { p.AllowDemo = false }, "Simulated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, ws, _, _, batch := automationFixture(t, tc.change)
			automationDue(t, s, batch)
			if e := s.evaluateAutomation(context.Background(), automationArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			automationState(t, s, batch, "skipped")
			var reason string
			s.pool.QueryRow(context.Background(), `SELECT reason FROM automation_batches WHERE id=$1`, batch).Scan(&reason)
			if !strings.Contains(reason, tc.want) {
				t.Fatal(reason)
			}
		})
	}
	s, _, _, _, ws, v, _, batch := automationFixture(t)
	automationDue(t, s, batch)
	if e := s.evaluateAutomation(context.Background(), automationArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	candidate, at := automationCandidate(t, s, v)
	candidate.Thresholds.CurrentMinutes = 30
	candidate.Current *= 2
	publishAutomation(t, s, ws, v, candidate, at.Add(5*time.Minute))
	pending := pendingAutomation(t, s)
	automationDue(t, s, pending)
	if e := s.evaluateAutomation(context.Background(), automationArgs{ws, pending}); e != nil {
		t.Fatal(e)
	}
	var recommendation string
	s.pool.QueryRow(context.Background(), `SELECT recommendation FROM automation_items WHERE batch_id=$1`, pending).Scan(&recommendation)
	if recommendation != "need_more_data" {
		t.Fatal("incomparable values treated as growth", recommendation)
	}
}
func TestAutomationHistoryScopePagingAndRetention(t *testing.T) {
	s, h, c, csrf, ws, v, _, batch := automationFixture(t)
	ctx := context.Background()
	// Identical timestamps stress the ID tie-breaker. No entries should be lost.
	_, e := s.pool.Exec(ctx, `INSERT INTO automation_batches(workspace_id,service_id,policy_revision,mapping_revision,state,reason,created_at) SELECT workspace_id,service_id,policy_revision,mapping_revision,'skipped','Fixture',now() FROM automation_batches CROSS JOIN generate_series(1,51) WHERE id=$1`, batch)
	if e != nil {
		t.Fatal(e)
	}
	type page struct {
		Items []automationActivity `json:"items"`
		Next  string               `json:"next_cursor"`
	}
	get := func(path string) page {
		t.Helper()
		w := call(h, "GET", path, nil, c, "", "")
		requireCode(t, w, 200)
		var p page
		if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	requireCode(t, call(h, "GET", "/api/v1/automation/activity", nil, nil, "", ""), 401)
	first := get("/api/v1/automation/activity")
	second := get("/api/v1/automation/activity?cursor=" + first.Next)
	if len(first.Items) != 50 || first.Next == "" || len(second.Items) != 2 || second.Next != "" {
		t.Fatal("incorrect history paging")
	}
	seen := map[string]bool{}
	for _, i := range append(first.Items, second.Items...) {
		if seen[i.ID] {
			t.Fatal("duplicate page item")
		}
		seen[i.ID] = true
	}
	if len(get("/api/v1/automation/activity?cursor=invalid").Items) != 0 || len(get("/api/v1/automation/activity?service_id=00000000-0000-0000-0000-000000000001&cursor="+first.Next).Items) != 0 {
		t.Fatal("filter/cursor scope lost")
	}
	if len(get("/api/v1/automation/activity?service_id="+v.ID).Items) != 50 {
		t.Fatal("service filter missing")
	}
	// Expired batches become visibly skipped, and cleanup retains dedup checkpoints.
	if _, e = s.pool.Exec(ctx, `UPDATE automation_batches SET created_at=now()-interval '16 minutes',due_at=now()-interval '15 minutes' WHERE id=$1`, batch); e != nil {
		t.Fatal(e)
	}
	if e = s.retainAutomation(ctx); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, batch, "skipped")
	if _, e = s.pool.Exec(ctx, `UPDATE automation_batches SET created_at=now()-interval '31 days'`); e != nil {
		t.Fatal(e)
	}
	if e = s.retainAutomation(ctx); e != nil {
		t.Fatal(e)
	}
	if len(get("/api/v1/automation/activity").Items) != 0 {
		t.Fatal("history not retained to bound")
	}
	var count int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_checkpoints WHERE workspace_id=$1`, ws).Scan(&count)
	if count == 0 {
		t.Fatal("cleanup lost dedup baseline")
	}
	// Direct finding links must reach items beyond the first 100-ID page.
	if _, e = s.pool.Exec(ctx, `INSERT INTO findings(workspace_id,service_id,mapping_revision,origin,detector,signal,occurrence,severity,title,description,details,first_seen,last_seen) SELECT workspace_id,service_id,mapping_revision,origin,detector,'page-'||g.n,1,severity,title,description,details,first_seen,last_seen FROM findings CROSS JOIN generate_series(1,101) AS g(n) WHERE service_id=$1 AND detector='error_rate'`, v.ID); e != nil {
		t.Fatal(e)
	}
	candidate, _ := automationCandidate(t, s, v)
	var id string
	e = s.pool.QueryRow(ctx, `INSERT INTO findings(id,workspace_id,service_id,mapping_revision,origin,detector,signal,occurrence,severity,title,description,details,first_seen,last_seen) VALUES('ffffffff-ffff-ffff-ffff-ffffffffffff',$1,$2,1,'demo','error_rate','paging',1,'warning','Old finding','Fixture',$3,now(),now()) RETURNING id::text`, ws, v.ID, mustJSON(candidate)).Scan(&id)
	if e != nil {
		t.Fatal(e)
	}
	unfiltered := call(h, "GET", "/api/v1/findings", nil, c, "", "")
	requireCode(t, unfiltered, 200)
	if strings.Contains(unfiltered.Body.String(), id) {
		t.Fatal("fixture finding unexpectedly fits on the first page")
	}
	w := call(h, "GET", "/api/v1/findings?id="+id, nil, c, "", "")
	requireCode(t, w, 200)
	var result struct {
		Items []Finding `json:"items"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil || len(result.Items) != 1 || result.Items[0].ID != id {
		t.Fatal("finding ID filter ignored", e)
	}
	requireCode(t, call(h, "POST", "/api/v1/automation/policy", automation.Defaults(), c, csrf, s.cfg.Origin), 409)
}
func mustJSON(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

func TestAutomationExhaustedCleanupAdvancesBaseline(t *testing.T) {
	s, _, _, _, ws, v, _, batch := automationFixture(t)
	ctx := context.Background()
	if _, e := s.pool.Exec(ctx, `UPDATE river_job SET state='discarded',finalized_at=now() WHERE id=(SELECT job_id FROM automation_batches WHERE id=$1)`, batch); e != nil {
		t.Fatal(e)
	}
	// Not-yet-due exhausted jobs must not abort other retention work.
	if e := s.retainAutomation(ctx); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, batch, "pending")
	automationDue(t, s, batch)
	if e := s.retainAutomation(ctx); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, batch, "skipped")
	var reason string
	if e := s.pool.QueryRow(ctx, `SELECT reason FROM automation_batches WHERE id=$1`, batch).Scan(&reason); e != nil || !strings.Contains(reason, "retries") {
		t.Fatal(reason, e)
	}
	candidate, at := automationCandidate(t, s, v)
	publishAutomation(t, s, ws, v, candidate, at.Add(5*time.Minute))
	var count int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_batches`).Scan(&count); e != nil || count != 1 {
		t.Fatal("exhausted evaluation replayed", count, e)
	}
}

func TestAutomationWorkerProcessesDurableBatch(t *testing.T) {
	s, _, _, _, _, _, _, batch := automationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	automationDue(t, s, batch)
	if _, e := s.pool.Exec(ctx, `UPDATE river_job SET state='available',scheduled_at=now() WHERE id=(SELECT job_id FROM automation_batches WHERE id=$1)`, batch); e != nil {
		t.Fatal(e)
	}
	if e := s.StartJobs(ctx); e != nil {
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
		if e := s.pool.QueryRow(ctx, `SELECT state FROM automation_batches WHERE id=$1`, batch).Scan(&state); e != nil {
			t.Fatal(e)
		}
		if state == "recommended" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("River did not evaluate durable recommendation", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
