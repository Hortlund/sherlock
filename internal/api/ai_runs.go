package api

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"net/http"
	"sherlock/internal/ai"
	"time"
)

type aiStart struct {
	ProviderID string `json:"provider_id"`
	Confirm    bool   `json:"confirm"`
	Digest     string `json:"preview_digest"`
	Fresh      bool   `json:"fresh"`
}

func (s *Server) previewAI(w http.ResponseWriter, r *http.Request) {
	var in aiStart
	if !s.decode(w, r, &in) {
		return
	}
	p, e := loadAIProvider(r.Context(), s.pool, workspace(r), in.ProviderID)
	if e != nil {
		s.missing(w, r, e)
		return
	}
	input, e := s.buildAIInput(r.Context(), s.pool, workspace(r), r.PathValue("id"), p)
	if e != nil {
		s.fail(w, r, 409, "evidence_unavailable", "Evidence is unavailable, expired, out of scope, or the service mapping changed. Collect a new investigation.")
		return
	}
	body, e := ai.RequestBody(p.Kind, p.Model, input.Payload)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"payload": json.RawMessage(body), "preview_digest": input.Digest, "provider_id": p.ID, "model": p.Model, "input_bound": input.Bound, "output_limit": ai.OutputLimit, "reservation": ai.Cost(int64(input.Bound), ai.OutputLimit, p.InputPrice, p.OutputPrice), "expires_at": input.Expires, "sends_data": false})
}
func (s *Server) startAITest(w http.ResponseWriter, r *http.Request) {
	var in aiStart
	if !s.decode(w, r, &in) {
		return
	}
	in.ProviderID = r.PathValue("id")
	s.enqueueAI(w, r, in, "")
}
func (s *Server) startAIAnalysis(w http.ResponseWriter, r *http.Request) {
	var in aiStart
	if !s.decode(w, r, &in) {
		return
	}
	s.enqueueAI(w, r, in, r.PathValue("id"))
}
func (s *Server) enqueueAI(w http.ResponseWriter, r *http.Request, in aiStart, runID string) {
	if !in.Confirm {
		s.fail(w, r, 400, "confirmation_required", "Confirm sending this bounded payload to the selected provider. API usage may incur charges.")
		return
	}
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = aiWorkspaceLock(ctx, tx, ws); e != nil {
		s.internal(w, r, e)
		return
	}
	policy, e := loadAIPolicy(ctx, tx, ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	p, e := loadAIProvider(ctx, tx, ws, in.ProviderID)
	if e != nil {
		s.missing(w, r, e)
		return
	}
	if !policy.Enabled || !p.Enabled || policy.RunLimit <= 0 || p.InputPrice <= 0 || p.OutputPrice <= 0 {
		s.fail(w, r, 409, "ai_disabled", "Enable workspace processing, provider access and positive budgets/prices first.")
		return
	}
	input, e := s.buildAIInput(ctx, tx, ws, runID, p)
	if e != nil {
		s.fail(w, r, 409, "evidence_unavailable", "Evidence is unavailable, expired, out of scope, or the service mapping changed. Collect a new investigation.")
		return
	}
	purpose, origin := "test", "demo"
	if runID != "" {
		purpose = "analysis"
		origin = input.Service.Origin
		if !aiAllowed(p, input.Service.ID, origin) {
			s.fail(w, r, 403, "egress_denied", "This service or data origin is not allowed to use the selected provider.")
			return
		}
		if p.Capability != "ready" {
			s.fail(w, r, 409, "capability_required", "Run a successful synthetic capability test for this model configuration first.")
			return
		}
		if in.Digest != input.Digest {
			s.fail(w, r, 409, "preview_changed", "Preview the current payload before sending it.")
			return
		}
	}
	var existing string
	e = tx.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE workspace_id=$1 AND provider_id=$2 AND provider_revision=$3 AND policy_revision=$4 AND digest=$5 AND purpose=$6 AND (state IN ('queued','analyzing') OR (state='completed' AND NOT $7 AND report IS NOT NULL AND expires_at>now())) ORDER BY created_at DESC LIMIT 1`, ws, p.ID, p.Revision, policy.Revision, input.Digest, purpose, in.Fresh || purpose == "test").Scan(&existing)
	if e == nil {
		s.json(w, 202, map[string]any{"id": existing, "reused": true})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	var active bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_runs WHERE workspace_id=$1 AND state IN ('queued','analyzing') AND (service_id::text=$2 OR ($2='' AND provider_id=$3 AND purpose='test')))`, ws, input.Service.ID, p.ID).Scan(&active)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if active {
		s.fail(w, r, 409, "ai_active", "An AI run is already active for this service or capability test.")
		return
	}
	var admitted int64
	e = tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN accounting IN ('reserved','unknown') THEN reserved ELSE charged END),0) FROM ai_runs WHERE workspace_id=$1 AND (created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' OR accounting IN ('reserved','unknown'))`, ws).Scan(&admitted)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	reserved := ai.Cost(int64(input.Bound), ai.OutputLimit, p.InputPrice, p.OutputPrice)
	if reserved > policy.RunLimit || admitted+reserved > policy.DailyLimit {
		s.fail(w, r, 409, "budget_exhausted", "AI budget exhausted or reservation exceeds the per-run limit. Existing uncertain reservations still count.")
		return
	}
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO ai_runs(workspace_id,provider_id,provider_revision,policy_revision,run_id,service_id,purpose,model,origin,digest,payload,evidence_ids,reserved,input_bound,input_price,output_price,price_date,expires_at,deadline) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,now()+interval '180 seconds') RETURNING id::text`, ws, p.ID, p.Revision, policy.Revision, runID, input.Service.ID, purpose, p.Model, origin, input.Digest, input.Payload, input.IDs, reserved, input.Bound, p.InputPrice, p.OutputPrice, p.PriceDate, input.Expires).Scan(&id)
	if e == nil {
		var job *rivertype.JobInsertResult
		job, e = s.jobs.InsertTx(ctx, tx, aiArgs{WorkspaceID: ws, ID: id}, &river.InsertOpts{Queue: "ai", MaxAttempts: 20})
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE ai_runs SET job_id=$2 WHERE id=$1`, id, job.Job.ID)
		}
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,$2)`, ws, "ai."+purpose+"_queued")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 202, map[string]any{"id": id, "reused": false})
}
func (s *Server) listAIRuns(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT id::text,provider_id::text,COALESCE(run_id::text,''),COALESCE(service_id::text,''),purpose,model,origin,state,reason,reserved,charged,accounting,input_bound,output_limit,input_tokens,output_tokens,created_at,started_at,finished_at,expires_at,CASE WHEN expires_at>now() THEN report ELSE NULL END,request_id FROM ai_runs WHERE workspace_id=$1 AND ($2='' OR run_id::text=$2) AND ($3='' OR (created_at,id)<(SELECT c.created_at,c.id FROM ai_runs c WHERE c.workspace_id=$1 AND c.id::text=$3 AND ($2='' OR c.run_id::text=$2))) ORDER BY created_at DESC,id DESC LIMIT 51`, workspace(r), r.URL.Query().Get("investigation_id"), r.URL.Query().Get("cursor"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, pid, run, service, purpose, model, origin, state, reason, accounting, requestID string
		var reserved, charged int64
		var inputBound, outputLimit int
		var input, output *int64
		var created, expires time.Time
		var started, finished *time.Time
		var report []byte
		e = rows.Scan(&id, &pid, &run, &service, &purpose, &model, &origin, &state, &reason, &reserved, &charged, &accounting, &inputBound, &outputLimit, &input, &output, &created, &started, &finished, &expires, &report, &requestID)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		var doc any
		if len(report) > 0 {
			doc = json.RawMessage(report)
		}
		items = append(items, map[string]any{"id": id, "provider_id": pid, "investigation_id": run, "service_id": service, "purpose": purpose, "model": model, "origin": origin, "state": state, "reason": reason, "reserved": reserved, "charged": charged, "accounting": accounting, "input_bound": inputBound, "output_limit": outputLimit, "input_tokens": input, "output_tokens": output, "created_at": created, "started_at": started, "finished_at": finished, "expires_at": expires, "report": doc, "request_id": requestID})
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
func (s *Server) cancelAI(w http.ResponseWriter, r *http.Request) { s.changeAIContent(w, r, false) }
func (s *Server) deleteAIContent(w http.ResponseWriter, r *http.Request) {
	s.changeAIContent(w, r, true)
}
func (s *Server) changeAIContent(w http.ResponseWriter, r *http.Request, purge bool) {
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	e = aiWorkspaceLock(ctx, tx, ws)
	if e == nil {
		var tag pgconn.CommandTag
		tag, e = tx.Exec(ctx, `UPDATE ai_runs SET state=CASE WHEN state IN ('queued','analyzing') THEN 'cancelled' ELSE state END,reason=CASE WHEN state IN ('queued','analyzing') THEN 'Cancelled by administrator; transmitted data cannot be recalled.' ELSE reason END,finished_at=CASE WHEN state IN ('queued','analyzing') THEN now() ELSE finished_at END,accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE accounting END,payload=NULL,report=CASE WHEN $3 THEN NULL ELSE report END WHERE workspace_id=$1 AND id::text=$2`, ws, r.PathValue("id"), purge)
		if e == nil && tag.RowsAffected() == 0 {
			s.missing(w, r, pgx.ErrNoRows)
			return
		}
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.cancel_or_delete')`, ws)
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
func (s *Server) reconcileAI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm bool  `json:"confirm"`
		Cost    int64 `json:"cost"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !in.Confirm || in.Cost < 0 || in.Cost > 1000000000 {
		s.fail(w, r, 400, "invalid_reconciliation", "Confirm a verified provider charge between $0 and $1,000.")
		return
	}
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	e = aiWorkspaceLock(ctx, tx, ws)
	if e == nil {
		var tag pgconn.CommandTag
		tag, e = tx.Exec(ctx, `UPDATE ai_runs SET charged=$3,accounting='reconciled' WHERE workspace_id=$1 AND id::text=$2 AND accounting='unknown' AND state NOT IN ('queued','analyzing') AND started_at<now()-interval '4 minutes'`, ws, r.PathValue("id"), in.Cost)
		if e == nil && tag.RowsAffected() == 0 {
			s.fail(w, r, 409, "not_reconcilable", "Only a finished uncertain invocation older than four minutes can be reconciled.")
			return
		}
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.cost_reconciled')`, ws)
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
