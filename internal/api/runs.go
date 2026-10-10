package api

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"sherlock/internal/detectors"
	"time"
)

func (s *Server) runRoutes(m *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"GET /api/v1/settings": s.getSettings, "GET /api/v1/system": s.systemHealth, "POST /api/v1/settings": s.saveSettings,
		"POST /api/v1/services/{id}/scans": s.startRun, "GET /api/v1/scans": s.listRuns, "GET /api/v1/scans/{id}": s.getRun,
		"GET /api/v1/findings": s.listFindings, "POST /api/v1/findings/{id}/state": s.findingState, "POST /api/v1/findings/{id}/feedback": s.findingFeedback,
		"GET /api/v1/overview": s.overview,
	} {
		m.Handle(path, s.requireSession(h))
	}
}
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	c, e := s.settings(r.Context(), workspace(r))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, c)
}
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var c detectors.Config
	if !s.decode(w, r, &c) {
		return
	}
	if !c.Valid() {
		s.fail(w, r, 400, "invalid_settings", "Check query windows, cadence, retention, and detector threshold bounds.")
		return
	}
	for _, d := range c.Disabled {
		if d != "new_exception" && d != "warning_burst" && d != "error_rate" {
			s.fail(w, r, 400, "invalid_detector", "Unknown detector.")
			return
		}
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize detector changes with recommendation publication, including the first save.
	if _, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,731903))`, workspace(r)); e != nil {
		s.internal(w, r, e)
		return
	}
	raw, _ := json.Marshal(c)
	_, e = tx.Exec(r.Context(), `INSERT INTO workspace_settings(workspace_id,settings) VALUES($1,$2) ON CONFLICT(workspace_id) DO UPDATE SET settings=EXCLUDED.settings`, workspace(r), raw)
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO audit_events(workspace_id,action) VALUES($1,'settings.changed')`, workspace(r))
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, c)
}
func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	ws := workspace(r)
	v, e := s.service(r.Context(), ws, r.PathValue("id"))
	if e != nil {
		s.missing(w, r, e)
		return
	}
	c, e := s.settings(r.Context(), ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	end := time.Now().UTC().Truncate(5 * time.Minute)
	start := end.Add(-time.Duration(c.CurrentMinutes) * time.Minute)
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	id, e := s.enqueue(r.Context(), tx, ws, v, c, "scan", false, start, end)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if errors.Is(e, errRunConflict) {
		s.fail(w, r, 409, "active_run_conflict", "The service mapping changed while starting the scan. Refresh and try again.")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 202, map[string]string{"id": id})
}

type Run struct {
	ID        string     `json:"id"`
	ServiceID string     `json:"service_id"`
	Kind      string     `json:"kind"`
	State     string     `json:"state"`
	Snapshot  Service    `json:"snapshot"`
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	Created   time.Time  `json:"created_at"`
	Finished  *time.Time `json:"finished_at"`
	Requests  int        `json:"requests"`
	Result    RunResult  `json:"result"`
}

const runColumns = `id::text,service_id::text,kind,state,snapshot,window_start,window_end,created_at,finished_at,requests,result`

func scanRun(row pgx.Row) (Run, error) {
	var v Run
	var snap, res []byte
	e := row.Scan(&v.ID, &v.ServiceID, &v.Kind, &v.State, &snap, &v.Start, &v.End, &v.Created, &v.Finished, &v.Requests, &res)
	if e == nil {
		e = json.Unmarshal(snap, &v.Snapshot)
	}
	if e == nil {
		e = json.Unmarshal(res, &v.Result)
	}
	return v, e
}
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT `+runColumns+` FROM runs WHERE workspace_id=$1 AND kind='scan' AND id::text>$2 ORDER BY id::text LIMIT 100`, workspace(r), r.URL.Query().Get("cursor"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []Run{}
	for rows.Next() {
		v, e := scanRun(rows)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, v)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	s.json(w, 200, listResponse(items, func(v Run) string { return v.ID }))
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	v, e := scanRun(s.pool.QueryRow(r.Context(), `SELECT `+runColumns+` FROM runs WHERE workspace_id=$1 AND id::text=$2`, workspace(r), r.PathValue("id")))
	if e != nil {
		s.missing(w, r, e)
		return
	}
	if investigationPath(r) && v.Kind != "investigation" {
		s.missing(w, r, pgx.ErrNoRows)
		return
	}
	s.json(w, 200, v)
}

type Finding struct {
	ID           string              `json:"id"`
	ServiceID    string              `json:"service_id"`
	Origin       string              `json:"origin"`
	State        string              `json:"state"`
	Severity     string              `json:"severity"`
	Title        string              `json:"title"`
	Description  string              `json:"description"`
	Details      detectors.Candidate `json:"details"`
	First        time.Time           `json:"first_seen"`
	Last         time.Time           `json:"last_seen"`
	SnoozedUntil *time.Time          `json:"snoozed_until"`
	Feedback     string              `json:"feedback"`
}

func (s *Server) listFindings(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT id::text,service_id::text,origin,state,severity,title,description,details,first_seen,last_seen,snoozed_until,feedback FROM findings WHERE workspace_id=$1 AND id::text>$2 AND ($3='' OR state=$3) AND ($4='' OR id::text=$4) ORDER BY id::text LIMIT 100`, workspace(r), r.URL.Query().Get("cursor"), r.URL.Query().Get("state"), r.URL.Query().Get("id"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []Finding{}
	for rows.Next() {
		var v Finding
		var raw []byte
		e = rows.Scan(&v.ID, &v.ServiceID, &v.Origin, &v.State, &v.Severity, &v.Title, &v.Description, &raw, &v.First, &v.Last, &v.SnoozedUntil, &v.Feedback)
		if e == nil {
			e = json.Unmarshal(raw, &v.Details)
		}
		if e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, v)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	s.json(w, 200, listResponse(items, func(v Finding) string { return v.ID }))
}
func allowedTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch to {
	case "acknowledged":
		return from == "new" || from == "snoozed"
	case "snoozed":
		return from == "new" || from == "acknowledged"
	case "resolved", "dismissed":
		return from == "new" || from == "acknowledged" || from == "snoozed"
	}
	return false
}
func (s *Server) findingState(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State       string `json:"state"`
		SnoozeHours int    `json:"snooze_hours"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.State == "snoozed" && (in.SnoozeHours < 1 || in.SnoozeHours > 168) {
		s.fail(w, r, 400, "invalid_snooze", "Choose 1–168 hours.")
		return
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	ws, id := workspace(r), r.PathValue("id")
	var old string
	e = tx.QueryRow(r.Context(), `SELECT state FROM findings WHERE workspace_id=$1 AND id::text=$2 FOR UPDATE`, ws, id).Scan(&old)
	if e != nil {
		s.missing(w, r, e)
		return
	}
	if !allowedTransition(old, in.State) {
		s.fail(w, r, 409, "invalid_transition", "This transition is not available from the current finding state.")
		return
	}
	if old == in.State {
		s.json(w, 200, map[string]bool{"ok": true})
		return
	}
	_, e = tx.Exec(r.Context(), `UPDATE findings SET state=$3,snoozed_until=CASE WHEN $3='snoozed' THEN now()+$4*interval '1 hour' ELSE NULL END,closed_at=CASE WHEN $3 IN ('resolved','dismissed') THEN now() ELSE NULL END WHERE workspace_id=$1 AND id::text=$2`, ws, id, in.State, in.SnoozeHours)
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO finding_events(workspace_id,finding_id,state) VALUES($1,$2,$3)`, ws, id, in.State)
	}
	if e == nil && in.State == "resolved" {
		e = s.notificationForFinding(r.Context(), tx, ws, id, "finding.resolved", nil)
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO audit_events(workspace_id,action) VALUES($1,$2)`, ws, "finding."+in.State)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]bool{"ok": true})
}
func (s *Server) findingFeedback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Feedback string `json:"feedback"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Feedback != "useful" && in.Feedback != "not_useful" {
		s.fail(w, r, 400, "invalid_feedback", "Choose useful or not_useful.")
		return
	}
	tag, e := s.pool.Exec(r.Context(), `UPDATE findings SET feedback=$3 WHERE workspace_id=$1 AND id::text=$2`, workspace(r), r.PathValue("id"), in.Feedback)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if tag.RowsAffected() == 0 {
		s.missing(w, r, pgx.ErrNoRows)
		return
	}
	s.json(w, 200, map[string]bool{"ok": true})
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	c, err := s.settings(r.Context(), workspace(r))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var services, monitored, findings, connected, sources int
	var last *time.Time
	e := s.pool.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM services WHERE workspace_id=$1),(SELECT count(*) FROM services WHERE workspace_id=$1 AND monitoring),(SELECT count(*) FROM findings WHERE workspace_id=$1 AND state IN ('new','acknowledged')),(SELECT count(*) FROM connections WHERE workspace_id=$1 AND NOT deleted AND status='connected'),(SELECT count(*) FROM connections WHERE workspace_id=$1 AND NOT deleted),(SELECT max(finished_at) FROM runs WHERE workspace_id=$1 AND kind='scan' AND state='completed')`, workspace(r)).Scan(&services, &monitored, &findings, &connected, &sources, &last)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"services": services, "monitored": monitored, "active_findings": findings, "connected": connected, "sources": sources, "last_successful_scan": last, "stale": last == nil || time.Since(*last) > time.Duration(c.CadenceMinutes+5)*time.Minute})
}
