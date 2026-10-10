package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sherlock/internal/connectors"
	"sherlock/internal/demo"
	"strings"
	"testing"
)

func TestSourceMappingAndEvidence(t *testing.T) {
	s, _ := testServer(t)
	up := httptest.NewServer(demo.Handler())
	defer up.Close()
	s.cfg.DemoURL = up.URL
	s.policy, _ = connectors.NewPolicy("127.0.0.0/8", "")
	h := s.Handler()
	boot := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
	requireCode(t, boot, 201)
	cookie := boot.Result().Cookies()[0]
	csrf := bodyMap(t, boot)["csrf_token"].(string)
	add := call(h, "POST", "/api/v1/connections", map[string]string{"name": "Real contract fixture", "kind": "prometheus", "base_url": up.URL, "auth_mode": "bearer", "secret": "private-api-token"}, cookie, csrf, s.cfg.Origin)
	requireCode(t, add, 200)
	id := bodyMap(t, add)["id"].(string)
	if strings.Contains(add.Body.String(), "private-api-token") {
		t.Fatal("credential returned")
	}
	var ciphertext []byte
	if e := s.pool.QueryRow(context.Background(), `SELECT credential FROM connections WHERE id::text=$1`, id).Scan(&ciphertext); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(ciphertext), "private-api-token") {
		t.Fatal("plaintext credential stored")
	}
	requireCode(t, call(h, "POST", "/api/v1/connections/"+id+"/test", map[string]string{}, cookie, csrf, s.cfg.Origin), 200)
	created := call(h, "POST", "/api/v1/demo", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, created, 201)
	service := bodyMap(t, created)["service_id"].(string)
	requireCode(t, call(h, "POST", "/api/v1/demo", map[string]string{}, cookie, csrf, s.cfg.Origin), 200)
	preview := call(h, "POST", "/api/v1/services/"+service+"/preview", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, preview, 200)
	var result struct {
		Evidence []Evidence `json:"evidence"`
		Issues   []string   `json:"issues"`
	}
	if e := json.Unmarshal(preview.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if len(result.Evidence) != 3 || len(result.Issues) != 0 {
		t.Fatalf("preview failed: %s", preview.Body.String())
	}
	ev := result.Evidence[0]
	requireCode(t, call(h, "GET", "/api/v1/evidence/"+ev.ID, nil, cookie, "", ""), 200)
	requireCode(t, call(h, "POST", "/api/v1/connections/"+ev.ConnectionID+"/state", map[string]string{"action": "disable"}, cookie, csrf, s.cfg.Origin), 200)
	again := call(h, "POST", "/api/v1/services/"+service+"/preview", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, again, 200)
	if !strings.Contains(again.Body.String(), "disabled") {
		t.Fatal("disabled source not reported")
	}
	requireCode(t, call(h, "GET", "/api/v1/evidence/"+ev.ID, nil, cookie, "", ""), 200)
	requireCode(t, call(h, "POST", "/api/v1/connections/"+id+"/state", map[string]string{"action": "delete"}, cookie, csrf, s.cfg.Origin), 200)
	if e := s.pool.QueryRow(context.Background(), `SELECT credential FROM connections WHERE id::text=$1`, id).Scan(&ciphertext); e != nil || len(ciphertext) != 0 {
		t.Fatal("deleted secret retained")
	}
}
