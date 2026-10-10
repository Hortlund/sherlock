package api

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"net/http"
	"net/url"
	"sherlock/internal/auth"
	"sherlock/internal/notifications"
	"strings"
	"time"
)

type Destination struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Kind      string              `json:"kind"`
	Enabled   bool                `json:"enabled"`
	Revision  int                 `json:"revision"`
	Rules     notifications.Rules `json:"rules"`
	SecretSet bool                `json:"secret_set"`
}

const destinationColumns = `id::text,name,kind,enabled,revision,rules,octet_length(secret)>0`

func scanDestination(row pgx.Row) (Destination, error) {
	var d Destination
	var raw []byte
	e := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Enabled, &d.Revision, &raw, &d.SecretSet)
	if e == nil {
		e = json.Unmarshal(raw, &d.Rules)
		if d.Rules.Services == nil {
			d.Rules.Services = []string{}
		}
	}
	return d, e
}
func (s *Server) notificationRoutes(m *http.ServeMux) {
	for p, h := range map[string]http.HandlerFunc{
		"GET /api/v1/notifications/destinations":               s.listDestinations,
		"POST /api/v1/notifications/destinations":              s.saveDestination,
		"POST /api/v1/notifications/destinations/{id}":         s.saveDestination,
		"POST /api/v1/notifications/destinations/{id}/delete":  s.deleteDestination,
		"POST /api/v1/notifications/destinations/{id}/preview": s.previewNotification,
		"POST /api/v1/notifications/destinations/{id}/test":    s.testNotification,
		"GET /api/v1/notifications/deliveries":                 s.listDeliveries,
		"POST /api/v1/notifications/deliveries/{id}/retry":     s.retryNotification,
	} {
		m.Handle(p, s.requireSession(h))
	}
}
func (s *Server) listDestinations(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT `+destinationColumns+` FROM notification_destinations WHERE workspace_id=$1 AND NOT deleted ORDER BY created_at,id`, workspace(r))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []Destination{}
	for rows.Next() {
		d, e := scanDestination(rows)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, d)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	u, _ := url.Parse(s.cfg.Origin)
	s.json(w, 200, map[string]any{"items": items, "app_url": s.cfg.Origin, "local_links": u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"})
}
func (s *Server) saveDestination(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string              `json:"name"`
		Kind       string              `json:"kind"`
		Enabled    bool                `json:"enabled"`
		Revision   int                 `json:"revision"`
		Rules      notifications.Rules `json:"rules"`
		URL        string              `json:"url"`
		SigningKey string              `json:"signing_key"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 80 {
		s.fail(w, r, 400, "invalid_destination", "Choose a destination name of 1–80 characters.")
		return
	}
	if e := in.Rules.Validate(); e != nil {
		s.fail(w, r, 400, "invalid_rules", e.Error())
		return
	}
	if in.Rules.Services == nil {
		in.Rules.Services = []string{}
	}
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	// Serialize creation limits; destination edits use revision preconditions.
	if _, e = tx.Exec(ctx, `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
		s.internal(w, r, e)
		return
	}
	id := r.PathValue("id")
	secret := notifications.Secret{}
	revision := 1
	if id != "" {
		var raw []byte
		var oldKind string
		e = tx.QueryRow(ctx, `SELECT id::text,kind,revision,secret FROM notification_destinations WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted FOR UPDATE`, ws, id).Scan(&id, &oldKind, &revision, &raw)
		if e != nil {
			s.missing(w, r, e)
			return
		}
		if revision != in.Revision {
			s.fail(w, r, 409, "revision_conflict", "Destination changed. Refresh before saving.")
			return
		}
		if oldKind != in.Kind {
			s.fail(w, r, 400, "immutable_kind", "Create a new destination to change its type.")
			return
		}
		plain, e := s.vault.Open(raw, ws+":notification:"+id)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		if e = json.Unmarshal(plain, &secret); e != nil {
			s.internal(w, r, e)
			return
		}
		revision++
	} else {
		var count int
		e = tx.QueryRow(ctx, `SELECT count(*) FROM notification_destinations WHERE workspace_id=$1 AND NOT deleted`, ws).Scan(&count)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		if count >= 20 {
			s.fail(w, r, 400, "destination_limit", "At most 20 notification destinations are supported.")
			return
		}
		if e = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); e != nil {
			s.internal(w, r, e)
			return
		}
	}
	if in.URL != "" {
		secret.URL = in.URL
	}
	if in.SigningKey != "" {
		secret.SigningKey = in.SigningKey
	}
	if e = notifications.ValidateURL(in.Kind, secret.URL, s.notifier.HTTPHosts); e != nil {
		s.fail(w, r, 400, "invalid_url", e.Error())
		return
	}
	if in.Kind == "webhook" && (len(secret.SigningKey) < 32 || len(secret.SigningKey) > 256) {
		s.fail(w, r, 400, "invalid_signing_key", "Use a signing secret of 32–256 characters.")
		return
	}
	if in.Kind != "webhook" {
		secret.SigningKey = ""
	}
	for _, service := range in.Rules.Services {
		var found bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE workspace_id=$1 AND id::text=$2)`, ws, service).Scan(&found)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		if !found {
			s.fail(w, r, 400, "invalid_service", "A selected service is unavailable in this workspace.")
			return
		}
	}
	plain, _ := json.Marshal(secret)
	raw, _ := json.Marshal(in.Rules)
	_, e = tx.Exec(ctx, `INSERT INTO notification_destinations(id,workspace_id,name,kind,enabled,revision,rules,secret) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,enabled=EXCLUDED.enabled,revision=EXCLUDED.revision,rules=EXCLUDED.rules,secret=EXCLUDED.secret,updated_at=now()`, id, ws, in.Name, in.Kind, in.Enabled, revision, raw, s.vault.Seal(plain, ws+":notification:"+id))
	if e == nil {
		_, e = tx.Exec(ctx, `UPDATE notification_deliveries SET state='cancelled',last_error='Destination configuration changed; queue a new test or await a new event.',finished_at=now(),body=NULL WHERE workspace_id=$1 AND destination_id=$2 AND state IN ('pending','retrying','failed')`, ws, id)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'notification.destination_saved')`, ws)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, Destination{id, in.Name, in.Kind, in.Enabled, revision, in.Rules, true})
}
func (s *Server) deleteDestination(w http.ResponseWriter, r *http.Request) {
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE notification_destinations SET enabled=false,deleted=true,secret=''::bytea,revision=revision+1 WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted`, ws, r.PathValue("id"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if tag.RowsAffected() == 0 {
		s.missing(w, r, pgx.ErrNoRows)
		return
	}
	_, e = tx.Exec(ctx, `UPDATE notification_deliveries SET state='cancelled',body=NULL,last_error='Destination deleted.',finished_at=now() WHERE workspace_id=$1 AND destination_id::text=$2 AND state IN ('pending','retrying','failed')`, ws, r.PathValue("id"))
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'notification.destination_deleted')`, ws)
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
func (s *Server) sampleNotification() notifications.Event {
	return notifications.Event{ID: "synthetic-preview", Type: "test", At: time.Now().UTC(), Origin: "demo", Severity: "notice", Summary: "[SIMULATED TEST] Sherlock notification delivery test. No incident is being reported.", URL: s.cfg.Origin + "/settings"}
}
func (s *Server) previewNotification(w http.ResponseWriter, r *http.Request) {
	d, e := scanDestination(s.pool.QueryRow(r.Context(), `SELECT `+destinationColumns+` FROM notification_destinations WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted`, workspace(r), r.PathValue("id")))
	if e != nil {
		s.missing(w, r, e)
		return
	}
	raw, e := notifications.Payload(d.Kind, "synthetic-preview", []notifications.Event{s.sampleNotification()})
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"payload": json.RawMessage(raw), "sends_message": false})
}
func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !in.Confirm {
		s.fail(w, r, 400, "confirm_test", "Confirm sending a synthetic test to this destination.")
		return
	}
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	d, e := scanDestination(tx.QueryRow(ctx, `SELECT `+destinationColumns+` FROM notification_destinations WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted FOR UPDATE`, ws, r.PathValue("id")))
	if e != nil {
		s.missing(w, r, e)
		return
	}
	if !d.Enabled {
		s.fail(w, r, 409, "destination_disabled", "Enable the destination before sending a test.")
		return
	}
	var recent int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE workspace_id=$1 AND destination_id=$2 AND test AND created_at>now()-interval '1 minute'`, ws, d.ID).Scan(&recent)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if recent >= 3 {
		s.fail(w, r, 429, "test_rate_limit", "Wait a minute before sending another test.")
		return
	}
	ev := s.sampleNotification()
	ev.ID = ""
	raw, _ := json.Marshal(ev)
	var eventID string
	e = tx.QueryRow(ctx, `INSERT INTO notification_events(workspace_id,event_key,kind,origin,severity,payload,expires_at) VALUES($1,$2,'test','demo','notice',$3,now()+interval '24 hours') RETURNING id::text`, ws, "test:"+auth.Token(), raw).Scan(&eventID)
	id := ""
	if e == nil {
		id, e = s.queueNotification(ctx, tx, ws, d, eventID, "test", true, time.Now(), time.Now().Add(24*time.Hour))
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'notification.test_requested')`, ws)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 202, map[string]string{"id": id})
}
func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT d.id::text,d.destination_id::text,n.name,d.state,d.test,d.attempts,d.last_error,d.message_id,d.created_at,d.due_at,d.expires_at,d.finished_at, (SELECT count(*) FROM notification_items i WHERE i.delivery_id=d.id AND NOT i.excluded), COALESCE((SELECT jsonb_agg(jsonb_build_object('number',a.number,'started_at',a.started_at,'finished_at',a.finished_at,'outcome',a.outcome,'status_code',a.status_code,'detail',a.detail) ORDER BY a.number) FROM notification_attempts a WHERE a.delivery_id=d.id),'[]'::jsonb) FROM notification_deliveries d JOIN notification_destinations n ON n.workspace_id=d.workspace_id AND n.id=d.destination_id WHERE d.workspace_id=$1 AND ($2='' OR (d.created_at,d.id)<(SELECT c.created_at,c.id FROM notification_deliveries c WHERE c.workspace_id=$1 AND c.id::text=$2)) ORDER BY d.created_at DESC,d.id DESC LIMIT 50`, workspace(r), r.URL.Query().Get("cursor"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, dest, name, state, detail, message string
		var test bool
		var attempts, count int
		var created, due, expiry time.Time
		var finished *time.Time
		var history []byte
		e = rows.Scan(&id, &dest, &name, &state, &test, &attempts, &detail, &message, &created, &due, &expiry, &finished, &count, &history)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, map[string]any{"id": id, "destination_id": dest, "destination_name": name, "state": state, "test": test, "attempts": attempts, "last_error": detail, "message_id": message, "created_at": created, "due_at": due, "expires_at": expiry, "finished_at": finished, "event_count": count, "history": json.RawMessage(history)})
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	next := ""
	if len(items) == 50 {
		next = items[49]["id"].(string)
	}
	s.json(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) retryNotification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !in.Confirm {
		s.fail(w, r, 400, "confirm_retry", "Retry may duplicate a message already accepted by the receiver.")
		return
	}
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	var id string
	e = tx.QueryRow(ctx, `UPDATE notification_deliveries d SET state='retrying',due_at=now(),finished_at=NULL WHERE workspace_id=$1 AND id::text=$2 AND state='failed' AND attempts<8 AND expires_at>now() AND EXISTS(SELECT 1 FROM notification_destinations n WHERE n.id=d.destination_id AND n.workspace_id=d.workspace_id AND n.enabled AND NOT n.deleted AND n.revision=d.revision) RETURNING id::text`, ws, r.PathValue("id")).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		s.fail(w, r, 409, "retry_unavailable", "Only unexpired failed deliveries with attempts remaining and an unchanged enabled destination can be retried.")
		return
	}
	if e == nil {
		_, e = s.jobs.InsertTx(ctx, tx, notificationArgs{WorkspaceID: ws, DeliveryID: id}, &river.InsertOpts{Queue: "notifications", MaxAttempts: 100})
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'notification.retry_requested')`, ws)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 202, map[string]string{"id": id})
}
