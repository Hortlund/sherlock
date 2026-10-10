package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sherlock/internal/auth"
	"sherlock/internal/config"
	"sherlock/migrations"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dsn := os.Getenv("SHERLOCK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SHERLOCK_TEST_DATABASE_URL for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + stringHex(auth.Digest(auth.Token()))[:16]
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	// Match cmd/sherlock: workers retain advisory-lock connections while doing
	// other queries. The CPU-dependent pgx default can deadlock four workers on CI.
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	})
	// Race two application startups through migration locking.
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := migrations.Apply(ctx, pool); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	s, err := New(ctx, pool, config.Config{Origin: "http://localhost:8080", SetupToken: strings.Repeat("t", 32), Key: bytes.Repeat([]byte{3}, 32), WebDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Handler()
}
func call(h http.Handler, method, path string, body any, cookie *http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.RemoteAddr = "192.0.2.10:1234"
	if method != "GET" {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func bodyMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func requireCode(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("got %d expected %d: %s", w.Code, status, w.Body.String())
	}
}
func bootstrapBody() map[string]string {
	return map[string]string{"setup_token": strings.Repeat("t", 32), "workspace_name": "Test workspace", "email": "owner@example.com", "password": "initial long password"}
}
func TestBootstrapRaceAndScope(t *testing.T) {
	s, h := testServer(t)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for r := range results {
		counts[r.Code]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("bootstrap race: %v", counts)
	}
	var users, workspaces int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM users").Scan(&users)
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM workspaces").Scan(&workspaces)
	if users != 1 || workspaces != 1 {
		t.Fatal("bootstrap was not atomic")
	}
	requireCode(t, call(h, "GET", "/api/v1/me", nil, nil, "", ""), 401)
	// A changed encryption key must not silently make persisted secrets unreadable.
	wrong := s.cfg
	wrong.Key = bytes.Repeat([]byte{4}, 32)
	if _, err := New(context.Background(), s.pool, wrong); err == nil {
		t.Fatal("wrong installation key accepted")
	}
}
func TestSessionLifecycleAndCSRF(t *testing.T) {
	s, h := testServer(t)
	origin := s.cfg.Origin
	requireCode(t, call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", "https://attacker.example"), 403)
	bad := bootstrapBody()
	bad["setup_token"] = "wrong"
	requireCode(t, call(h, "POST", "/api/v1/setup/bootstrap", bad, nil, "", origin), 403)
	created := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", origin)
	requireCode(t, created, 201)
	cookies := created.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing cookie")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatal("insecure cookie")
	}
	csrf := bodyMap(t, created)["csrf_token"].(string)
	me := call(h, "GET", "/api/v1/me", nil, cookie, "", "")
	requireCode(t, me, 200)
	if bodyMap(t, me)["csrf_token"] != csrf {
		t.Fatal("read changed CSRF token")
	}
	if strings.Contains(me.Body.String(), "password") || strings.Contains(me.Body.String(), s.cfg.SetupToken) || strings.Contains(me.Body.String(), cookie.Value) {
		t.Fatal("secret disclosed")
	}
	requireCode(t, call(h, "POST", "/api/v1/auth/logout", map[string]string{}, cookie, "", origin), 403)
	requireCode(t, call(h, "POST", "/api/v1/auth/logout", map[string]string{}, cookie, csrf, "https://attacker.example"), 403)
	requireCode(t, call(h, "POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "wrong password"}, nil, "", origin), 401)
	login := call(h, "POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "initial long password"}, cookie, "", origin)
	requireCode(t, login, 200)
	requireCode(t, call(h, "GET", "/api/v1/me", nil, cookie, "", ""), 401)
	cookie = login.Result().Cookies()[0]
	csrf = bodyMap(t, login)["csrf_token"].(string)
	// A fresh server instance sees the same persisted session and installation key.
	restarted, err := New(context.Background(), s.pool, s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, call(restarted.Handler(), "GET", "/api/v1/me", nil, cookie, "", ""), 200)
	changed := call(h, "POST", "/api/v1/auth/password", map[string]string{"current_password": "initial long password", "new_password": "replacement long password"}, cookie, csrf, origin)
	requireCode(t, changed, 200)
	requireCode(t, call(h, "GET", "/api/v1/me", nil, cookie, "", ""), 401)
	requireCode(t, call(h, "POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "initial long password"}, nil, "", origin), 401)
	login = call(h, "POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "replacement long password"}, nil, "", origin)
	requireCode(t, login, 200)
	cookie = login.Result().Cookies()[0]
	csrf = bodyMap(t, login)["csrf_token"].(string)
	requireCode(t, call(h, "POST", "/api/v1/auth/logout", map[string]string{}, cookie, csrf, origin), 200)
	requireCode(t, call(h, "GET", "/api/v1/me", nil, cookie, "", ""), 401)
	var hash string
	if err = s.pool.QueryRow(context.Background(), `SELECT password_hash FROM users`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatal("password not hashed")
	}
}
func TestAuthExpiryLimitsAndKeyScope(t *testing.T) {
	s, h := testServer(t)
	origin := s.cfg.Origin
	created := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", origin)
	requireCode(t, created, 201)
	cookie := created.Result().Cookies()[0]
	_, err := s.pool.Exec(context.Background(), `UPDATE sessions SET expires_at=now()-interval '1 second'`)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, call(h, "GET", "/api/v1/me", nil, cookie, "", ""), 401)
	for i := 0; i < 9; i++ {
		w := call(h, "POST", "/api/v1/auth/login", map[string]string{"email": fmt.Sprintf("unknown%d@example.com", i), "password": "wrong password"}, nil, "", origin)
		requireCode(t, w, 401)
	}
	requireCode(t, call(h, "POST", "/api/v1/auth/login", map[string]string{"email": "new@example.com", "password": "wrong password"}, nil, "", origin), 429)
	s.Cleanup(context.Background())
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Fatal("expired session not cleaned")
	}
}
func TestSecureCookieAndStaticIsolation(t *testing.T) {
	s, _ := testServer(t)
	s.cfg.SecureCookies = true
	s.cfg.Origin = "https://sherlock.example.com"
	h := s.Handler()
	created := call(h, "POST", "/api/v1/setup/bootstrap", bootstrapBody(), nil, "", s.cfg.Origin)
	requireCode(t, created, 201)
	cookie := created.Result().Cookies()[0]
	if !cookie.Secure || cookie.Name != "__Host-sherlock_session" {
		t.Fatal("HTTPS cookie missing protection")
	}
	requireCode(t, call(h, "GET", "/.env", nil, nil, "", ""), 404)
	requireCode(t, call(h, "GET", "/api/v1/missing", nil, nil, "", ""), 404)
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("auth response cacheable")
	}
}
