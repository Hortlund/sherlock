package api

import (
	"bytes"
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
	"os/exec"
	"sherlock/internal/auth"
	"sherlock/internal/connectors"
	"sherlock/internal/demo"
	"sherlock/internal/maintenance"
	"testing"
)

func TestRetentionAndKeyRotation(t *testing.T) {
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
	preview := call(h, "POST", "/api/v1/services/"+sid+"/preview", map[string]string{}, cookie, csrf, s.cfg.Origin)
	requireCode(t, preview, 200)
	var id, ws, connection string
	if e := s.pool.QueryRow(ctx, `SELECT id::text,workspace_id::text,connection_id::text FROM evidence LIMIT 1`).Scan(&id, &ws, &connection); e != nil {
		t.Fatal(e)
	}
	if _, e := s.pool.Exec(ctx, `UPDATE evidence SET expires_at=now()-interval '1 second' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	if e := s.Retain(ctx); e != nil {
		t.Fatal(e)
	}
	var doc string
	s.pool.QueryRow(ctx, `SELECT document::text FROM evidence WHERE id=$1`, id).Scan(&doc)
	if doc != "{}" {
		t.Fatal("expired content retained")
	}
	requireCode(t, call(h, "GET", "/api/v1/evidence/"+id, nil, cookie, "", ""), 410)
	oldKey, newKey := s.cfg.Key, bytes.Repeat([]byte{7}, 32)
	if e := maintenance.Rotate(ctx, s.pool, oldKey, newKey); e != nil {
		t.Fatal(e)
	}
	if _, e := New(ctx, s.pool, s.cfg); e == nil {
		t.Fatal("old key accepted after rotation")
	}
	s.cfg.Key = newKey
	rotated, e := New(ctx, s.pool, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = rotated.source(ctx, ws, connection); e != nil {
		t.Fatal("credential failed after rotation", e)
	}
	if e = maintenance.Rotate(ctx, s.pool, oldKey, bytes.Repeat([]byte{9}, 32)); e == nil {
		t.Fatal("incorrect old key accepted")
	}
	if _, _, e = rotated.source(ctx, ws, connection); e != nil {
		t.Fatal("failed rotation changed credentials")
	}
}
func TestPostgresBackupRestore(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	h := s.Handler()
	boot := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
	requireCode(t, boot, 201)
	cookie := boot.Result().Cookies()[0]
	csrf := bodyMap(t, boot)["csrf_token"].(string)
	d := createDestination(t, s, h, cookie, csrf, "https://receiver.example/fixture-secret", true)
	queued := call(h, "POST", "/api/v1/notifications/destinations/"+d.ID+"/test", map[string]bool{"confirm": true}, cookie, csrf, s.cfg.Origin)
	requireCode(t, queued, 202)
	provider := configureAI(t, s, h, cookie, csrf, "openai", "")
	aiID := queueAITest(t, s, h, cookie, csrf, provider.ID)
	var schema string
	if e := s.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); e != nil {
		t.Fatal(e)
	}
	args := []string{"compose", "-f", "../../compose.test.yaml", "exec", "-T", "postgres", "pg_dump", "-U", "sherlock_test", "-d", "sherlock_test", "-Fc", "-n", schema, "--no-owner", "--no-acl"}
	dump, e := exec.CommandContext(ctx, "docker", args...).Output()
	if e != nil {
		t.Fatal("backup command failed", e)
	}
	restored := "restore_" + stringHex(auth.Digest(auth.Token()))[:12]
	if _, e = s.pool.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{restored}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	defer s.pool.Exec(ctx, "DROP DATABASE "+pgx.Identifier{restored}.Sanitize()+" WITH (FORCE)")
	restore := exec.CommandContext(ctx, "docker", "compose", "-f", "../../compose.test.yaml", "exec", "-T", "postgres", "pg_restore", "-U", "sherlock_test", "-d", restored, "--no-owner", "--no-acl", "--exit-on-error")
	restore.Stdin = bytes.NewReader(dump)
	if output, e := restore.CombinedOutput(); e != nil {
		t.Fatalf("restore failed: %v %s", e, output)
	}
	cfg := s.pool.Config()
	cfg.ConnConfig.Database = restored
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	app, e := New(ctx, pool, s.cfg)
	if e != nil {
		t.Fatal("restored installation failed startup", e)
	}
	var ws string
	var encrypted []byte
	if e = pool.QueryRow(ctx, `SELECT workspace_id::text,secret FROM notification_destinations WHERE id=$1`, d.ID).Scan(&ws, &encrypted); e != nil {
		t.Fatal(e)
	}
	plain, e := app.vault.Open(encrypted, ws+":notification:"+d.ID)
	if e != nil || !bytes.Contains(plain, []byte("https://receiver.example/fixture-secret")) || !bytes.Contains(plain, []byte("ssssssssssssssssssssssssssssssss")) {
		t.Fatal("backup lost notification secrets")
	}
	notificationState(t, app, bodyMap(t, queued)["id"].(string), "pending")
	if e = pool.QueryRow(ctx, `SELECT secret FROM ai_providers WHERE id=$1`, provider.ID).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	plain, e = app.vault.Open(encrypted, ws+":ai:"+provider.ID)
	if e != nil || string(plain) != "fixture-only" {
		t.Fatal("backup lost AI provider key", e)
	}
	assertAIState(t, app, aiID, "queued")
	var aiJobs int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='ai_analysis'`).Scan(&aiJobs); e != nil || aiJobs != 1 {
		t.Fatal("backup lost pending AI job", e)
	}
	var jobs int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='notification'`).Scan(&jobs); e != nil || jobs == 0 {
		t.Fatal("backup lost notification job")
	}
	requireCode(t, call(app.Handler(), "GET", "/api/v1/me", nil, cookie, "", ""), 200)
	var count int
	pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&count)
	if count == 0 {
		t.Fatal("audit history not restored")
	}
}
