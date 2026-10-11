package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"net/http"
	"sherlock/internal/ai"
	"sherlock/internal/automation"
)

// Called in the recommendation transaction, after the local decision. The AI
// assessment is advisory; it cannot mutate the rule decision, findings or alerts.
func (s *Server) enqueueTriage(ctx context.Context, tx pgx.Tx, ws, batch string, policy automation.Policy) error {
	if policy.TriageProviderID == "" {
		return nil
	}
	if e := aiWorkspaceLock(ctx, tx, ws); e != nil {
		return e
	}
	fallback := func(reason string) error {
		_, e := tx.Exec(ctx, `UPDATE automation_batches SET triage_status='fallback',triage_reason=$3 WHERE workspace_id=$1 AND id=$2`, ws, batch, reason)
		return e
	}
	p, e := loadAIProvider(ctx, tx, ws, policy.TriageProviderID)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return fallback("Triage provider unavailable; local recommendation retained.")
		}
		return e
	}
	p = triageProvider(p)
	ap, e := loadAIPolicy(ctx, tx, ws)
	if e != nil {
		return e
	}
	if !ap.Enabled || !p.Enabled || p.Model == "" || p.Capability != "ready" || ap.RunLimit <= 0 || ap.DailyLimit <= 0 || p.InputPrice <= 0 || p.OutputPrice <= 0 {
		return fallback("Triage requires workspace consent, positive budgets and a tested triage model; local recommendation retained.")
	}
	input, e := s.buildTriageInput(ctx, tx, ws, batch, p)
	if e != nil {
		return fallback("Triage summary unavailable, oversized, or outside provider service consent; local recommendation retained.")
	}
	var id string
	e = tx.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE workspace_id=$1 AND provider_id=$2 AND provider_revision=$3 AND policy_revision=$4 AND digest=$5 AND purpose='triage' AND NOT cancel_requested AND state='completed' AND report IS NOT NULL AND expires_at>now() ORDER BY created_at DESC LIMIT 1`, ws, p.ID, p.Revision, ap.Revision, input.Digest).Scan(&id)
	if e == nil {
		_, e = tx.Exec(ctx, `UPDATE automation_batches SET ai_run_id=$3,triage_status='cached',triage_reason='Reused a validated assessment of the same scoped evidence.' WHERE workspace_id=$1 AND id=$2`, ws, batch, id)
		return e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	var active bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_runs WHERE workspace_id=$1 AND service_id=$2 AND state IN ('queued','analyzing'))`, ws, input.Service.ID).Scan(&active); e != nil {
		return e
	}
	if active {
		return fallback("Another AI task is active for this service; local recommendation retained.")
	}
	var admitted int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN accounting IN ('reserved','unknown') THEN reserved ELSE charged END),0) FROM ai_runs WHERE workspace_id=$1 AND (created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' OR accounting IN ('reserved','unknown'))`, ws).Scan(&admitted); e != nil {
		return e
	}
	reserved := ai.Cost(int64(input.Bound), ai.TriageOutputLimit, p.InputPrice, p.OutputPrice)
	if reserved > ap.RunLimit || admitted+reserved > ap.DailyLimit {
		return fallback("AI budget unavailable, including unresolved reservations; local recommendation retained.")
	}
	e = tx.QueryRow(ctx, `INSERT INTO ai_runs(workspace_id,provider_id,provider_revision,policy_revision,service_id,purpose,model,origin,digest,payload,evidence_ids,reserved,input_bound,output_limit,input_price,output_price,price_date,expires_at,deadline,prompt_version,triage_batch_id) VALUES($1,$2,$3,$4,$5,'triage',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,now()+interval '15 seconds',$18,$19) RETURNING id::text`, ws, p.ID, p.Revision, ap.Revision, input.Service.ID, p.Model, input.Service.Origin, input.Digest, input.Payload, input.IDs, reserved, input.Bound, ai.TriageOutputLimit, p.InputPrice, p.OutputPrice, p.PriceDate, input.Expires, ai.TriageVersion, batch).Scan(&id)
	if e != nil {
		return e
	}
	job, e := s.jobs.InsertTx(ctx, tx, aiArgs{WorkspaceID: ws, ID: id}, &river.InsertOpts{Queue: "ai", MaxAttempts: 20})
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE ai_runs SET job_id=$2 WHERE id=$1`, id, job.Job.ID); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE automation_batches SET ai_run_id=$3,triage_status='queued',triage_reason='Optional AI assessment queued; automation policy controls admission.' WHERE workspace_id=$1 AND id=$2`, ws, batch, id)
	return e
}

// Always acquire workspace scope -> service -> automation policy -> Monitoring
// settings -> findings before AI accounting. Starting with the shared workspace
// guard prevents a mapping editor from holding that row while waiting on us.
// Service keys are unchanged, so manual admission's FK checks stay compatible.
func lockTriageScope(ctx context.Context, tx pgx.Tx, ws, batch string) error {
	if batch == "" {
		return nil
	}
	if e := lockWorkspaceScope(ctx, tx, ws); e != nil {
		return e
	}
	statements := []string{
		`SELECT s.id FROM services s JOIN automation_batches b ON b.workspace_id=s.workspace_id AND b.service_id=s.id WHERE b.workspace_id=$1 AND b.id::text=$2 FOR NO KEY UPDATE OF s`,
		`SELECT workspace_id FROM automation_policy WHERE workspace_id=$1 AND $2::text<>'' FOR SHARE`,
		`SELECT pg_advisory_xact_lock_shared(hashtextextended($1,731903)) WHERE $2::text<>''`,
		`SELECT f.id FROM findings f JOIN automation_items i ON i.workspace_id=f.workspace_id AND i.finding_id=f.id WHERE i.workspace_id=$1 AND i.batch_id::text=$2 FOR SHARE OF f`,
	}
	for _, q := range statements {
		if _, e := tx.Exec(ctx, q, ws, batch); e != nil {
			return e
		}
	}
	return nil
}

func (s *Server) previewTriage(w http.ResponseWriter, r *http.Request) {
	p, e := loadAIProvider(r.Context(), s.pool, workspace(r), r.PathValue("id"))
	if e != nil {
		s.missing(w, r, e)
		return
	}
	p = triageProvider(p)
	if p.Model == "" {
		s.fail(w, r, 400, "triage_unconfigured", "Configure a triage model first.")
		return
	}
	input, e := s.buildTriageInput(r.Context(), s.pool, workspace(r), "", p)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	body, e := ai.RequestBody(p.Kind, p.Model, input.Payload, "triage")
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"payload": json.RawMessage(body), "sends_data": false, "input_bound": input.Bound, "output_limit": ai.TriageOutputLimit, "reservation": ai.Cost(int64(input.Bound), ai.TriageOutputLimit, p.InputPrice, p.OutputPrice)})
}
