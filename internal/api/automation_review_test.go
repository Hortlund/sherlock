package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sherlock/internal/automation"
	"sherlock/internal/connectors"
	"sherlock/internal/detectors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func healthyAutomationResult(at time.Time, complete bool) detectors.Result {
	inputs := func(start, end time.Time) []detectors.Input {
		return []detectors.Input{
			{ID: "total", Observation: connectors.Observation{Kind: "request_total", Complete: complete, Value: 1000, Start: start, End: end}},
			{ID: "errors", Observation: connectors.Observation{Kind: "request_errors", Complete: complete, Value: 1, Start: start, End: end}},
			{ID: "logs", Observation: connectors.Observation{Kind: "log_patterns", Complete: complete, SampleCount: 20, CoverageFraction: 1, Start: start, End: end}},
		}
	}
	start := at.Add(-15 * time.Minute)
	return detectors.Evaluate(inputs(start, at), inputs(start.Add(-60*time.Minute), start), detectors.Defaults())
}
func TestAutomationCompleteRecovery(t *testing.T) {
	for _, scenario := range []string{"healthy", "incomplete", "older", "requalified", "replayed after evaluation"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, _, ws, v, _, first := automationFixture(t)
			ctx := context.Background()
			automationDue(t, s, first)
			if e := s.evaluateAutomation(ctx, automationArgs{ws, first}); e != nil {
				t.Fatal(e)
			}
			if _, e := s.pool.Exec(ctx, `UPDATE automation_services SET next_eligible_at=now()-interval '1 minute'`); e != nil {
				t.Fatal(e)
			}
			candidate, at := automationCandidate(t, s, v)
			candidate.Current *= 2
			publishAutomation(t, s, ws, v, candidate, at.Add(5*time.Minute))
			batch := pendingAutomation(t, s)
			healthyAt := at.Add(10 * time.Minute)
			if scenario == "older" {
				healthyAt = at.Add(4 * time.Minute)
			}
			result := healthyAutomationResult(healthyAt, scenario != "incomplete")
			if len(result.Candidates) != 0 {
				t.Fatal(result)
			}
			if e := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, healthyAt, candidate.Thresholds, result.Candidates, result.Coverage...); e != nil {
				t.Fatal(e)
			}
			if scenario == "requalified" {
				publishAutomation(t, s, ws, v, candidate, healthyAt.Add(time.Minute))
			}
			// A candidate older than the healthy scan must not undo the recovery marker.
			if scenario == "healthy" {
				publishAutomation(t, s, ws, v, candidate, at.Add(6*time.Minute))
			}
			automationDue(t, s, batch)
			if e := s.evaluateAutomation(ctx, automationArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			want := "recommended"
			if scenario == "healthy" || scenario == "replayed after evaluation" {
				want = "skipped"
			}
			automationState(t, s, batch, want)
			if want == "skipped" {
				var reason string
				var cooled bool
				if e := s.pool.QueryRow(ctx, `SELECT reason FROM automation_items WHERE batch_id=$1`, batch).Scan(&reason); e != nil || !strings.Contains(reason, "newer complete scan") {
					t.Fatal(reason, e)
				}
				if e := s.pool.QueryRow(ctx, `SELECT next_eligible_at>now() FROM automation_services WHERE service_id=$1`, v.ID).Scan(&cooled); e != nil || cooled {
					t.Fatal("recovery started cooldown", cooled, e)
				}
			}
			if scenario == "replayed after evaluation" {
				candidate.Current *= 2
				publishAutomation(t, s, ws, v, candidate, at.Add(7*time.Minute))
				var count int
				if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_batches`).Scan(&count); e != nil || count != 2 {
					t.Fatal("older candidate replayed after healthy skip", count, e)
				}
			}
		})
	}
}
func TestAutomationDetectorSettingsAtDispatch(t *testing.T) {
	for _, scenario := range []string{"disabled", "threshold", "window", "operational only"} {
		t.Run(scenario, func(t *testing.T) {
			s, h, c, csrf, ws, _, _, batch := automationFixture(t)
			cfg := detectors.Defaults()
			switch scenario {
			case "disabled":
				cfg.Disabled = []string{"error_rate", "new_exception", "warning_burst"}
			case "threshold":
				cfg.ErrorDelta = .5
			case "window":
				cfg.CurrentMinutes = 30
			case "operational only":
				cfg.RetentionDays = 14
				cfg.CadenceMinutes = 10
			}
			requireCode(t, call(h, "POST", "/api/v1/settings", cfg, c, csrf, s.cfg.Origin), 200)
			automationDue(t, s, batch)
			if e := s.evaluateAutomation(context.Background(), automationArgs{ws, batch}); e != nil {
				t.Fatal(e)
			}
			want := "skipped"
			if scenario == "operational only" {
				want = "recommended"
			}
			automationState(t, s, batch, want)
			if want == "skipped" {
				var count int
				if e := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM automation_services`).Scan(&count); e != nil || count != 0 {
					t.Fatal("obsolete settings started cooldown", count, e)
				}
				var reason string
				if e := s.pool.QueryRow(context.Background(), `SELECT string_agg(reason,' ') FROM automation_items WHERE batch_id=$1`, batch).Scan(&reason); e != nil || !strings.Contains(reason, "Monitoring") {
					t.Fatal(reason, e)
				}
			}
		})
	}
}
func TestAutomationStaleQueuedScan(t *testing.T) {
	s, h, c, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		at, _ := strconv.ParseFloat(q.Get("time"), 64)
		value := "1000"
		if strings.Contains(q.Get("query"), "3600s") {
			value = "4000"
		}
		if strings.Contains(q.Get("query"), `status="500"`) {
			value = "120"
			if strings.Contains(q.Get("query"), "3600s") {
				value = "4"
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{map[string]any{"metric": map[string]string{}, "value": []any{at, value}}}}})
	}))
	defer upstream.Close()
	if _, e := s.pool.Exec(ctx, `UPDATE connections SET base_url=$2 WHERE id=$1`, v.PrometheusID, upstream.URL); e != nil {
		t.Fatal(e)
	}
	if _, e := s.pool.Exec(ctx, `UPDATE services SET monitoring=true WHERE id=$1`, v.ID); e != nil {
		t.Fatal(e)
	}
	policy := automation.Defaults()
	policy.Mode = "triage_only"
	policy.Services = []string{v.ID}
	policy.AllowDemo = true
	saveAutomationTest(t, s, h, c, csrf, policy)
	resp := call(h, "POST", "/api/v1/services/"+v.ID+"/scans", map[string]any{}, c, csrf, s.cfg.Origin)
	requireCode(t, resp, 202)
	run := bodyMap(t, resp)["id"].(string)
	if _, e := s.pool.Exec(ctx, `UPDATE runs SET window_start=window_start-interval '1 day',window_end=window_end-interval '1 day',created_at=created_at-interval '1 day' WHERE id=$1`, run); e != nil {
		t.Fatal(e)
	}
	if e := s.execute(ctx, runArgs{ws, run}); e != nil {
		t.Fatal(e)
	}
	batch := pendingAutomation(t, s)
	automationDue(t, s, batch)
	if e := s.evaluateAutomation(ctx, automationArgs{ws, batch}); e != nil {
		t.Fatal(e)
	}
	automationState(t, s, batch, "skipped")
	var reason string
	if e := s.pool.QueryRow(ctx, `SELECT reason FROM automation_items WHERE batch_id=$1`, batch).Scan(&reason); e != nil || !strings.Contains(reason, "older than 15 minutes") {
		t.Fatal(reason, e)
	}
	var count int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM automation_services`).Scan(&count); e != nil || count != 0 {
		t.Fatal("stale scan started cooldown", count, e)
	}
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events`).Scan(&count); e != nil || count == 0 {
		t.Fatal("automation suppressed independent factual events", count, e)
	}
}
