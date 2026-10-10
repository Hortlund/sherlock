package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"math/rand/v2"
	"sherlock/internal/notifications"
	"time"
)

type notificationArgs struct {
	WorkspaceID string `json:"workspace_id"`
	EventID     string `json:"event_id,omitempty"`
	DeliveryID  string `json:"delivery_id,omitempty"`
}

func (notificationArgs) Kind() string { return "notification" }

type notificationWorker struct {
	river.WorkerDefaults[notificationArgs]
	server *Server
}

func (w *notificationWorker) Work(ctx context.Context, j *river.Job[notificationArgs]) error {
	if j.Args.EventID != "" {
		return w.server.routeNotification(ctx, j.Args)
	}
	return w.server.deliverNotification(ctx, j.Args)
}

// Called inside the transaction publishing a domain change. No destination or network locks here.
func (s *Server) notificationEvent(ctx context.Context, tx pgx.Tx, ws, key, kind, service, finding, run, origin, severity string, evidenceIDs []string) error {
	var retention int
	e := tx.QueryRow(ctx, `SELECT COALESCE((SELECT (settings->>'retention_days')::int FROM workspace_settings WHERE workspace_id=$1),7)`, ws).Scan(&retention)
	if e != nil {
		return e
	}
	expires := time.Now().Add(time.Duration(retention) * 24 * time.Hour)
	if len(evidenceIDs) > 0 {
		var earliest *time.Time
		e = tx.QueryRow(ctx, `SELECT min(expires_at) FROM evidence WHERE workspace_id=$1 AND id::text=ANY($2)`, ws, evidenceIDs).Scan(&earliest)
		if e != nil {
			return e
		}
		if earliest != nil && earliest.Before(expires) {
			expires = *earliest
		}
	}
	var name string
	if e = tx.QueryRow(ctx, `SELECT name FROM services WHERE workspace_id=$1 AND id=$2`, ws, service).Scan(&name); e != nil {
		return e
	}
	summary := kind + " · " + severity + " · " + notifications.Display(name)
	if finding != "" {
		var title string
		if e = tx.QueryRow(ctx, `SELECT title FROM findings WHERE workspace_id=$1 AND id=$2`, ws, finding).Scan(&title); e != nil {
			return e
		}
		summary += " · " + notifications.Display(title)
	}
	if kind == "finding.resolved" {
		summary += " (manually resolved; recovery not verified)"
	}
	if origin == "demo" {
		summary = "[SIMULATED] " + summary
	}
	link := s.cfg.Origin + "/findings"
	if run != "" {
		link = s.cfg.Origin + "/investigations?id=" + run
	}
	ev := notifications.Event{Type: kind, At: time.Now().UTC(), ServiceID: service, FindingID: finding, RunID: run, Origin: origin, Severity: severity, Summary: summary, URL: link}
	raw, _ := json.Marshal(ev)
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO notification_events(workspace_id,event_key,kind,service_id,finding_id,run_id,origin,severity,payload,expires_at) VALUES($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8,$9,$10) ON CONFLICT(workspace_id,event_key) DO NOTHING RETURNING id::text`, ws, key, kind, service, finding, run, origin, severity, raw, expires).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	_, e = s.jobs.InsertTx(ctx, tx, notificationArgs{WorkspaceID: ws, EventID: id}, &river.InsertOpts{Queue: "notifications", MaxAttempts: 100})
	return e
}
func (s *Server) routeNotification(ctx context.Context, a notificationArgs) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var kind, service, origin, severity string
	var expires, created time.Time
	e = tx.QueryRow(ctx, `SELECT kind,COALESCE(service_id::text,''),origin,severity,expires_at,created_at FROM notification_events WHERE workspace_id=$1 AND id::text=$2 AND expires_at>now()`, a.WorkspaceID, a.EventID).Scan(&kind, &service, &origin, &severity, &expires, &created)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT `+destinationColumns+` FROM notification_destinations WHERE workspace_id=$1 AND enabled AND NOT deleted AND updated_at<=$2 ORDER BY id FOR UPDATE`, a.WorkspaceID, created)
	if e != nil {
		return e
	}
	ds := []Destination{}
	for rows.Next() {
		d, e := scanDestination(rows)
		if e != nil {
			rows.Close()
			return e
		}
		ds = append(ds, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, d := range ds {
		if !d.Rules.Matches(kind, service, origin, severity) {
			continue
		}
		due := time.Now()
		if !d.Rules.Immediate {
			due = due.Add(time.Minute)
		}
		if _, e = s.queueNotification(ctx, tx, a.WorkspaceID, d, a.EventID, service, false, due, minTime(expires, created.Add(24*time.Hour))); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (s *Server) queueNotification(ctx context.Context, tx pgx.Tx, ws string, d Destination, event, service string, test bool, due, expires time.Time) (string, error) {
	var id string
	e := tx.QueryRow(ctx, `SELECT delivery_id::text FROM notification_items WHERE workspace_id=$1 AND destination_id=$2 AND event_id=$3`, ws, d.ID, event).Scan(&id)
	if e == nil {
		return id, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	if !test && !d.Rules.Immediate {
		e = tx.QueryRow(ctx, `SELECT id::text FROM notification_deliveries d WHERE workspace_id=$1 AND destination_id=$2 AND revision=$3 AND service_key=$4 AND state='pending' AND body IS NULL AND NOT test AND due_at>now() AND created_at>now()-interval '60 seconds' AND (SELECT count(*) FROM notification_items i WHERE i.delivery_id=d.id)<10 ORDER BY due_at LIMIT 1 FOR UPDATE`, ws, d.ID, d.Revision, service).Scan(&id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return "", e
		}
	}
	if id == "" {
		e = tx.QueryRow(ctx, `INSERT INTO notification_deliveries(workspace_id,destination_id,revision,service_key,test,due_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, ws, d.ID, d.Revision, service, test, due, expires).Scan(&id)
		if e != nil {
			return "", e
		}
		_, e = s.jobs.InsertTx(ctx, tx, notificationArgs{WorkspaceID: ws, DeliveryID: id}, &river.InsertOpts{Queue: "notifications", MaxAttempts: 100, ScheduledAt: due})
		if e != nil {
			return "", e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO notification_items(workspace_id,delivery_id,destination_id,event_id) VALUES($1,$2,$3,$4)`, ws, id, d.ID, event)
	if e == nil {
		_, e = tx.Exec(ctx, `UPDATE notification_deliveries SET expires_at=LEAST(expires_at,$3) WHERE workspace_id=$1 AND id=$2`, ws, id, expires)
	}
	return id, e
}

func (s *Server) deliverNotification(ctx context.Context, a notificationArgs) error {
	// A session advisory lock serializes a destination across replicas, without holding
	// its configuration row or any finding transaction while waiting on the network.
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	var destID string
	e = conn.QueryRow(ctx, `SELECT destination_id::text FROM notification_deliveries WHERE workspace_id=$1 AND id::text=$2`, a.WorkspaceID, a.DeliveryID).Scan(&destID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var locked bool
	e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,731901))`, destID).Scan(&locked)
	if e != nil {
		return e
	}
	if !locked {
		return river.JobSnooze(time.Second)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, e := conn.Exec(c, `SELECT pg_advisory_unlock(hashtextextended($1,731901))`, destID); e != nil {
			_ = conn.Conn().Close(c)
		}
	}()
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Configuration mutations and grouping take destination then delivery locks too.
	// Keep this order so editing during dispatch cannot deadlock with the worker.
	if _, e = tx.Exec(ctx, `SELECT id FROM notification_destinations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, destID); e != nil {
		return e
	}
	var state string
	var revision, attempts int
	var test bool
	var body []byte
	var due, expires time.Time
	e = tx.QueryRow(ctx, `SELECT state,revision,attempts,test,body,due_at,expires_at FROM notification_deliveries WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, a.DeliveryID).Scan(&state, &revision, &attempts, &test, &body, &due, &expires)
	if e != nil {
		return e
	}
	if state == "sent" || state == "cancelled" || state == "failed" {
		return nil
	}
	cancelDelivery := func(reason string) error {
		_, err := tx.Exec(ctx, `UPDATE notification_deliveries SET state='cancelled',last_error=$3,body=NULL,finished_at=now() WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.DeliveryID, reason)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return err
	}
	if !expires.After(time.Now()) {
		return cancelDelivery("Delivery content or retry window expired.")
	}
	d, e := scanDestination(tx.QueryRow(ctx, `SELECT `+destinationColumns+` FROM notification_destinations WHERE workspace_id=$1 AND id=$2 AND NOT deleted`, a.WorkspaceID, destID))
	if errors.Is(e, pgx.ErrNoRows) {
		return cancelDelivery("Destination deleted.")
	}
	if e != nil {
		return e
	}
	if !d.Enabled || d.Revision != revision {
		return cancelDelivery("Destination disabled or configuration changed.")
	}
	if attempts >= 8 {
		_, e = tx.Exec(ctx, `UPDATE notification_deliveries SET state='failed',last_error='Eight delivery attempts exhausted.',finished_at=now() WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.DeliveryID)
		if e == nil {
			e = tx.Commit(ctx)
		}
		return e
	}
	var next time.Time
	var encrypted []byte
	e = tx.QueryRow(ctx, `SELECT next_send_at,secret FROM notification_destinations WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, destID).Scan(&next, &encrypted)
	if e != nil {
		return e
	}
	now := time.Now()
	if next.After(due) {
		due = next
	}
	if !test {
		if allowed := d.Rules.NextAllowed(now); allowed.After(due) {
			due = allowed
		}
	}
	if due.After(now) {
		if !due.Before(expires) {
			return cancelDelivery("Quiet hours or retry delay exceeds delivery expiry.")
		}
		_, e = tx.Exec(ctx, `UPDATE notification_deliveries SET due_at=$3 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.DeliveryID, due)
		if e == nil {
			e = tx.Commit(ctx)
		}
		if e != nil {
			return e
		}
		return river.JobSnooze(time.Until(due))
	}
	rows, e := tx.Query(ctx, `SELECT e.id::text,e.kind,COALESCE(e.service_id::text,''),e.origin,e.severity,e.payload,e.expires_at,COALESCE(f.state,''),i.excluded FROM notification_items i JOIN notification_events e ON e.workspace_id=i.workspace_id AND e.id=i.event_id LEFT JOIN findings f ON f.workspace_id=e.workspace_id AND f.id=e.finding_id WHERE i.workspace_id=$1 AND i.delivery_id=$2 ORDER BY e.id`, a.WorkspaceID, a.DeliveryID)
	if e != nil {
		return e
	}
	events := []notifications.Event{}
	excluded := []string{}
	invalidFrozen := false
	for rows.Next() {
		var id, kind, service, origin, severity, fs string
		var raw []byte
		var expiry time.Time
		var skip bool
		if e = rows.Scan(&id, &kind, &service, &origin, &severity, &raw, &expiry, &fs, &skip); e != nil {
			break
		}
		if skip {
			continue
		}
		valid := expiry.After(now) && (test || d.Rules.Matches(kind, service, origin, severity))
		if kind == "finding.created" {
			valid = valid && fs == "new"
		}
		if kind == "finding.escalated" {
			valid = valid && (fs == "new" || fs == "acknowledged")
		}
		if kind == "finding.resolved" {
			valid = valid && fs == "resolved"
		}
		if !valid {
			excluded = append(excluded, id)
			invalidFrozen = invalidFrozen || body != nil
			continue
		}
		var ev notifications.Event
		if e = json.Unmarshal(raw, &ev); e != nil {
			break
		}
		ev.ID = id
		events = append(events, ev)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	if invalidFrozen || len(events) == 0 {
		return cancelDelivery("Delivery no longer matches lifecycle, privacy filters, or retention.")
	}
	if len(excluded) > 0 {
		_, e = tx.Exec(ctx, `UPDATE notification_items SET excluded=true WHERE workspace_id=$1 AND delivery_id=$2 AND event_id::text=ANY($3)`, a.WorkspaceID, a.DeliveryID, excluded)
		if e != nil {
			return e
		}
	}
	if body == nil {
		body, e = notifications.Payload(d.Kind, a.DeliveryID, events)
		if e != nil {
			return e
		}
	}
	plain, e := s.vault.Open(encrypted, a.WorkspaceID+":notification:"+destID)
	if e != nil {
		return e
	}
	var secret notifications.Secret
	if e = json.Unmarshal(plain, &secret); e != nil {
		return e
	}
	attempts++
	_, e = tx.Exec(ctx, `UPDATE notification_deliveries SET state='sending',body=$3,attempts=$4,due_at=now()+interval '30 seconds' WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.DeliveryID, body, attempts)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO notification_attempts(workspace_id,delivery_id,number) VALUES($1,$2,$3)`, a.WorkspaceID, a.DeliveryID, attempts)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `UPDATE notification_destinations SET next_send_at=now()+interval '1 second' WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, destID)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return e
	}
	// Recheck destination after persisting the attempt intent and immediately before dispatch.
	var enabled bool
	e = conn.QueryRow(ctx, `SELECT enabled AND NOT deleted AND revision=$3 FROM notification_destinations WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, destID, revision).Scan(&enabled)
	if e != nil {
		return e
	}
	result := notifications.Result{Detail: "Destination changed before dispatch."}
	if enabled {
		result = s.notifier.Send(ctx, d.Kind, secret, a.DeliveryID, body)
	}
	final, outcome := "failed", "rejected"
	nextAttempt := time.Now()
	if !enabled {
		final = "cancelled"
		outcome = "cancelled"
	} else if result.Status >= 200 && result.Status < 300 && !result.Unknown && !result.Retry {
		final = "sent"
		outcome = "acknowledged"
	} else if result.Retry {
		outcome = "retryable"
		if result.Unknown {
			outcome = "unknown"
		}
		delay := time.Duration(5*(1<<min(attempts-1, 7)))*time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
		if result.After > delay {
			delay = result.After
		}
		nextAttempt = time.Now().Add(delay)
		if attempts < 8 && nextAttempt.Before(expires) {
			final = "retrying"
		}
	}
	finishCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	tx2, e := conn.Begin(finishCtx)
	if e != nil {
		return e
	}
	defer tx2.Rollback(finishCtx)
	if _, e = tx2.Exec(finishCtx, `SELECT id FROM notification_destinations WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, destID); e != nil {
		return e
	}
	_, e = tx2.Exec(finishCtx, `UPDATE notification_attempts SET finished_at=now(),outcome=$4,status_code=$5,detail=$6 WHERE workspace_id=$1 AND delivery_id=$2 AND number=$3`, a.WorkspaceID, a.DeliveryID, attempts, outcome, result.Status, result.Detail)
	if e == nil {
		_, e = tx2.Exec(finishCtx, `UPDATE notification_deliveries SET state=$3,last_error=$4,message_id=$5,due_at=$6,finished_at=CASE WHEN $3='retrying' THEN NULL ELSE now() END WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.DeliveryID, final, result.Detail, result.MessageID, nextAttempt)
	}
	if result.Status == 429 && e == nil {
		_, e = tx2.Exec(finishCtx, `UPDATE notification_destinations SET next_send_at=GREATEST(next_send_at,$3) WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, destID, nextAttempt)
	}
	if e == nil {
		e = tx2.Commit(finishCtx)
	}
	if e != nil {
		return e
	}
	if final == "retrying" {
		return river.JobSnooze(max(time.Until(nextAttempt), time.Second))
	}
	return nil
}

func (s *Server) notificationForFinding(ctx context.Context, tx pgx.Tx, ws, id, kind string, evidence []string) error {
	var service, origin, severity string
	var details struct {
		EvidenceIDs []string `json:"evidence_ids"`
	}
	var raw []byte
	e := tx.QueryRow(ctx, `SELECT service_id::text,origin,severity,details FROM findings WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&service, &origin, &severity, &raw)
	if e != nil {
		return e
	}
	if evidence == nil {
		if e = json.Unmarshal(raw, &details); e != nil {
			return e
		}
		evidence = details.EvidenceIDs
	}
	return s.notificationEvent(ctx, tx, ws, fmt.Sprintf("%s:%s", kind, id), kind, service, id, "", origin, severity, evidence)
}
