package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"sherlock/internal/ai"
	"sherlock/internal/auth"
	"sherlock/internal/config"
	"sherlock/internal/connectors"
	vault "sherlock/internal/crypto"
	store "sherlock/internal/db/sqlc"
	"sherlock/internal/notifications"
)

type Server struct {
	aiClient  *ai.Client
	aiFixture bool
	jobs      *river.Client[pgx.Tx]
	pool      *pgxpool.Pool
	q         *store.Queries
	cfg       config.Config
	passwords chan struct{}
	dummyHash string
	policy    *connectors.Policy
	notifier  *notifications.Sender
	vault     *vault.Vault
}
type contextKey string

const requestIDKey contextKey = "request_id"
const sessionKey contextKey = "session"
const tokenKey contextKey = "token"

func New(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (*Server, error) {
	v, err := vault.New(cfg.Key)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73190002)`); err != nil {
		return nil, err
	}
	var check []byte
	err = tx.QueryRow(ctx, `SELECT key_check FROM installation_secrets WHERE id=true`).Scan(&check)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO installation_secrets(key_check) VALUES($1)`, v.Seal([]byte("sherlock-key-check"), "installation"))
	} else if err == nil {
		plain, e := v.Open(check, "installation")
		if e != nil || string(plain) != "sherlock-key-check" {
			return nil, errors.New("encryption key does not match this installation; restore the original key")
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	dummy, err := auth.HashPassword(auth.Token())
	if err != nil {
		return nil, err
	}
	policy, err := connectors.NewPolicy(cfg.AllowedCIDRs, cfg.AllowedHosts)
	if err != nil {
		return nil, err
	}
	notifier, err := notifications.NewSender(cfg.NotificationCIDRs, cfg.NotificationHosts, cfg.NotificationHTTPHosts)
	if err != nil {
		return nil, err
	}
	server := &Server{aiClient: ai.NewClient(), notifier: notifier, policy: policy, vault: v, pool: pool, q: store.New(pool), cfg: cfg, passwords: make(chan struct{}, 2), dummyHash: dummy}
	if err = server.initJobs(ctx); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { s.json(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if s.pool.Ping(ctx) != nil {
			s.fail(w, r, 503, "not_ready", "Database unavailable.")
			return
		}
		s.json(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /api/v1/setup/status", s.setupStatus)
	mux.HandleFunc("POST /api/v1/setup/bootstrap", s.rateLimited(s.bootstrap))
	mux.HandleFunc("POST /api/v1/auth/login", s.rateLimited(s.login))
	mux.Handle("GET /api/v1/me", s.requireSession(http.HandlerFunc(s.me)))
	mux.Handle("POST /api/v1/auth/logout", s.requireSession(http.HandlerFunc(s.logout)))
	mux.Handle("POST /api/v1/auth/password", s.requireSession(http.HandlerFunc(s.rateLimited(s.changePassword))))
	s.sourceRoutes(mux)
	s.runRoutes(mux)
	s.investigationRoutes(mux)
	s.notificationRoutes(mux)
	s.aiRoutes(mux)
	s.automationRoutes(mux)
	s.inboundRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { s.fail(w, r, 404, "not_found", "Endpoint not found.") })
	mux.HandleFunc("/", s.static)
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := auth.Token()[:16]
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if s.cfg.SecureCookies {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// Mutations, including login, require the exact configured Origin. Only
		// the separately HMAC-authenticated inbound endpoint is exempt.
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			signedInbound := r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/inbound/alerts/") && len(strings.TrimPrefix(r.URL.Path, "/api/v1/inbound/alerts/")) == 36 && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/api/v1/inbound/alerts/"), "/")
			if !signedInbound && (r.Header.Get("Origin") != s.cfg.Origin || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
				s.fail(w, r, 403, "origin_denied", "Request origin is not allowed.")
				return
			}
			kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || kind != "application/json" {
				s.fail(w, r, 415, "json_required", "Send application/json.")
				return
			}
		}
		defer func() {
			if recover() != nil {
				slog.Error("request panic", "request_id", id)
				s.fail(w, r, 500, "internal_error", "Request could not be completed.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			peer = r.RemoteAddr
		}
		// Ignore forwarded IP headers: only the directly connected peer is trusted.
		buckets := []struct {
			key   string
			limit int32
		}{{"global", 60}, {"peer:" + stringHex(auth.Digest(peer)), 10}}
		for _, bucket := range buckets {
			count, err := s.q.CheckRate(r.Context(), bucket.key)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if count > bucket.limit {
				w.Header().Set("Retry-After", "60")
				s.fail(w, r, 429, "rate_limited", "Too many attempts. Try again in a minute.")
				return
			}
		}
		select {
		case s.passwords <- struct{}{}:
			defer func() { <-s.passwords }()
			next(w, r)
		default:
			w.Header().Set("Retry-After", "2")
			s.fail(w, r, 429, "busy", "Authentication is busy. Try again shortly.")
		}
	}
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.q.IsInitialized(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.json(w, 200, map[string]bool{"initialized": initialized})
}

type bootstrapRequest struct {
	SetupToken    string `json:"setup_token"`
	WorkspaceName string `json:"workspace_name"`
	Email         string `json:"email"`
	Password      string `json:"password"`
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	var in bootstrapRequest
	if !s.decode(w, r, &in) {
		return
	}
	if !auth.Equal(in.SetupToken, s.cfg.SetupToken) {
		s.fail(w, r, 403, "invalid_setup_token", "The setup token is incorrect.")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.WorkspaceName = strings.TrimSpace(in.WorkspaceName)
	if !validEmail(in.Email) || len(in.WorkspaceName) < 1 || len(in.WorkspaceName) > 80 || !auth.ValidPassword(in.Password) {
		s.fail(w, r, 400, "invalid_input", "Use a valid email, a workspace name of 1–80 bytes, and a password of 12 or more characters (maximum 256 bytes).")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(73190003)`); err != nil {
		s.internal(w, r, err)
		return
	}
	q := s.q.WithTx(tx)
	initialized, err := q.IsInitialized(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if initialized {
		s.fail(w, r, 409, "already_initialized", "This installation already has an administrator. Sign in instead.")
		return
	}
	workspace, err := q.CreateWorkspace(r.Context(), in.WorkspaceName)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	user, err := q.CreateAdmin(r.Context(), store.CreateAdminParams{WorkspaceID: workspace.ID, Email: in.Email, PasswordHash: hash})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	token := auth.Token()
	csrf := s.csrfFor(token)
	err = q.CreateSession(r.Context(), store.CreateSessionParams{TokenHash: auth.Digest(token), WorkspaceID: workspace.ID, UserID: user.ID, CsrfHash: auth.Digest(csrf)})
	if err == nil {
		err = q.Audit(r.Context(), store.AuditParams{WorkspaceID: workspace.ID, Action: "workspace.bootstrapped"})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.setCookie(w, token)
	s.json(w, 201, map[string]string{"csrf_token": csrf})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	user, err := s.q.FindUser(r.Context(), in.Email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	hash := user.PasswordHash
	if err != nil {
		hash = s.dummyHash
	}
	valid := auth.VerifyPassword(in.Password, hash)
	if err != nil || !valid {
		s.fail(w, r, 401, "invalid_credentials", "Email or password is incorrect.")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := s.q.WithTx(tx)
	// Serialize with password changes. Recheck the hash so an old password cannot
	// create a fresh session after a concurrent password change has revoked them.
	var current string
	err = tx.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, user.WorkspaceID, user.ID).Scan(&current)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if current != user.PasswordHash {
		s.fail(w, r, 401, "invalid_credentials", "Credentials changed. Sign in again.")
		return
	}
	if cookie, e := r.Cookie(s.cookieName()); e == nil {
		if err = q.DeleteSession(r.Context(), auth.Digest(cookie.Value)); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	token := auth.Token()
	csrf := s.csrfFor(token)
	err = q.CreateSession(r.Context(), store.CreateSessionParams{TokenHash: auth.Digest(token), WorkspaceID: user.WorkspaceID, UserID: user.ID, CsrfHash: auth.Digest(csrf)})
	if err == nil {
		err = q.Audit(r.Context(), store.AuditParams{WorkspaceID: user.WorkspaceID, Action: "auth.login"})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.setCookie(w, token)
	s.json(w, 200, map[string]string{"csrf_token": csrf})
}
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.cookieName())
		if err != nil || len(cookie.Value) != 43 {
			s.fail(w, r, 401, "unauthenticated", "Sign in to continue.")
			return
		}
		session, err := s.q.GetSession(r.Context(), auth.Digest(cookie.Value))
		if errors.Is(err, pgx.ErrNoRows) {
			s.clearCookie(w)
			s.fail(w, r, 401, "unauthenticated", "Your session has expired. Sign in again.")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if subtle.ConstantTimeCompare(auth.Digest(r.Header.Get("X-CSRF-Token")), session.CsrfHash) != 1 {
				s.fail(w, r, 403, "csrf_denied", "Session verification failed. Reload and try again.")
				return
			}
		}
		ctx := context.WithValue(r.Context(), sessionKey, session)
		ctx = context.WithValue(ctx, tokenKey, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value(sessionKey).(store.GetSessionRow)
	csrf := s.csrfFor(r.Context().Value(tokenKey).(string))
	s.json(w, 200, map[string]any{"user": map[string]string{"id": session.ID.String(), "email": session.Email}, "workspace": map[string]string{"id": session.WorkspaceID.String(), "name": session.WorkspaceName}, "csrf_token": csrf, "expires_at": session.ExpiresAt.Time})
}
func (s *Server) csrfFor(token string) string { return stringHex(auth.Digest("csrf:" + token)) }
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value(sessionKey).(store.GetSessionRow)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := s.q.WithTx(tx)
	err = q.DeleteSession(r.Context(), auth.Digest(r.Context().Value(tokenKey).(string)))
	if err == nil {
		err = q.Audit(r.Context(), store.AuditParams{WorkspaceID: session.WorkspaceID, Action: "auth.logout"})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.clearCookie(w)
	s.json(w, 200, map[string]bool{"ok": true})
}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !auth.ValidPassword(in.New) {
		s.fail(w, r, 400, "invalid_password", "Use at least 12 characters and at most 256 bytes.")
		return
	}
	session := r.Context().Value(sessionKey).(store.GetSessionRow)
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	q := s.q.WithTx(tx)
	var hash string
	err = tx.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, session.WorkspaceID, session.ID).Scan(&hash)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// A session may have been revoked while this request waited for the user lock.
	if _, err = q.GetSession(r.Context(), auth.Digest(r.Context().Value(tokenKey).(string))); err != nil {
		s.fail(w, r, 401, "unauthenticated", "Sign in again.")
		return
	}
	if !auth.VerifyPassword(in.Current, hash) {
		s.fail(w, r, 400, "invalid_password", "Current password is incorrect.")
		return
	}
	hash, err = auth.HashPassword(in.New)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = q.UpdatePassword(r.Context(), store.UpdatePasswordParams{WorkspaceID: session.WorkspaceID, ID: session.ID, PasswordHash: hash})
	if err == nil {
		err = q.DeleteUserSessions(r.Context(), store.DeleteUserSessionsParams{WorkspaceID: session.WorkspaceID, UserID: session.ID})
	}
	if err == nil {
		err = q.Audit(r.Context(), store.AuditParams{WorkspaceID: session.WorkspaceID, Action: "auth.password_changed"})
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.clearCookie(w)
	s.json(w, 200, map[string]bool{"ok": true})
}
func (s *Server) cookieName() string {
	if s.cfg.SecureCookies {
		return "__Host-sherlock_session"
	}
	return "sherlock_session"
}
func (s *Server) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
}
func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		s.fail(w, r, 400, "invalid_json", "Request must be a valid JSON object with supported fields.")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		s.fail(w, r, 400, "invalid_json", "Only one JSON object is allowed.")
		return false
	}
	return true
}
func (s *Server) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	s.json(w, status, map[string]any{"code": code, "message": message, "request_id": r.Context().Value(requestIDKey)})
}
func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "request_id", r.Context().Value(requestIDKey), "error_type", errorKind(err))
	s.fail(w, r, 500, "internal_error", "Request could not be completed. Try again.")
}
func errorKind(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "storage_or_internal"
}
func validEmail(s string) bool {
	a, e := mail.ParseAddress(s)
	return e == nil && a.Address == s && len(s) <= 254 && strings.Contains(s, "@")
}
func stringHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[2*i] = hex[v>>4]
		out[2*i+1] = hex[v&15]
	}
	return string(out)
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		s.fail(w, r, 405, "method_not_allowed", "Method not allowed.")
		return
	}
	name := filepath.Clean("/" + r.URL.Path)
	if strings.HasPrefix(name, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, filepath.Join(s.cfg.WebDir, name))
		return
	}
	if name != "/" && name != "/setup" && name != "/login" && name != "/settings" && name != "/connections" && name != "/services" && name != "/findings" && name != "/investigations" {
		s.fail(w, r, 404, "not_found", "Page not found.")
		return
	}
	path := filepath.Join(s.cfg.WebDir, "index.html")
	if _, err := os.Stat(path); err != nil {
		s.fail(w, r, 503, "ui_not_built", "Build the web application first.")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}

func (s *Server) Cleanup(ctx context.Context) { _ = s.q.CleanupSessions(ctx); _ = s.q.CleanupAuth(ctx) }
