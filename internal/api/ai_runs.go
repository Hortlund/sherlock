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
	Tools      bool   `json:"tools"`
	Parent     string `json:"parent_ai_run_id"`
	Question   string `json:"question"`
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
	if in.Tools || in.Parent != "" || in.Question != "" {
		s.deepRequest(w, r, in, true)
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
func (s *Server) startAITriageTest(w http.ResponseWriter, r *http.Request) {
	var in aiStart
	if !s.decode(w, r, &in) {
		return
	}
	in.ProviderID = r.PathValue("id")
	s.enqueueAI(w, r, in, "", "triage_test")
}
func (s *Server) startAIAnalysis(w http.ResponseWriter, r *http.Request) {
	var in aiStart
	if !s.decode(w, r, &in) {
		return
	}
	if in.Tools || in.Parent != "" || in.Question != "" {
		s.deepRequest(w, r, in, false)
		return
	}
	s.enqueueAI(w, r, in, r.PathValue("id"))
}
func (s *Server) enqueueAI(w http.ResponseWriter, r *http.Request, in aiStart, runID string, roles ...string) {
	purpose, origin := "test", "demo"
	if len(roles) > 0 {
		purpose = roles[0]
	}
	_, outputLimit := ai.Limits(purpose)
	promptVersion := ai.PromptVersion
	deadlineSeconds := 180
	if ai.IsTriage(purpose) {
		promptVersion = ai.TriageVersion
		deadlineSeconds = 15
	}
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
	if ai.IsTriage(purpose) {
		p = triageProvider(p)
	}
	if !policy.Enabled || !p.Enabled || policy.RunLimit <= 0 || p.InputPrice <= 0 || p.OutputPrice <= 0 {
		s.fail(w, r, 409, "ai_disabled", "Enable workspace processing, provider access and positive budgets/prices first.")
		return
	}
	input, e := s.invocationInput(ctx, tx, ws, runID, "", purpose, p)
	if e != nil {
		s.fail(w, r, 409, "evidence_unavailable", "Evidence is unavailable, expired, out of scope, or the service mapping changed. Collect a new investigation.")
		return
	}
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
	// Completed automatic analysis has the same report contract as manual
	// analysis. Reuse it under the same cache guards, without taking over active
	// automatic work or changing its provenance and cancellation policy.
	e = tx.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE workspace_id=$1 AND provider_id=$2 AND provider_revision=$3 AND policy_revision=$4 AND (digest=$5 OR ($6='analysis' AND deep AND parent_ai_run_id IS NULL AND base_digest=$5 AND state='completed')) AND (purpose=$6 OR ($6='analysis' AND purpose='automatic' AND state='completed')) AND (state IN ('queued','analyzing') OR (state='completed' AND NOT $7 AND report IS NOT NULL AND expires_at>now())) ORDER BY created_at DESC LIMIT 1`, ws, p.ID, p.Revision, policy.Revision, input.Digest, purpose, in.Fresh || purpose == "test" || purpose == "triage_test").Scan(&existing)
	if e == nil {
		var deep bool
		var ids []string
		if e = tx.QueryRow(ctx, `SELECT deep,evidence_ids FROM ai_runs WHERE workspace_id=$1 AND id=$2`, ws, existing).Scan(&deep, &ids); e == nil && deep {
			// A tool report may cite evidence beyond the original snapshot. Every one
			// of those citations must still be retained and allowed before cache reuse.
			if _, err := s.buildAIInput(ctx, tx, ws, runID, p, ids...); err != nil {
				e = pgx.ErrNoRows
			}
		}
	}
	if e == nil {
		s.json(w, 202, map[string]any{"id": existing, "reused": true})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	var active bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_runs WHERE workspace_id=$1 AND state IN ('queued','analyzing') AND (service_id::text=$2 OR ($2='' AND provider_id=$3 AND purpose=$4)))`, ws, input.Service.ID, p.ID, purpose).Scan(&active)
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
	reserved := ai.Cost(int64(input.Bound), int64(outputLimit), p.InputPrice, p.OutputPrice)
	if reserved > policy.RunLimit || admitted+reserved > policy.DailyLimit {
		s.fail(w, r, 409, "budget_exhausted", "AI budget exhausted or reservation exceeds the per-run limit. Existing uncertain reservations still count.")
		return
	}
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO ai_runs(workspace_id,provider_id,provider_revision,policy_revision,run_id,service_id,purpose,model,origin,digest,payload,evidence_ids,reserved,input_bound,input_price,output_price,price_date,expires_at,deadline,output_limit,prompt_version) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,now()+$19*interval '1 second',$20,$21) RETURNING id::text`, ws, p.ID, p.Revision, policy.Revision, runID, input.Service.ID, purpose, p.Model, origin, input.Digest, input.Payload, input.IDs, reserved, input.Bound, p.InputPrice, p.OutputPrice, p.PriceDate, input.Expires, deadlineSeconds, outputLimit, promptVersion).Scan(&id)
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
	rows, e := s.pool.Query(r.Context(), `SELECT id::text,provider_id::text,COALESCE(run_id::text,''),COALESCE(service_id::text,''),purpose,model,origin,state,reason,reserved,charged,accounting,input_bound,output_limit,input_tokens,output_tokens,created_at,started_at,finished_at,expires_at,CASE WHEN expires_at>now() THEN report ELSE NULL END,request_id,deep,tools_enabled,COALESCE(parent_ai_run_id::text,''),CASE WHEN expires_at>now() THEN COALESCE(question,'') ELSE '' END,telemetry_attempts FROM ai_runs WHERE workspace_id=$1 AND ($2='' OR run_id::text=$2) AND ($3='' OR (created_at,id)<(SELECT c.created_at,c.id FROM ai_runs c WHERE c.workspace_id=$1 AND c.id::text=$3 AND ($2='' OR c.run_id::text=$2))) ORDER BY created_at DESC,id DESC LIMIT 51`, workspace(r), r.URL.Query().Get("investigation_id"), r.URL.Query().Get("cursor"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, pid, run, service, purpose, model, origin, state, reason, accounting, requestID string
		var reserved, charged int64
		var inputBound, outputLimit, telemetryAttempts int
		var deep, tools bool
		var parent, question string
		var input, output *int64
		var created, expires time.Time
		var started, finished *time.Time
		var report []byte
		e = rows.Scan(&id, &pid, &run, &service, &purpose, &model, &origin, &state, &reason, &reserved, &charged, &accounting, &inputBound, &outputLimit, &input, &output, &created, &started, &finished, &expires, &report, &requestID, &deep, &tools, &parent, &question, &telemetryAttempts)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		var doc any
		if len(report) > 0 {
			doc = json.RawMessage(report)
		}
		items = append(items, map[string]any{"id": id, "provider_id": pid, "investigation_id": run, "service_id": service, "purpose": purpose, "model": model, "origin": origin, "state": state, "reason": reason, "reserved": reserved, "charged": charged, "accounting": accounting, "input_bound": inputBound, "output_limit": outputLimit, "input_tokens": input, "output_tokens": output, "created_at": created, "started_at": started, "finished_at": finished, "expires_at": expires, "report": doc, "request_id": requestID, "deep": deep, "tools": tools, "parent_ai_run_id": parent, "question": question, "telemetry_attempts": telemetryAttempts})
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
		tag, e = tx.Exec(ctx, `UPDATE ai_runs SET cancel_requested=true,state=CASE WHEN state IN ('queued','analyzing') THEN 'cancelled' ELSE state END,reason=CASE WHEN state IN ('queued','analyzing') THEN 'Cancelled by administrator; transmitted data cannot be recalled.' ELSE reason END,finished_at=CASE WHEN state IN ('queued','analyzing') THEN now() ELSE finished_at END,accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE accounting END,payload=NULL,report=CASE WHEN $3 THEN NULL ELSE report END WHERE workspace_id=$1 AND id::text=$2`, ws, r.PathValue("id"), purge)
		if e == nil && tag.RowsAffected() == 0 {
			s.missing(w, r, pgx.ErrNoRows)
			return
		}
	}
	if e == nil {
		// Admission holds this same accounting lock: if cancellation wins it
		// sees cancel_requested; if admission wins we cancel its child here.
		// Read batch links without locking/updating batches after accounting.
		_, e = tx.Exec(ctx, `UPDATE ai_runs SET cancel_requested=true,state='cancelled',reason='Upstream triage was cancelled by administrator.',finished_at=now(),payload=NULL,accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE accounting END WHERE workspace_id=$1 AND purpose='automatic' AND state IN ('queued','analyzing') AND automation_batch_id IN (SELECT id FROM automation_batches WHERE workspace_id=$1 AND ai_run_id::text=$2)`, ws, r.PathValue("id"))
	}
	if e == nil && purge {
		_, e = tx.Exec(ctx, `WITH RECURSIVE children AS (SELECT id FROM ai_runs WHERE workspace_id=$1 AND id::text=$2 UNION ALL SELECT a.id FROM ai_runs a JOIN children c ON a.parent_ai_run_id=c.id WHERE a.workspace_id=$1) UPDATE ai_runs SET cancel_requested=true,payload=NULL,report=NULL,question=NULL,state=CASE WHEN state IN ('queued','analyzing') THEN 'cancelled' ELSE state END WHERE workspace_id=$1 AND id IN (SELECT id FROM children)`, ws, r.PathValue("id"))
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
