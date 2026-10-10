package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sherlock/internal/connectors"
	"sherlock/internal/demo"
	"testing"
	"time"
)

func TestDurableScanReplayAndLifecycle(t *testing.T) {
	s, _ := testServer(t)
	up := httptest.NewServer(demo.Handler())
	defer up.Close()
	s.cfg.DemoURL = up.URL
	s.cfg.AllowedCIDRs = "127.0.0.0/8"
	s.policy, _ = connectors.NewPolicy("127.0.0.0/8", "")
	h := s.Handler()
	ctx := context.Background()
	boot := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
	requireCode(t, boot, 201)
	cookie := boot.Result().Cookies()[0]
	csrf := bodyMap(t, boot)["csrf_token"].(string)
	d := call(h, "POST", "/api/v1/demo", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, d, 201)
	sid := bodyMap(t, d)["service_id"].(string)
	queue := func() string {
		w := call(h, "POST", "/api/v1/services/"+sid+"/scans", map[string]string{}, cookie, csrf, s.cfg.Origin)
		requireCode(t, w, 202)
		return bodyMap(t, w)["id"].(string)
	}
	id := queue()
	if queue() != id {
		t.Fatal("active scan was duplicated")
	}
	var ws string
	if e := s.pool.QueryRow(ctx, `SELECT workspace_id::text FROM services WHERE id=$1`, sid).Scan(&ws); e != nil {
		t.Fatal(e)
	}
	// A new application instance must find the previously committed River job.
	restarted, e := New(ctx, s.pool, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.StartJobs(ctx); e != nil {
		t.Fatal(e)
	}
	defer restarted.StopJobs(ctx)
	deadline := time.Now().Add(10 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		if e = s.pool.QueryRow(ctx, `SELECT state FROM runs WHERE id=$1`, id).Scan(&state); e != nil {
			t.Fatal(e)
		}
		if terminal(state) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state != "completed" {
		t.Fatalf("run state %s", state)
	}
	if e = restarted.StopJobs(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	check := func() []Finding {
		w := call(h, "GET", "/api/v1/findings", nil, cookie, "", "")
		requireCode(t, w, 200)
		var v struct {
			Items []Finding `json:"items"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v.Items
	}
	findings := check()
	if len(findings) != 2 {
		t.Fatalf("want 2 findings got %d", len(findings))
	}
	if e = s.execute(ctx, runArgs{ws, queue()}); e != nil {
		t.Fatal(e)
	}
	if len(check()) != 2 {
		t.Fatal("overlapping replay duplicated findings")
	}
	path := "/api/v1/findings/" + findings[0].ID + "/state"
	for _, st := range []string{"acknowledged", "snoozed", "acknowledged", "resolved", "resolved"} {
		requireCode(t, call(h, "POST", path, map[string]any{"state": st, "snooze_hours": 1}, cookie, csrf, s.cfg.Origin), 200)
	}
	requireCode(t, call(h, "POST", path, map[string]any{"state": "acknowledged"}, cookie, csrf, s.cfg.Origin), 409)
	// A persisted exhausted budget survives delivery retries and cannot emit new evidence.
	id = queue()
	_, e = s.pool.Exec(ctx, `UPDATE runs SET requests=12 WHERE id=$1`, id)
	if e != nil {
		t.Fatal(e)
	}
	_ = s.execute(ctx, runArgs{ws, id})
	var count int
	s.pool.QueryRow(ctx, `SELECT requests FROM runs WHERE id=$1`, id).Scan(&count)
	if count != 12 {
		t.Fatal("request budget reset")
	}
}

func TestInvestigationProgressScopeAndCancellation(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	up := httptest.NewServer(demo.Handler())
	defer up.Close()
	s.cfg.DemoURL = up.URL
	s.policy, _ = connectors.NewPolicy("127.0.0.0/8", "")
	h := s.Handler()
	boot := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
	requireCode(t, boot, 201)
	cookie := boot.Result().Cookies()[0]
	csrf := bodyMap(t, boot)["csrf_token"].(string)
	demoResult := call(h, "POST", "/api/v1/demo", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, demoResult, 201)
	sid := bodyMap(t, demoResult)["service_id"].(string)
	var ws string
	s.pool.QueryRow(ctx, `SELECT workspace_id::text FROM services WHERE id=$1`, sid).Scan(&ws)
	start := func() string {
		w := call(h, "POST", "/api/v1/services/"+sid+"/investigations", map[string]string{}, cookie, csrf, s.cfg.Origin)
		requireCode(t, w, 202)
		return bodyMap(t, w)["id"].(string)
	}
	id := start()
	if e := s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	w := call(h, "GET", "/api/v1/investigations/"+id, nil, cookie, "", "")
	requireCode(t, w, 200)
	var run Run
	if e := json.Unmarshal(w.Body.Bytes(), &run); e != nil {
		t.Fatal(e)
	}
	if run.State != "completed" || run.Requests != 6 || run.Result.Report == nil || !run.Result.Report.Adequate {
		t.Fatalf("invalid report %s", w.Body.String())
	}
	events := call(h, "GET", "/api/v1/investigations/"+id+"/events", nil, cookie, "", "")
	requireCode(t, events, 200)
	var result struct{ Items []progressEvent }
	json.Unmarshal(events.Body.Bytes(), &result)
	if len(result.Items) != 4 {
		t.Fatalf("missing events: %s", events.Body.String())
	}
	id = start()
	requireCode(t, call(h, "POST", "/api/v1/investigations/"+id+"/cancel", map[string]string{}, cookie, csrf, s.cfg.Origin), 200)
	if e := s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	var requests int
	var state string
	s.pool.QueryRow(ctx, `SELECT state,requests FROM runs WHERE id=$1`, id).Scan(&state, &requests)
	if state != "cancelled" || requests != 0 {
		t.Fatal("cancelled job made requests")
	}
	id = start()
	s.pool.Exec(ctx, `UPDATE runs SET deadline=now()-interval '1 second' WHERE id=$1`, id)
	if e := s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	s.pool.QueryRow(ctx, `SELECT state,requests FROM runs WHERE id=$1`, id).Scan(&state, &requests)
	if state != "failed" || requests != 0 {
		t.Fatal("deadline reset on retry")
	}
	requireCode(t, call(h, "GET", "/api/v1/investigations/"+id, nil, nil, "", ""), 401)
}

func TestPersistedScheduleDisableAndMapping(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	up := httptest.NewServer(demo.Handler())
	defer up.Close()
	s.cfg.DemoURL = up.URL
	s.policy, _ = connectors.NewPolicy("127.0.0.0/8", "")
	h := s.Handler()
	boot := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
	requireCode(t, boot, 201)
	cookie := boot.Result().Cookies()[0]
	csrf := bodyMap(t, boot)["csrf_token"].(string)
	created := call(h, "POST", "/api/v1/demo", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, created, 201)
	sid := bodyMap(t, created)["service_id"].(string)
	if e := s.Pump(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&count)
	if count != 0 {
		t.Fatal("disabled monitoring scheduled")
	}
	s.pool.Exec(ctx, `UPDATE services SET monitoring=true WHERE id=$1`, sid)
	if e := s.Pump(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.Pump(ctx); e != nil {
		t.Fatal(e)
	}
	s.pool.QueryRow(ctx, `SELECT count(*) FROM runs`).Scan(&count)
	if count != 1 {
		t.Fatal("due schedule duplicated")
	}
	var ws, id string
	s.pool.QueryRow(ctx, `SELECT workspace_id::text,id::text FROM runs`).Scan(&ws, &id)
	s.pool.Exec(ctx, `UPDATE services SET monitoring=false WHERE id=$1`, sid)
	if e := s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	s.pool.QueryRow(ctx, `SELECT requests FROM runs WHERE id=$1`, id).Scan(&count)
	if count != 0 {
		t.Fatal("disabled monitoring collected")
	}
	w := call(h, "POST", "/api/v1/services/"+sid+"/scans", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, w, 202)
	id = bodyMap(t, w)["id"].(string)
	s.pool.Exec(ctx, `UPDATE services SET revision=revision+1 WHERE id=$1`, sid)
	if e := s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	s.pool.QueryRow(ctx, `SELECT requests FROM runs WHERE id=$1`, id).Scan(&count)
	if count != 0 {
		t.Fatal("stale mapping collected")
	}
}
