package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sherlock/internal/automation"
	"time"

	"github.com/jackc/pgx/v5"
)

func loadAutomationPolicy(ctx context.Context, q aiDB, ws string) (automation.Policy, error) {
	p := automation.Defaults()
	var raw []byte
	var revision int
	e := q.QueryRow(ctx, `SELECT config,revision FROM automation_policy WHERE workspace_id=$1`, ws).Scan(&raw, &revision)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, nil
	}
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(raw, &p)
	p.Revision = revision
	return p, e
}
func (s *Server) automationRoutes(m *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"GET /api/v1/automation/policy":   s.getAutomationPolicy,
		"POST /api/v1/automation/policy":  s.saveAutomationPolicy,
		"GET /api/v1/automation/activity": s.listAutomationActivity,
	} {
		m.Handle(path, s.requireSession(h))
	}
}
func (s *Server) getAutomationPolicy(w http.ResponseWriter, r *http.Request) {
	p, e := loadAutomationPolicy(r.Context(), s.pool, workspace(r))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, p)
}
func (s *Server) saveAutomationPolicy(w http.ResponseWriter, r *http.Request) {
	var p automation.Policy
	if !s.decode(w, r, &p) {
		return
	}
	if !p.Valid() {
		s.fail(w, r, 400, "invalid_automation_policy", "Choose Off or Recommendations, a cooldown of 5–1440 minutes, and a growth multiplier of 1.5–10.")
		return
	}
	if p.Services == nil {
		p.Services = []string{}
	}
	unique := map[string]bool{}
	for _, id := range p.Services {
		if unique[id] {
			s.fail(w, r, 400, "invalid_services", "Select each service once.")
			return
		}
		unique[id] = true
	}
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	raw, _ := json.Marshal(automation.Defaults())
	if _, e = tx.Exec(ctx, `INSERT INTO automation_policy(workspace_id,config,revision) VALUES($1,$2,0) ON CONFLICT DO NOTHING`, ws, raw); e != nil {
		s.internal(w, r, e)
		return
	}
	var revision, count int
	if e = tx.QueryRow(ctx, `SELECT revision FROM automation_policy WHERE workspace_id=$1 FOR UPDATE`, ws).Scan(&revision); e != nil {
		s.internal(w, r, e)
		return
	}
	if revision != p.Revision {
		s.fail(w, r, 409, "revision_conflict", "Automation settings changed. Refresh before saving.")
		return
	}
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM services WHERE workspace_id=$1 AND id::text=ANY($2)`, ws, p.Services).Scan(&count); e != nil {
		s.internal(w, r, e)
		return
	}
	if count != len(p.Services) {
		s.fail(w, r, 400, "invalid_services", "Choose services from this workspace.")
		return
	}
	p.Revision++
	raw, _ = json.Marshal(p)
	_, e = tx.Exec(ctx, `UPDATE automation_policy SET config=$2,revision=$3,updated_at=clock_timestamp() WHERE workspace_id=$1`, ws, raw, p.Revision)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'automation.policy_changed')`, ws)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, p)
}

type automationItem struct {
	FindingID      string                 `json:"finding_id"`
	Title          string                 `json:"title"`
	Trigger        string                 `json:"trigger"`
	Recommendation string                 `json:"recommendation"`
	Reason         string                 `json:"reason"`
	Measurement    automation.Measurement `json:"measurement"`
	ObservedAt     time.Time              `json:"observed_at"`
}
type automationActivity struct {
	ID              string           `json:"id"`
	ServiceID       string           `json:"service_id"`
	ServiceName     string           `json:"service_name"`
	Origin          string           `json:"origin"`
	State           string           `json:"state"`
	Reason          string           `json:"reason"`
	PolicyRevision  int              `json:"policy_revision"`
	MappingRevision int              `json:"mapping_revision"`
	CreatedAt       time.Time        `json:"created_at"`
	DueAt           time.Time        `json:"due_at"`
	EvaluatedAt     *time.Time       `json:"evaluated_at"`
	NextEligibleAt  *time.Time       `json:"next_eligible_at"`
	Items           []automationItem `json:"items"`
}

func (s *Server) listAutomationActivity(w http.ResponseWriter, r *http.Request) {
	ctx, ws := r.Context(), workspace(r)
	service, cursor := r.URL.Query().Get("service_id"), r.URL.Query().Get("cursor")
	rows, e := s.pool.Query(ctx, `SELECT b.id::text,b.service_id::text,s.name,s.origin,b.state,b.reason,b.policy_revision,b.mapping_revision,b.created_at,b.due_at,b.evaluated_at,b.next_eligible_at,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('finding_id',i.finding_id,'title',f.title,'trigger',i.trigger,'recommendation',i.recommendation,'reason',i.reason,'measurement',i.measurement,'observed_at',i.observed_at) ORDER BY i.finding_id) FROM automation_items i JOIN findings f ON f.workspace_id=i.workspace_id AND f.id=i.finding_id WHERE i.workspace_id=b.workspace_id AND i.batch_id=b.id),'[]'::jsonb)
 FROM automation_batches b JOIN services s ON s.workspace_id=b.workspace_id AND s.id=b.service_id
 WHERE b.workspace_id=$1 AND ($2='' OR b.service_id::text=$2) AND ($3='' OR (b.created_at,b.id)<(SELECT created_at,id FROM automation_batches WHERE workspace_id=$1 AND id::text=$3 AND ($2='' OR service_id::text=$2)))
 ORDER BY b.created_at DESC,b.id DESC LIMIT 51`, ws, service, cursor)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []automationActivity{}
	for rows.Next() {
		var a automationActivity
		var raw []byte
		if e = rows.Scan(&a.ID, &a.ServiceID, &a.ServiceName, &a.Origin, &a.State, &a.Reason, &a.PolicyRevision, &a.MappingRevision, &a.CreatedAt, &a.DueAt, &a.EvaluatedAt, &a.NextEligibleAt, &raw); e != nil {
			s.internal(w, r, e)
			return
		}
		if e = json.Unmarshal(raw, &a.Items); e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, a)
	}
	if e = rows.Err(); e != nil {
		s.internal(w, r, e)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = items[49].ID
	}
	s.json(w, 200, map[string]any{"items": items, "next_cursor": next})
}
