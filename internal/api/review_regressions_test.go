package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sherlock/internal/connectors"
	"sherlock/internal/demo"
	"sherlock/internal/detectors"
	"strconv"
	"testing"
	"time"
)

func reviewFixture(t *testing.T, delay time.Duration) (*Server, http.Handler, *http.Cookie, string, string, Service) {
	t.Helper()
	s, h := testServer(t)
	fixture := demo.Handler()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(delay); fixture.ServeHTTP(w, r) }))
	t.Cleanup(upstream.Close)
	s.cfg.DemoURL = upstream.URL
	s.policy, _ = connectors.NewPolicy("127.0.0.0/8", "")
	boot := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
	requireCode(t, boot, 201)
	cookie := boot.Result().Cookies()[0]
	csrf := bodyMap(t, boot)["csrf_token"].(string)
	created := call(h, "POST", "/api/v1/demo", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, created, 201)
	id := bodyMap(t, created)["service_id"].(string)
	var ws string
	if e := s.pool.QueryRow(context.Background(), `SELECT workspace_id::text FROM services WHERE id=$1`, id).Scan(&ws); e != nil {
		t.Fatal(e)
	}
	v, e := s.service(context.Background(), ws, id)
	if e != nil {
		t.Fatal(e)
	}
	return s, h, cookie, csrf, ws, v
}
func serviceBody(v Service) map[string]any {
	return map[string]any{"name": v.Name, "environment": v.Environment, "prometheus_id": v.PrometheusID, "loki_id": v.LokiID, "mapping": v.Mapping, "monitoring": v.Monitoring}
}
func TestServiceMetadataPreservesFindingIdentity(t *testing.T) {
	s, h, cookie, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	scan := func() {
		t.Helper()
		w := call(h, "POST", "/api/v1/services/"+v.ID+"/scans", map[string]string{}, cookie, csrf, s.cfg.Origin)
		requireCode(t, w, 202)
		if e := s.execute(ctx, runArgs{ws, bodyMap(t, w)["id"].(string)}); e != nil {
			t.Fatal(e)
		}
	}
	scan()
	var finding string
	if e := s.pool.QueryRow(ctx, `SELECT id::text FROM findings LIMIT 1`).Scan(&finding); e != nil {
		t.Fatal(e)
	}
	requireCode(t, call(h, "POST", "/api/v1/findings/"+finding+"/state", map[string]string{"state": "acknowledged"}, cookie, csrf, s.cfg.Origin), 200)
	for _, monitoring := range []bool{true, false, false} {
		v.Name = "Renamed checkout"
		v.Monitoring = monitoring
		// Opening and saving a legacy mapping in the new editor emits explicit selectors.
		v.Mapping = v.Mapping.Canonical(true, true)
		saved := call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), cookie, csrf, s.cfg.Origin)
		requireCode(t, saved, 200)
		if bodyMap(t, saved)["revision"] != float64(v.Revision) {
			t.Fatal("non-signal edit changed mapping revision")
		}
	}
	scan()
	var count int
	var state string
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM findings`).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if e := s.pool.QueryRow(ctx, `SELECT state FROM findings WHERE id=$1`, finding).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if count != 2 || state != "acknowledged" {
		t.Fatalf("finding identity lost: %d %s", count, state)
	}
	v.Mapping.LokiLabels = map[string]string{"app": "different"}
	saved := call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), cookie, csrf, s.cfg.Origin)
	requireCode(t, saved, 200)
	if bodyMap(t, saved)["revision"] != float64(v.Revision+1) {
		t.Fatal("selector change did not invalidate mapping")
	}
}
func TestInvestigationLaunchConflict(t *testing.T) {
	s, h, cookie, csrf, _, v := reviewFixture(t, 0)
	end := time.Now().UTC().Truncate(5 * time.Minute)
	body := map[string]any{"start": end.Add(-30 * time.Minute), "end": end}
	path := "/api/v1/services/" + v.ID + "/investigations"
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { responses <- call(h, "POST", path, body, cookie, csrf, s.cfg.Origin) }()
	}
	first, second := <-responses, <-responses
	requireCode(t, first, 202)
	requireCode(t, second, 202)
	id := bodyMap(t, first)["id"]
	if bodyMap(t, second)["id"] != id {
		t.Fatal("equivalent concurrent request duplicated")
	}
	requireCode(t, call(h, "POST", path, map[string]any{"start": end.Add(-time.Hour), "end": end.Add(-30 * time.Minute)}, cookie, csrf, s.cfg.Origin), 409)
	cfg := detectors.Defaults()
	cfg.MinTraffic = 200
	requireCode(t, call(h, "POST", "/api/v1/settings", cfg, cookie, csrf, s.cfg.Origin), 200)
	requireCode(t, call(h, "POST", path, body, cookie, csrf, s.cfg.Origin), 409)
	requireCode(t, call(h, "POST", "/api/v1/settings", detectors.Defaults(), cookie, csrf, s.cfg.Origin), 200)
	v.Mapping.Labels = map[string]string{"app": "changed"}
	requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), cookie, csrf, s.cfg.Origin), 200)
	requireCode(t, call(h, "POST", path, body, cookie, csrf, s.cfg.Origin), 409)
	requireCode(t, call(h, "POST", "/api/v1/investigations/"+id.(string)+"/cancel", map[string]string{}, cookie, csrf, s.cfg.Origin), 200)
	requireCode(t, call(h, "POST", path, body, cookie, csrf, s.cfg.Origin), 202)
}
func TestOverviewFreshnessRespectsCadence(t *testing.T) {
	s, h, cookie, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	queued := call(h, "POST", "/api/v1/services/"+v.ID+"/scans", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, queued, 202)
	id := bodyMap(t, queued)["id"].(string)
	if e := s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		cadence, age int
		stale        bool
	}{{60, 30, false}, {60, 66, true}, {5, 9, false}, {5, 11, true}} {
		cfg := detectors.Defaults()
		cfg.CadenceMinutes = tc.cadence
		requireCode(t, call(h, "POST", "/api/v1/settings", cfg, cookie, csrf, s.cfg.Origin), 200)
		if _, e := s.pool.Exec(ctx, `UPDATE runs SET finished_at=now()-$2*interval '1 minute' WHERE id=$1`, id, tc.age); e != nil {
			t.Fatal(e)
		}
		got := call(h, "GET", "/api/v1/overview", nil, cookie, "", "")
		requireCode(t, got, 200)
		if bodyMap(t, got)["stale"] != tc.stale {
			t.Fatalf("cadence=%d age=%d: %s", tc.cadence, tc.age, got.Body.String())
		}
	}
}
func TestConcurrentScansSharingSourceComplete(t *testing.T) {
	s, h, cookie, csrf, ws, v := reviewFixture(t, 80*time.Millisecond)
	ctx := context.Background()
	ids := []string{}
	for range 4 {
		v.PrometheusID = ""
		v.Mapping = v.Mapping.Canonical(false, true)
		created := call(h, "POST", "/api/v1/services", serviceBody(v), cookie, csrf, s.cfg.Origin)
		requireCode(t, created, 200)
		sid := bodyMap(t, created)["id"].(string)
		queued := call(h, "POST", "/api/v1/services/"+sid+"/scans", map[string]string{}, cookie, csrf, s.cfg.Origin)
		requireCode(t, queued, 202)
		ids = append(ids, bodyMap(t, queued)["id"].(string))
	}
	done := make(chan error, 4)
	for _, id := range ids {
		go func() { done <- s.execute(ctx, runArgs{ws, id}) }()
	}
	for range ids {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range ids {
		response := call(h, "GET", "/api/v1/scans/"+id, nil, cookie, "", "")
		requireCode(t, response, 200)
		var run Run
		if e := json.Unmarshal(response.Body.Bytes(), &run); e != nil {
			t.Fatal(e)
		}
		if run.State != "completed" || run.Requests != 2 || len(run.Result.Issues) != 0 {
			t.Fatalf("scan dropped: %s", response.Body.String())
		}
	}
}

func TestMonitoringDisableStopsScheduledFindingWrites(t *testing.T) {
	s, _, _, _, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	candidate := detectors.Candidate{Detector: "new_exception", Signal: "test", Severity: "warning", Title: "Test", Description: "Test"}
	if e := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, true, time.Now(), detectors.Defaults(), []detectors.Candidate{candidate}); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM findings`).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 0 {
		t.Fatal("disabled scheduled run emitted a finding")
	}
	if e := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, time.Now(), detectors.Defaults(), []detectors.Candidate{candidate}); e != nil {
		t.Fatal(e)
	}
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM findings`).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if count != 1 {
		t.Fatal("manual scan incorrectly suppressed")
	}
}

func TestSingleSourceMappingRoundTrip(t *testing.T) {
	s, h, cookie, csrf, _, original := reviewFixture(t, 0)
	for _, kind := range []string{"prometheus", "loki"} {
		t.Run(kind, func(t *testing.T) {
			v := original
			v.Name = kind + " only"
			if kind == "prometheus" {
				v.LokiID = ""
			} else {
				v.PrometheusID = ""
			}
			created := call(h, "POST", "/api/v1/services", serviceBody(v), cookie, csrf, s.cfg.Origin)
			requireCode(t, created, 200)
			if err := json.Unmarshal(created.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if kind == "loki" && v.Mapping.ErrorLabels != nil {
				t.Fatal("expected canonical null metrics labels")
			}
			if kind == "prometheus" && (v.Mapping.ErrorPattern != "" || v.Mapping.WarningPattern != "") {
				t.Fatal("expected canonical empty log patterns")
			}
			v.Name += " renamed"
			v.Monitoring = true
			saved := call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), cookie, csrf, s.cfg.Origin)
			requireCode(t, saved, 200)
			if bodyMap(t, saved)["revision"] != float64(v.Revision) {
				t.Fatal("metadata edit changed signal identity")
			}
			if kind == "loki" {
				v.Mapping.ErrorPattern = ""
				requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), cookie, csrf, s.cfg.Origin), 400)
				v.Mapping.ErrorPattern = "error"
				v.Mapping.WarningPattern = ""
				requireCode(t, call(h, "POST", "/api/v1/services/"+v.ID, serviceBody(v), cookie, csrf, s.cfg.Origin), 400)
			}
		})
	}
}

func TestLegacyFingerprintPreservesLifecycle(t *testing.T) {
	for _, state := range []string{"acknowledged", "dismissed"} {
		t.Run(state, func(t *testing.T) {
			s, h, cookie, csrf, ws, v := reviewFixture(t, 0)
			ctx := context.Background()
			end := time.Now().UTC().Truncate(time.Minute)
			// Original v1 hash of "error authentication password=[redacted]".
			legacy := detectors.Candidate{Detector: "new_exception", Signal: "v1:a79354cc23c009793a1f8c5788a8bf91b4ad0777efd42c609812690c91e5b65b", Severity: "warning", Title: "Authentication errors", Description: "Original finding"}
			if err := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, end, detectors.Defaults(), []detectors.Candidate{legacy}); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := s.pool.QueryRow(ctx, `SELECT id::text FROM findings`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			requireCode(t, call(h, "POST", "/api/v1/findings/"+id+"/state", map[string]string{"state": state}, cookie, csrf, s.cfg.Origin), 200)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				values := [][]string{}
				for i := 0; i < 20; i++ {
					values = append(values, []string{strconv.FormatInt(end.Add(-15*time.Minute).Add(time.Duration(i)*45*time.Second).UnixNano(), 10), "error authentication password=changed-private-value"})
				}
				json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"values": values}}}})
			}))
			defer upstream.Close()
			current, err := s.policy.Logs(ctx, connectors.Source{ID: "compat", URL: upstream.URL}, v.Mapping, end.Add(-15*time.Minute), end)
			if err != nil {
				t.Fatal(err)
			}
			baseline := connectors.Observation{Kind: "log_patterns", Complete: true, SampleCount: 20, CoverageFraction: .95, Start: end.Add(-75 * time.Minute), End: end.Add(-15 * time.Minute)}
			result := detectors.Evaluate([]detectors.Input{{ID: "current", Observation: current}}, []detectors.Input{{ID: "baseline", Observation: baseline}}, detectors.Defaults())
			if len(result.Candidates) != 1 || result.Candidates[0].Signal != legacy.Signal {
				t.Fatalf("identity changed: %+v", result)
			}
			if err := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, end.Add(time.Minute), detectors.Defaults(), result.Candidates); err != nil {
				t.Fatal(err)
			}
			var count int
			var actual string
			if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM findings`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if err := s.pool.QueryRow(ctx, `SELECT state FROM findings WHERE id=$1`, id).Scan(&actual); err != nil {
				t.Fatal(err)
			}
			if count != 1 || actual != state {
				t.Fatalf("lifecycle lost: %d findings, state %s", count, actual)
			}
		})
	}
}
