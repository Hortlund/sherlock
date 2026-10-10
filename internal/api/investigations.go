package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"sherlock/internal/investigations"
	"strconv"
	"strings"
	"time"
)

func (s *Server) investigationRoutes(m *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"POST /api/v1/services/{id}/investigations": s.startInvestigation, "GET /api/v1/investigations": s.listInvestigations, "GET /api/v1/investigations/{id}": s.getRun, "POST /api/v1/investigations/{id}/cancel": s.cancelInvestigation, "GET /api/v1/investigations/{id}/events": s.runEvents,
	} {
		m.Handle(path, s.requireSession(h))
	}
}
func (s *Server) startInvestigation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Start     *time.Time `json:"start"`
		End       *time.Time `json:"end"`
		FindingID string     `json:"finding_id"`
		Minutes   int        `json:"minutes"`
	}
	if !s.decode(w, r, &in) {
		return
	}
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
	if in.End != nil {
		end = in.End.UTC()
	}
	if in.FindingID != "" {
		var revision int
		e = s.pool.QueryRow(r.Context(), `SELECT mapping_revision,last_seen FROM findings WHERE workspace_id=$1 AND id::text=$2 AND service_id=$3 AND origin=$4`, ws, in.FindingID, v.ID, v.Origin).Scan(&revision, &end)
		if e != nil {
			s.missing(w, r, e)
			return
		}
		if revision != v.Revision {
			s.fail(w, r, 409, "mapping_changed", "This finding uses an older mapping. Start a service investigation to use the current mapping.")
			return
		}
	}
	minutes := in.Minutes
	if minutes == 0 {
		minutes = min(30, 120-c.BaselineMinutes)
	}
	start := end.Add(-time.Duration(minutes) * time.Minute)
	if in.Start != nil {
		start = in.Start.UTC()
	}
	if end.After(time.Now().Add(time.Second)) || end.Before(time.Now().Add(-7*24*time.Hour)) || end.Sub(start) < 5*time.Minute || end.Sub(start) > time.Hour || end.Sub(start)+time.Duration(c.BaselineMinutes)*time.Minute > 2*time.Hour {
		s.fail(w, r, 400, "invalid_window", "Choose a 5–60 minute incident within the past seven days, with at most two hours including comparison context.")
		return
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	id, e := s.enqueue(r.Context(), tx, ws, v, c, "investigation", false, start, end)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if errors.Is(e, errRunConflict) {
		s.fail(w, r, 409, "active_run_conflict", "Another run uses a different window, mapping, or settings. Wait for it to finish or cancel it before starting this investigation.")
		return
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 202, map[string]string{"id": id})
}
func (s *Server) listInvestigations(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT `+runColumns+` FROM runs WHERE workspace_id=$1 AND kind='investigation' AND id::text>$2 ORDER BY id::text LIMIT 100`, workspace(r), r.URL.Query().Get("cursor"))
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
func (s *Server) cancelInvestigation(w http.ResponseWriter, r *http.Request) {
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	ws, id := workspace(r), r.PathValue("id")
	var state string
	var jobID int64
	e = tx.QueryRow(r.Context(), `SELECT state,job_id FROM runs WHERE workspace_id=$1 AND id::text=$2 AND kind='investigation' FOR UPDATE`, ws, id).Scan(&state, &jobID)
	if e != nil {
		s.missing(w, r, e)
		return
	}
	if !terminal(state) {
		if _, e = s.jobs.JobCancelTx(r.Context(), tx, jobID); e != nil {
			s.internal(w, r, e)
			return
		}
		_, e = tx.Exec(r.Context(), `UPDATE runs SET cancelled=true,state='cancelled',finished_at=now() WHERE workspace_id=$1 AND id::text=$2`, ws, id)
		if e == nil {
			_, e = tx.Exec(r.Context(), `INSERT INTO run_events(workspace_id,run_id,stage,message) VALUES($1,$2,'cancelled','Cancelled by administrator.') ON CONFLICT DO NOTHING`, ws, id)
		}
		if e == nil {
			_, e = tx.Exec(r.Context(), `INSERT INTO audit_events(workspace_id,action) VALUES($1,'investigation.cancelled')`, ws)
		}
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

type progressEvent struct {
	ID      int64     `json:"id"`
	Stage   string    `json:"stage"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Polling is the documented reconnect fallback: monotonically increasing event IDs, after cursor.
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	ws, id := workspace(r), r.PathValue("id")
	var exists bool
	e := s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM runs WHERE workspace_id=$1 AND id::text=$2 AND kind='investigation')`, ws, id).Scan(&exists)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if !exists {
		s.missing(w, r, pgx.ErrNoRows)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	rows, e := s.pool.Query(r.Context(), `SELECT id,stage,message,created_at FROM run_events WHERE workspace_id=$1 AND run_id::text=$2 AND id>$3 ORDER BY id LIMIT 100`, ws, id, after)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	events := []progressEvent{}
	for rows.Next() {
		var v progressEvent
		if e = rows.Scan(&v.ID, &v.Stage, &v.Message, &v.At); e != nil {
			s.internal(w, r, e)
			return
		}
		events = append(events, v)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	s.json(w, 200, map[string]any{"items": events})
}
func (s *Server) buildReport(ctx context.Context, a runArgs, v Service, start, end time.Time, baselineMinutes int, evs []Evidence) (*investigations.Report, error) {
	verified := []investigations.Evidence{}
	for _, ev := range evs {
		var record investigations.Evidence
		var raw []byte
		e := s.pool.QueryRow(ctx, `SELECT id::text,workspace_id::text,service_id::text,origin,mapping_revision,expires_at,document FROM evidence WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, ev.ID).Scan(&record.ID, &record.WorkspaceID, &record.ServiceID, &record.Origin, &record.MappingRevision, &record.ExpiresAt, &raw)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &record.Observation); e != nil {
			return nil, e
		}
		verified = append(verified, record)
	}
	report, e := investigations.Build(investigations.Scope{WorkspaceID: a.WorkspaceID, ServiceID: v.ID, Origin: v.Origin, MappingRevision: v.Revision, Start: start, End: end, BaselineStart: start.Add(-time.Duration(baselineMinutes) * time.Minute)}, verified)
	return &report, e
}
func investigationPath(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/v1/investigations")
}
