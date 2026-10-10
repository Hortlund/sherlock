package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sherlock/internal/detectors"
	"sherlock/internal/maintenance"
	"sherlock/internal/notifications"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func destinationInput(url string, demo bool) map[string]any {
	origins := []string{"live"}
	if demo {
		origins = append(origins, "demo")
	}
	return map[string]any{"name": "Fixture alerts", "kind": "webhook", "enabled": true, "url": url, "signing_key": strings.Repeat("s", 32), "rules": notifications.Rules{Events: notifications.Events, Origins: origins, Severities: []string{"notice", "warning"}, Services: []string{}, Timezone: "UTC"}}
}
func createDestination(t *testing.T, s *Server, h http.Handler, c *http.Cookie, csrf, url string, demo bool) Destination {
	t.Helper()
	s.cfg.NotificationCIDRs = "127.0.0.0/8"
	s.cfg.NotificationHTTPHosts = "127.0.0.1"
	s.notifier, _ = notifications.NewSender(s.cfg.NotificationCIDRs, "", s.cfg.NotificationHTTPHosts)
	w := call(h, "POST", "/api/v1/notifications/destinations", destinationInput(url, demo), c, csrf, s.cfg.Origin)
	requireCode(t, w, 200)
	if strings.Contains(w.Body.String(), url) || strings.Contains(w.Body.String(), strings.Repeat("s", 32)) {
		t.Fatal("secret in response")
	}
	var d Destination
	if e := json.Unmarshal(w.Body.Bytes(), &d); e != nil {
		t.Fatal(e)
	}
	return d
}
func notificationIDs(t *testing.T, s *Server, table string) []string {
	t.Helper()
	rows, e := s.pool.Query(context.Background(), "SELECT id::text FROM "+table+" ORDER BY created_at,id")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			t.Fatal(e)
		}
		out = append(out, id)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return out
}
func notificationState(t *testing.T, s *Server, id string, want string) {
	t.Helper()
	var state string
	if e := s.pool.QueryRow(context.Background(), `SELECT state FROM notification_deliveries WHERE id=$1`, id).Scan(&state); e != nil {
		t.Fatal(e)
	}
	if state != want {
		t.Fatalf("state %s, want %s", state, want)
	}
}
func notificationSQL(t *testing.T, s *Server, q string, args ...any) {
	t.Helper()
	if _, e := s.pool.Exec(context.Background(), q, args...); e != nil {
		t.Fatal(e)
	}
}
func TestNotificationEventsGroupingRollbackAndLifecycle(t *testing.T) {
	s, h, c, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Sherlock-Signature") != notifications.Signature(strings.Repeat("s", 32), r.Header.Get("X-Sherlock-Timestamp"), b) {
			t.Error("signature")
		}
		if !bytes.Contains(b, []byte("SIMULATED")) {
			t.Error("unlabeled demo")
		}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	d := createDestination(t, s, h, c, csrf, receiver.URL, true)
	createDestination(t, s, h, c, csrf, receiver.URL, false) // Default live-only destination must ignore demo.
	scan := func() {
		w := call(h, "POST", "/api/v1/services/"+v.ID+"/scans", map[string]string{}, c, csrf, s.cfg.Origin)
		requireCode(t, w, 202)
		if e := s.execute(ctx, runArgs{ws, bodyMap(t, w)["id"].(string)}); e != nil {
			t.Fatal(e)
		}
	}
	scan()
	scan()
	events := notificationIDs(t, s, "notification_events")
	if len(events) != 2 {
		t.Fatalf("ordinary scan generated more alerts: %d", len(events))
	}
	for _, id := range events {
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if e := s.routeNotification(ctx, notificationArgs{WorkspaceID: ws, EventID: id}); e != nil {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
	}
	deliveries := notificationIDs(t, s, "notification_deliveries")
	if len(deliveries) != 1 {
		t.Fatalf("group/dedup/origin failed: %v", deliveries)
	}
	var dest string
	var due time.Time
	if e := s.pool.QueryRow(ctx, `SELECT destination_id::text,due_at FROM notification_deliveries WHERE id=$1`, deliveries[0]).Scan(&dest, &due); e != nil {
		t.Fatal(e)
	}
	if dest != d.ID || time.Until(due) < 50*time.Second {
		t.Fatal("group window")
	}
	// Suppress one finding after grouping without losing the other or its dedup tombstone.
	var finding string
	if e := s.pool.QueryRow(ctx, `SELECT id::text FROM findings LIMIT 1`).Scan(&finding); e != nil {
		t.Fatal(e)
	}
	requireCode(t, call(h, "POST", "/api/v1/findings/"+finding+"/state", map[string]any{"state": "snoozed", "snooze_hours": 1}, c, csrf, s.cfg.Origin), 200)
	notificationSQL(t, s, `UPDATE notification_deliveries SET due_at=now()`)
	if e := s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: deliveries[0]}); e != nil {
		t.Fatal(e)
	}
	notificationState(t, s, deliveries[0], "sent")
	if calls.Load() != 1 {
		t.Fatal("unexpected sends")
	}
	var count int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_items WHERE excluded`).Scan(&count)
	if count != 1 {
		t.Fatal("snoozed event not excluded")
	}
	for _, id := range events {
		if e := s.routeNotification(ctx, notificationArgs{WorkspaceID: ws, EventID: id}); e != nil {
			t.Fatal(e)
		}
	}
	if len(notificationIDs(t, s, "notification_deliveries")) != 1 {
		t.Fatal("excluded event replayed")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.notificationEvent(ctx, tx, ws, "rolled-back", "investigation.completed", v.ID, "", "", "demo", "notice", nil); e != nil {
		t.Fatal(e)
	}
	tx.Rollback(ctx)
	if len(notificationIDs(t, s, "notification_events")) != 2 {
		t.Fatal("rolled-back event retained")
	}
	s.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'event_id' NOT IN (SELECT id::text FROM notification_events) AND kind='notification'`).Scan(&count)
	if count != 0 {
		t.Fatal("orphan outbox job")
	}
	// Resolution inherits evidence expiry and says manual, not recovery.
	requireCode(t, call(h, "POST", "/api/v1/findings/"+finding+"/state", map[string]string{"state": "resolved"}, c, csrf, s.cfg.Origin), 200)
	var summary string
	var expiry, sourceExpiry time.Time
	if e = s.pool.QueryRow(ctx, `SELECT payload->>'summary',expires_at FROM notification_events WHERE kind='finding.resolved'`).Scan(&summary, &expiry); e != nil {
		t.Fatal(e)
	}
	s.pool.QueryRow(ctx, `SELECT min(e.expires_at) FROM evidence e JOIN findings f ON e.id::text IN (SELECT jsonb_array_elements_text(f.details->'evidence_ids')) WHERE f.id=$1`, finding).Scan(&sourceExpiry)
	if expiry.After(sourceExpiry) || !strings.Contains(summary, "recovery not verified") {
		t.Fatal("resolution provenance")
	}
	history := call(h, "GET", "/api/v1/notifications/deliveries", nil, c, "", "")
	requireCode(t, history, 200)
	if strings.Contains(history.Body.String(), receiver.URL) || strings.Contains(history.Body.String(), strings.Repeat("s", 32)) {
		t.Fatal("history leaks secret")
	}
}
func TestNotificationRetriesRevocationAndExpiry(t *testing.T) {
	s, h, c, csrf, ws, _ := reviewFixture(t, 0)
	ctx := context.Background()
	var code atomic.Int32
	code.Store(429)
	var calls atomic.Int32
	var bodies [][]byte
	var mu sync.Mutex
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(int(code.Load()))
	}))
	defer receiver.Close()
	d := createDestination(t, s, h, c, csrf, receiver.URL, true)
	queue := func() string {
		w := call(h, "POST", "/api/v1/notifications/destinations/"+d.ID+"/test", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin)
		requireCode(t, w, 202)
		return bodyMap(t, w)["id"].(string)
	}
	requireCode(t, call(h, "POST", "/api/v1/notifications/destinations/"+d.ID+"/test", map[string]bool{}, c, csrf, s.cfg.Origin), 400)
	requireCode(t, call(h, "POST", "/api/v1/notifications/destinations/"+d.ID+"/preview", map[string]bool{}, c, csrf, s.cfg.Origin), 200)
	if calls.Load() != 0 {
		t.Fatal("preview sent")
	}
	id := queue()
	_ = s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id})
	notificationState(t, s, id, "retrying")
	var due time.Time
	s.pool.QueryRow(ctx, `SELECT due_at FROM notification_deliveries WHERE id=$1`, id).Scan(&due)
	if time.Until(due) < 115*time.Second {
		t.Fatal("Retry-After ignored")
	}
	_ = s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id})
	if calls.Load() != 1 {
		t.Fatal("early retry")
	}
	ready := func() {
		notificationSQL(t, s, `UPDATE notification_deliveries SET due_at=now()`)
		notificationSQL(t, s, `UPDATE notification_destinations SET next_send_at=now()`)
	}
	ready()
	code.Store(401)
	if e := s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id}); e != nil {
		t.Fatal(e)
	}
	notificationState(t, s, id, "failed")
	requireCode(t, call(h, "POST", "/api/v1/notifications/deliveries/"+id+"/retry", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin), 202)
	ready()
	code.Store(204)
	if e := s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id}); e != nil {
		t.Fatal(e)
	}
	notificationState(t, s, id, "sent")
	mu.Lock()
	if len(bodies) != 3 || !bytes.Equal(bodies[0], bodies[2]) {
		t.Fatal("retry changed exact body")
	}
	mu.Unlock()
	expired := queue()
	notificationSQL(t, s, `UPDATE notification_deliveries SET expires_at=now()-interval '1 second',body='content' WHERE id=$1`, expired)
	if e := s.Retain(ctx); e != nil {
		t.Fatal(e)
	}
	notificationState(t, s, expired, "cancelled")
	if e := s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: expired}); e != nil {
		t.Fatal(e)
	}
	cancelled := queue()
	input := destinationInput("", true)
	input["enabled"] = false
	input["revision"] = d.Revision
	input["signing_key"] = ""
	requireCode(t, call(h, "POST", "/api/v1/notifications/destinations/"+d.ID, input, c, csrf, s.cfg.Origin), 200)
	notificationState(t, s, cancelled, "cancelled")
	requireCode(t, call(h, "POST", "/api/v1/notifications/destinations/"+d.ID, input, c, csrf, s.cfg.Origin), 409)
	requireCode(t, call(h, "POST", "/api/v1/notifications/destinations/"+d.ID+"/test", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin), 409)
	requireCode(t, call(h, "POST", "/api/v1/notifications/destinations/"+d.ID+"/delete", map[string]bool{}, c, csrf, s.cfg.Origin), 200)
	var secret []byte
	s.pool.QueryRow(ctx, `SELECT secret FROM notification_destinations WHERE id=$1`, d.ID).Scan(&secret)
	if len(secret) != 0 || calls.Load() != 3 {
		t.Fatal("deletion/expiry failed")
	}
}
func TestNotificationRestartRotationAndDispatchGates(t *testing.T) {
	s, h, c, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer receiver.Close()
	d := createDestination(t, s, h, c, csrf, receiver.URL, true)
	// Full secret lifecycle: transactionally re-encrypt, reject old key, restart and drain committed job.
	if e := maintenance.Rotate(ctx, s.pool, s.cfg.Key, bytes.Repeat([]byte{8}, 32)); e != nil {
		t.Fatal(e)
	}
	old := s.cfg.Key
	s.cfg.Key = bytes.Repeat([]byte{8}, 32)
	restarted, e := New(ctx, s.pool, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	s.cfg.Key = old
	var enc []byte
	s.pool.QueryRow(ctx, `SELECT secret FROM notification_destinations WHERE id=$1`, d.ID).Scan(&enc)
	if bytes.Contains(enc, []byte(receiver.URL)) {
		t.Fatal("URL plaintext")
	}
	if _, e = s.vault.Open(enc, ws+":notification:"+d.ID); e == nil {
		t.Fatal("old key decrypts")
	}
	plain, e := restarted.vault.Open(enc, ws+":notification:"+d.ID)
	if e != nil || !bytes.Contains(plain, []byte(receiver.URL)) || !bytes.Contains(plain, []byte(strings.Repeat("s", 32))) {
		t.Fatal("rotation lost destination secrets")
	}
	w := call(restarted.Handler(), "POST", "/api/v1/notifications/destinations/"+d.ID+"/test", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin)
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	if e = restarted.StartJobs(ctx); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(10 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if e = restarted.StopJobs(ctx); e != nil {
		t.Fatal(e)
	}
	notificationState(t, s, id, "sent")
	// Old events must not be backfilled after a configuration edit.
	tx, _ := s.pool.Begin(ctx)
	if e = restarted.notificationEvent(ctx, tx, ws, "before-edit", "investigation.completed", v.ID, "", "", "demo", "notice", nil); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	notificationSQL(t, s, `UPDATE notification_destinations SET updated_at=clock_timestamp()`)
	var event string
	s.pool.QueryRow(ctx, `SELECT id::text FROM notification_events WHERE event_key='before-edit'`).Scan(&event)
	if e = restarted.routeNotification(ctx, notificationArgs{WorkspaceID: ws, EventID: event}); e != nil {
		t.Fatal(e)
	}
	if len(notificationIDs(t, s, "notification_deliveries")) != 1 {
		t.Fatal("backfilled old event")
	}
}

func TestNotificationQuietHoursUnknownOutcomeAndAttemptBound(t *testing.T) {
	s, h, c, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	var calls atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, e := w.(http.Hijacker).Hijack()
		if e == nil {
			conn.Close()
		}
	}))
	defer receiver.Close()
	d := createDestination(t, s, h, c, csrf, receiver.URL, true)
	now := time.Now().UTC()
	d.Rules.Immediate = true
	d.Rules.QuietStart = now.Add(-time.Hour).Format("15:04")
	d.Rules.QuietEnd = now.Add(time.Hour).Format("15:04")
	raw, _ := json.Marshal(d.Rules)
	notificationSQL(t, s, `UPDATE notification_destinations SET rules=$2 WHERE id=$1`, d.ID, raw)
	tx, _ := s.pool.Begin(ctx)
	if e := s.notificationEvent(ctx, tx, ws, "quiet", "investigation.completed", v.ID, "", "", "demo", "notice", nil); e != nil {
		t.Fatal(e)
	}
	if e := tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	event := notificationIDs(t, s, "notification_events")[0]
	if e := s.routeNotification(ctx, notificationArgs{WorkspaceID: ws, EventID: event}); e != nil {
		t.Fatal(e)
	}
	id := notificationIDs(t, s, "notification_deliveries")[0]
	_ = s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id})
	if calls.Load() != 0 {
		t.Fatal("quiet hours ignored")
	}
	notificationState(t, s, id, "pending")
	d.Rules.QuietStart = ""
	d.Rules.QuietEnd = ""
	raw, _ = json.Marshal(d.Rules)
	notificationSQL(t, s, `UPDATE notification_destinations SET rules=$2 WHERE id=$1`, d.ID, raw)
	for i := 1; i <= 8; i++ {
		notificationSQL(t, s, `UPDATE notification_deliveries SET due_at=now() WHERE id=$1`, id)
		notificationSQL(t, s, `UPDATE notification_destinations SET next_send_at=now()`)
		_ = s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id})
		var outcome string
		if e := s.pool.QueryRow(ctx, `SELECT outcome FROM notification_attempts WHERE delivery_id=$1 AND number=$2`, id, i).Scan(&outcome); e != nil || outcome != "unknown" {
			t.Fatalf("lost acknowledgement not recorded: %s %v", outcome, e)
		}
	}
	notificationState(t, s, id, "failed")
	_ = s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id})
	if calls.Load() != 8 {
		t.Fatalf("attempt cap: %d", calls.Load())
	}
	requireCode(t, call(h, "POST", "/api/v1/notifications/deliveries/"+id+"/retry", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin), 409)
	// Expire both source copy and frozen delivery while retaining operational history.
	notificationSQL(t, s, `UPDATE notification_events SET expires_at=now()-interval '1 second'`)
	notificationSQL(t, s, `UPDATE notification_deliveries SET expires_at=now()-interval '1 second'`)
	if e := s.Retain(ctx); e != nil {
		t.Fatal(e)
	}
	var empty bool
	s.pool.QueryRow(ctx, `SELECT body IS NULL FROM notification_deliveries WHERE id=$1`, id).Scan(&empty)
	if !empty {
		t.Fatal("expired payload retained")
	}
	var payload string
	s.pool.QueryRow(ctx, `SELECT payload::text FROM notification_events WHERE id=$1`, event).Scan(&payload)
	if payload != "{}" {
		t.Fatal("expired event retained")
	}
	notificationSQL(t, s, `UPDATE notification_deliveries SET created_at=now()-interval '31 days'`)
	notificationSQL(t, s, `UPDATE notification_events SET created_at=now()-interval '31 days'`)
	if e := s.Retain(ctx); e != nil {
		t.Fatal(e)
	}
	if len(notificationIDs(t, s, "notification_deliveries")) != 0 || len(notificationIDs(t, s, "notification_events")) != 0 {
		t.Fatal("30-day cleanup")
	}
}

func TestNotificationEscalationAndInvestigationEvents(t *testing.T) {
	s, h, c, csrf, ws, v := reviewFixture(t, 0)
	ctx := context.Background()
	run := func(path string) string {
		w := call(h, "POST", path, map[string]any{}, c, csrf, s.cfg.Origin)
		requireCode(t, w, 202)
		id := bodyMap(t, w)["id"].(string)
		if e := s.execute(ctx, runArgs{ws, id}); e != nil {
			t.Fatal(e)
		}
		return id
	}
	run("/api/v1/services/" + v.ID + "/scans")
	var finding string
	var raw []byte
	if e := s.pool.QueryRow(ctx, `SELECT id::text,details FROM findings WHERE severity='warning' LIMIT 1`).Scan(&finding, &raw); e != nil {
		t.Fatal(e)
	}
	// Feed the existing detector candidate at notice then warning to exercise the event boundary.
	var candidate detectors.Candidate
	if e := json.Unmarshal(raw, &candidate); e != nil {
		t.Fatal(e)
	}
	candidate.Severity = "notice"
	end := time.Now().Add(time.Minute)
	if e := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, end, detectors.Defaults(), []detectors.Candidate{candidate}); e != nil {
		t.Fatal(e)
	}
	requireCode(t, call(h, "POST", "/api/v1/findings/"+finding+"/state", map[string]string{"state": "acknowledged"}, c, csrf, s.cfg.Origin), 200)
	candidate.Severity = "warning"
	for range 2 {
		if e := s.persistFindings(ctx, runArgs{WorkspaceID: ws}, v, false, end.Add(time.Minute), detectors.Defaults(), []detectors.Candidate{candidate}); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events WHERE kind='finding.escalated' AND finding_id=$1`, finding).Scan(&count); e != nil || count != 1 {
		t.Fatalf("escalation dedup %d %v", count, e)
	}
	id := run("/api/v1/services/" + v.ID + "/investigations")
	if e := s.execute(ctx, runArgs{ws, id}); e != nil {
		t.Fatal(e)
	}
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events WHERE kind='investigation.completed' AND run_id::text=$1 AND payload->>'run_id'=$1`, id).Scan(&count); e != nil || count != 1 {
		t.Fatalf("report event dedup %d %v", count, e)
	}
}

func TestNotificationConcurrentDispatchAndEdit(t *testing.T) {
	s, h, c, csrf, ws, _ := reviewFixture(t, 0)
	ctx := context.Background()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release; w.WriteHeader(204) }))
	defer receiver.Close()
	// Always release the fixture before closing it if a test assertion fails.
	var once sync.Once
	defer once.Do(func() { close(release) })
	d := createDestination(t, s, h, c, csrf, receiver.URL, true)
	w := call(h, "POST", "/api/v1/notifications/destinations/"+d.ID+"/test", map[string]bool{"confirm": true}, c, csrf, s.cfg.Origin)
	requireCode(t, w, 202)
	id := bodyMap(t, w)["id"].(string)
	done := make(chan error, 1)
	go func() { done <- s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id}) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no dispatch")
	}
	// Replaying while the original worker is on the network must not send another request.
	if e := s.deliverNotification(ctx, notificationArgs{WorkspaceID: ws, DeliveryID: id}); e == nil {
		t.Fatal("concurrent send was not deferred")
	}
	edit := destinationInput("", true)
	edit["enabled"] = false
	edit["revision"] = d.Revision
	edit["signing_key"] = ""
	edited := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		edited <- call(h, "POST", "/api/v1/notifications/destinations/"+d.ID, edit, c, csrf, s.cfg.Origin)
	}()
	select {
	case response := <-edited:
		requireCode(t, response, 200)
	case <-time.After(3 * time.Second):
		t.Fatal("configuration edit blocked on network")
	}
	once.Do(func() { close(release) })
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	notificationState(t, s, id, "sent") // It was already dispatched before disable; report the real acknowledgement.
}
