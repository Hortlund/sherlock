package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"sherlock/internal/ai"
	"sherlock/internal/inbound"
)

type InboundBinding struct {
	ID         string `json:"id"`
	ServiceID  string `json:"service_id"`
	Name       string `json:"name"`
	Origin     string `json:"origin"`
	Enabled    bool   `json:"enabled"`
	Revision   int    `json:"revision"`
	PerMinute  int    `json:"per_minute"`
	DailyLimit int    `json:"daily_limit"`
}

const bindingColumns = `id::text,service_id::text,name,origin,enabled,revision,per_minute,daily_limit`

func scanBinding(row pgx.Row) (InboundBinding, error) {
	var b InboundBinding
	e := row.Scan(&b.ID, &b.ServiceID, &b.Name, &b.Origin, &b.Enabled, &b.Revision, &b.PerMinute, &b.DailyLimit)
	return b, e
}
func (s *Server) inboundRoutes(m *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"GET /api/v1/inbound/bindings":              s.listInboundBindings,
		"POST /api/v1/inbound/bindings":             s.saveInboundBinding,
		"POST /api/v1/inbound/bindings/{id}":        s.saveInboundBinding,
		"POST /api/v1/inbound/bindings/{id}/delete": s.deleteInboundBinding,
		"GET /api/v1/inbound/receipts":              s.listInboundReceipts,
	} {
		m.Handle(path, s.requireSession(h))
	}
	m.HandleFunc("POST /api/v1/inbound/alerts/{id}", s.receiveInbound)
}
func (s *Server) listInboundBindings(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT `+bindingColumns+` FROM inbound_bindings WHERE workspace_id=$1 AND NOT deleted ORDER BY created_at,id LIMIT 100`, workspace(r))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []InboundBinding{}
	for rows.Next() {
		b, e := scanBinding(rows)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, b)
	}
	if e = rows.Err(); e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"items": items})
}

// Scope edits and admission use workspace -> service -> automation -> binding ->
// detector settings -> accounting -> receipt/run locks. No lock spans HTTP IO.
func lockInboundBinding(ctx context.Context, tx pgx.Tx, ws, id string) error {
	if e := lockWorkspaceScope(ctx, tx, ws); e != nil {
		return e
	}
	var sid string
	if e := tx.QueryRow(ctx, `SELECT service_id::text FROM inbound_bindings WHERE workspace_id=$1 AND id::text=$2`, ws, id).Scan(&sid); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `SELECT id FROM services WHERE workspace_id=$1 AND id=$2 FOR NO KEY UPDATE`, ws, sid); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `SELECT workspace_id FROM automation_policy WHERE workspace_id=$1 FOR SHARE`, ws); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, `SELECT id FROM inbound_bindings WHERE workspace_id=$1 AND id::text=$2 FOR UPDATE`, ws, id); e != nil {
		return e
	}
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,731903))`, ws)
	return e
}
func lockInboundReceipt(ctx context.Context, tx pgx.Tx, ws, id string) error {
	var binding string
	if e := tx.QueryRow(ctx, `SELECT binding_id::text FROM inbound_receipts WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&binding); e != nil {
		return e
	}
	return lockInboundBinding(ctx, tx, ws, binding)
}
func (s *Server) saveInboundBinding(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Binding InboundBinding `json:"binding"`
		Secret  string         `json:"signing_secret"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	b := in.Binding
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 100 || (b.Origin != "demo" && b.Origin != "live") || b.PerMinute < 1 || b.PerMinute > 60 || b.DailyLimit < 1 || b.DailyLimit > 100 || (in.Secret != "" && (len(in.Secret) < 32 || len(in.Secret) > 256 || strings.ContainsAny(in.Secret, "\r\n"))) {
		s.fail(w, r, 400, "invalid_binding", "Supply a name, service/origin, 1–60 requests per minute, 1–100 investigations per UTC day, and a signing secret of 32–256 bytes.")
		return
	}
	ctx, ws, id := r.Context(), workspace(r), r.PathValue("id")
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	if id != "" {
		e = lockInboundBinding(ctx, tx, ws, id)
	} else {
		e = lockWorkspaceScope(ctx, tx, ws)
	}
	if e != nil {
		s.missing(w, r, e)
		return
	}
	if id != "" {
		old, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM inbound_bindings WHERE workspace_id=$1 AND id=$2 AND NOT deleted`, ws, id))
		if err != nil {
			s.missing(w, r, err)
			return
		}
		if old.ServiceID != b.ServiceID || old.Origin != b.Origin {
			s.fail(w, r, 400, "immutable_scope", "Create a new binding to change service or origin.")
			return
		}
	}
	var origin string
	e = tx.QueryRow(ctx, `SELECT origin FROM services WHERE workspace_id=$1 AND id::text=$2 FOR NO KEY UPDATE`, ws, b.ServiceID).Scan(&origin)
	if e != nil || origin != b.Origin {
		s.fail(w, r, 400, "invalid_service", "Choose an existing service and its exact origin.")
		return
	}
	if e = aiWorkspaceLock(ctx, tx, ws); e != nil {
		s.internal(w, r, e)
		return
	}
	var encrypted []byte
	if id == "" {
		if in.Secret == "" {
			s.fail(w, r, 400, "secret_required", "Supply a new signing secret.")
			return
		}
		var count int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM inbound_bindings WHERE workspace_id=$1 AND NOT deleted`, ws).Scan(&count); e != nil {
			s.internal(w, r, e)
			return
		}
		if count >= 100 {
			s.fail(w, r, 409, "binding_limit", "At most 100 active binding configurations are supported.")
			return
		}
		if e = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); e != nil {
			s.internal(w, r, e)
			return
		}
		encrypted = s.vault.Seal([]byte(in.Secret), ws+":inbound:"+id)
		_, e = tx.Exec(ctx, `INSERT INTO inbound_bindings(id,workspace_id,service_id,name,origin,enabled,secret,per_minute,daily_limit) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, ws, b.ServiceID, b.Name, b.Origin, b.Enabled, encrypted, b.PerMinute, b.DailyLimit)
	} else {
		old, err := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM inbound_bindings WHERE workspace_id=$1 AND id=$2 AND NOT deleted`, ws, id))
		if err != nil {
			s.missing(w, r, err)
			return
		}
		if old.ServiceID != b.ServiceID || old.Origin != b.Origin {
			s.fail(w, r, 400, "immutable_scope", "Create a new binding to change service or origin.")
			return
		}
		if old.Revision != b.Revision {
			s.fail(w, r, 409, "revision_conflict", "Binding changed; reload before saving.")
			return
		}
		if in.Secret != "" {
			encrypted = s.vault.Seal([]byte(in.Secret), ws+":inbound:"+id)
		}
		_, e = tx.Exec(ctx, `UPDATE inbound_bindings SET name=$3,enabled=$4,per_minute=$5,daily_limit=$6,revision=revision+1,secret=COALESCE($7,secret) WHERE workspace_id=$1 AND id=$2`, ws, id, b.Name, b.Enabled, b.PerMinute, b.DailyLimit, encrypted)
		if e == nil {
			e = cancelInboundBinding(ctx, tx, ws, id, false)
		}
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'inbound.binding_saved')`, ws)
	}
	if e == nil {
		b, e = scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM inbound_bindings WHERE workspace_id=$1 AND id=$2`, ws, id))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, b)
}
func cancelInboundBinding(ctx context.Context, tx pgx.Tx, ws, id string, purge bool) error {
	_, e := tx.Exec(ctx, `UPDATE inbound_receipts SET state=CASE WHEN state IN ('queued','collecting','collected') THEN 'cancelled' ELSE state END,reason=CASE WHEN state IN ('queued','collecting','collected') THEN 'Binding changed or disabled; pending work cancelled.' ELSE reason END,title=CASE WHEN $3 THEN NULL ELSE title END WHERE workspace_id=$1 AND binding_id=$2`, ws, id, purge)
	if e != nil {
		return e
	}
	return cancelInboundRuns(ctx, tx, ws, id)
}
func cancelInboundRuns(ctx context.Context, tx pgx.Tx, ws, binding string) error {
	_, e := tx.Exec(ctx, `WITH stopped AS (UPDATE runs SET cancelled=true,state='cancelled',finished_at=now() WHERE workspace_id=$1 AND inbound_receipt_id IN(SELECT id FROM inbound_receipts WHERE workspace_id=$1 AND ($2='' OR binding_id::text=$2)) AND state IN ('queued','collecting','evaluating','reporting') RETURNING workspace_id,id) INSERT INTO run_events(workspace_id,run_id,stage,message) SELECT workspace_id,id,'cancelled','Inbound consent changed; processing cancelled.' FROM stopped ON CONFLICT DO NOTHING`, ws, binding)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE ai_runs SET cancel_requested=true,state='cancelled',reason='Inbound consent changed; pending processing cancelled.',payload=NULL,finished_at=now(),accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE accounting END WHERE workspace_id=$1 AND inbound_receipt_id IN(SELECT id FROM inbound_receipts WHERE workspace_id=$1 AND ($2='' OR binding_id::text=$2)) AND state IN ('queued','analyzing')`, ws, binding)
	return e
}

// The caller holds the workspace row exclusively before editing the service.
// Cancel receipts without taking accounting/run locks: provider configuration
// writers may hold accounting while their workspace FK check waits for us.
// Workers observe this durable revocation at their final dispatch boundary.
func cancelInboundServiceReceipts(ctx context.Context, tx pgx.Tx, ws, service string) error {
	_, e := tx.Exec(ctx, `UPDATE inbound_receipts x SET state='cancelled',reason='Service monitoring disabled; pending inbound work cancelled.' WHERE workspace_id=$1 AND binding_id IN (SELECT id FROM inbound_bindings WHERE workspace_id=$1 AND service_id=$2) AND (state IN ('queued','collecting','collected') OR (state='ai_queued' AND EXISTS(SELECT 1 FROM ai_runs a WHERE a.workspace_id=x.workspace_id AND a.inbound_receipt_id=x.id AND a.state IN ('queued','analyzing'))))`, ws, service)
	return e
}

func (s *Server) deleteInboundBinding(w http.ResponseWriter, r *http.Request) {
	ctx, ws, id := r.Context(), workspace(r), r.PathValue("id")
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = lockInboundBinding(ctx, tx, ws, id); e != nil {
		s.missing(w, r, e)
		return
	}
	if e = aiWorkspaceLock(ctx, tx, ws); e == nil {
		_, e = tx.Exec(ctx, `UPDATE inbound_bindings SET enabled=false,deleted=true,secret=''::bytea,revision=revision+1 WHERE workspace_id=$1 AND id=$2`, ws, id)
	}
	if e == nil {
		e = cancelInboundBinding(ctx, tx, ws, id, true)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'inbound.binding_deleted')`, ws)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]bool{"ok": true})
}
func (s *Server) receiveInbound(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, inbound.BodyLimit))
	if e != nil {
		s.fail(w, r, 413, "payload_too_large", "Inbound alerts are limited to 16 KiB.")
		return
	}
	id := r.PathValue("id")
	var ws string
	var encrypted []byte
	var revision int
	e = s.pool.QueryRow(ctx, `SELECT workspace_id::text,secret,revision FROM inbound_bindings WHERE id::text=$1 AND enabled AND NOT deleted`, id).Scan(&ws, &encrypted, &revision)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	secret, openErr := s.vault.Open(encrypted, ws+":inbound:"+id)
	if e != nil || openErr != nil || len(r.Header.Values("X-Sherlock-Timestamp")) != 1 || len(r.Header.Values("X-Sherlock-Signature")) != 1 || !inbound.Verify(secret, r.Header.Get("X-Sherlock-Timestamp"), r.Header.Get("X-Sherlock-Signature"), body, time.Now()) {
		s.fail(w, r, 401, "invalid_signature", "A current signature for an enabled inbound binding is required.")
		return
	}
	alert, e := inbound.Decode(body)
	if e != nil {
		s.fail(w, r, 400, "invalid_alert", e.Error())
		return
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = lockInboundBinding(ctx, tx, ws, id); e != nil {
		s.internal(w, r, e)
		return
	}
	b, e := scanBinding(tx.QueryRow(ctx, `SELECT `+bindingColumns+` FROM inbound_bindings WHERE workspace_id=$1 AND id=$2 AND NOT deleted`, ws, id))
	if e != nil || !b.Enabled || b.Revision != revision {
		s.fail(w, r, 401, "binding_changed", "Binding changed while authenticating; sign with the current enabled configuration.")
		return
	}
	// Durable fixed-window rate limit includes authenticated duplicate requests.
	var permitted bool
	e = tx.QueryRow(ctx, `UPDATE inbound_bindings SET rate_count=CASE WHEN rate_start<=now()-interval '1 minute' THEN 1 ELSE LEAST(rate_count+1,per_minute+1) END,rate_start=CASE WHEN rate_start<=now()-interval '1 minute' THEN now() ELSE rate_start END WHERE workspace_id=$1 AND id=$2 RETURNING rate_count<=per_minute`, ws, id).Scan(&permitted)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if !permitted {
		if e = tx.Commit(ctx); e != nil {
			s.internal(w, r, e)
			return
		}
		w.Header().Set("Retry-After", "60")
		s.fail(w, r, 429, "rate_limited", "Binding request rate exceeded; retry after one minute.")
		return
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	var receipt, oldDigest string
	e = tx.QueryRow(ctx, `SELECT id::text,digest FROM inbound_receipts WHERE workspace_id=$1 AND binding_id=$2 AND external_id=$3`, ws, id, alert.EventID).Scan(&receipt, &oldDigest)
	if e == nil {
		if e = tx.Commit(ctx); e != nil {
			s.internal(w, r, e)
			return
		}
		if oldDigest != digest {
			s.fail(w, r, 409, "event_conflict", "Event ID already exists with different exact body bytes.")
			return
		}
		s.json(w, 202, map[string]any{"id": receipt, "reused": true})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	cfg, e := loadDetectorSettings(ctx, tx, ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if !inbound.ValidWindow(alert, cfg.BaselineMinutes, time.Now()) {
		s.fail(w, r, 422, "unsupported_window", "Use a 5–60 minute incident ending within the last seven days, not in the future, and at most two hours including comparison context. No window is silently substituted.")
		return
	}
	v, e := scanService(tx.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id=$2`, ws, b.ServiceID))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	policy, e := loadAutomationPolicy(ctx, tx, ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	// Serialize receipt consent snapshots with AI/provider edits.
	if e = aiWorkspaceLock(ctx, tx, ws); e != nil {
		s.internal(w, r, e)
		return
	}
	provider, ap, consentErr := inboundAIConsent(ctx, tx, ws, v, policy)
	state, reason := "queued", "Verified receipt awaiting admission."
	if reason = inboundPolicyReason(policy, v, b); reason != "" {
		state = "skipped"
	} else if consentErr != nil {
		state, reason = "skipped", consentErr.Error()
	} else {
		reason = "Verified receipt awaiting admission."
	}
	e = tx.QueryRow(ctx, `INSERT INTO inbound_receipts(workspace_id,binding_id,binding_revision,policy_revision,mapping_revision,external_id,digest,title,window_start,window_end,state,reason,ai_policy_revision,provider_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id::text`, ws, id, b.Revision, policy.Revision, v.Revision, alert.EventID, digest, ai.Clean(alert.Title, v.Mapping.SensitivePatterns), alert.Start, alert.End, state, reason, ap.Revision, provider.Revision).Scan(&receipt)
	if e == nil && state == "queued" {
		e = s.queueInbound(ctx, tx, inboundArgs{ws, receipt})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 202, map[string]any{"id": receipt, "reused": false})
}
func (s *Server) queueInbound(ctx context.Context, tx pgx.Tx, a inboundArgs) error {
	j, e := s.jobs.InsertTx(ctx, tx, a, &river.InsertOpts{Queue: "inbound", MaxAttempts: 6})
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE inbound_receipts SET job_id=$3 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID, j.Job.ID)
	return e
}
func (s *Server) listInboundReceipts(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT x.id::text,x.external_id,x.binding_id::text,b.name,b.service_id::text,x.state,x.reason,x.created_at,CASE WHEN x.expires_at>now() THEN COALESCE(x.title,'') ELSE '' END,x.window_start,x.window_end,COALESCE(r.id::text,''),COALESCE(r.state,''),COALESCE(a.id::text,''),COALESCE(a.state,''),COALESCE(a.reason,'') FROM inbound_receipts x JOIN inbound_bindings b ON b.workspace_id=x.workspace_id AND b.id=x.binding_id LEFT JOIN runs r ON r.workspace_id=x.workspace_id AND r.inbound_receipt_id=x.id LEFT JOIN ai_runs a ON a.workspace_id=x.workspace_id AND a.inbound_receipt_id=x.id WHERE x.workspace_id=$1 AND ($2='' OR (x.created_at,x.id)<(SELECT created_at,id FROM inbound_receipts WHERE workspace_id=$1 AND id::text=$2)) ORDER BY x.created_at DESC,x.id DESC LIMIT 51`, workspace(r), r.URL.Query().Get("cursor"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, event, binding, name, sid, state, reason, title, run, rs, aid, as, ar string
		var at, start, end time.Time
		if e = rows.Scan(&id, &event, &binding, &name, &sid, &state, &reason, &at, &title, &start, &end, &run, &rs, &aid, &as, &ar); e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, map[string]any{"id": id, "event_id": event, "binding_id": binding, "binding_name": name, "service_id": sid, "state": state, "reason": reason, "created_at": at, "title": title, "start": start, "end": end, "investigation_id": run, "investigation_state": rs, "ai_run_id": aid, "ai_state": as, "ai_reason": ar})
	}
	if e = rows.Err(); e != nil {
		s.internal(w, r, e)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = items[49]["id"].(string)
	}
	s.json(w, 200, map[string]any{"items": items, "next_cursor": next})
}
