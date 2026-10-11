package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"sherlock/internal/ai"
	"sherlock/internal/investigations"
)

type automaticArgs struct {
	WorkspaceID string `json:"workspace_id"`
	BatchID     string `json:"batch_id"`
}

func (automaticArgs) Kind() string { return "automatic_admission" }

type automaticWorker struct {
	river.WorkerDefaults[automaticArgs]
	server *Server
}

func (w *automaticWorker) Work(ctx context.Context, j *river.Job[automaticArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return w.server.admitAutomatic(ctx, j.Args)
}

// Admission and its job, immutable evidence report, cost reservation and cooldown
// commit together. Scope locks precede accounting, as in triage execution.
func (s *Server) admitAutomatic(ctx context.Context, a automaticArgs) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockTriageScope(ctx, tx, a.WorkspaceID, a.BatchID); e != nil {
		return e
	}
	if e = aiWorkspaceLock(ctx, tx, a.WorkspaceID); e != nil {
		return e
	}
	var status, triageID string
	var created time.Time
	var exhausted bool
	e = tx.QueryRow(ctx, `SELECT admission_status,COALESCE(ai_run_id::text,''),created_at,EXISTS(SELECT 1 FROM river_job j WHERE j.id=b.admission_job_id AND j.state IN ('discarded','cancelled')) FROM automation_batches b WHERE workspace_id=$1 AND id::text=$2 FOR UPDATE`, a.WorkspaceID, a.BatchID).Scan(&status, &triageID, &created, &exhausted)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if status != "pending" {
		return nil
	}
	skip := func(reason string) error {
		_, err := tx.Exec(ctx, `UPDATE automation_batches SET admission_status='skipped',admission_reason=$3 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.BatchID, reason)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return err
	}
	if exhausted || time.Since(created) > 15*time.Minute {
		return skip("Admission expired or exhausted its retries; old work is not replayed.")
	}
	policy, e := loadAutomationPolicy(ctx, tx, a.WorkspaceID)
	if e != nil {
		return e
	}
	if policy.Mode != "auto_investigate" || !policy.AutomaticConsent {
		return skip("Automatic investigations are off or recurring consent was revoked.")
	}
	p, e := loadAIProvider(ctx, tx, a.WorkspaceID, policy.InvestigationProviderID)
	if errors.Is(e, pgx.ErrNoRows) {
		return skip("Investigation provider is unavailable.")
	}
	if e != nil {
		return e
	}
	ap, e := loadAIPolicy(ctx, tx, a.WorkspaceID)
	if e != nil {
		return e
	}
	if !ap.Enabled || !p.Enabled || p.Capability != "ready" || p.Model == "" || ap.RunLimit <= 0 || ap.DailyLimit <= 0 || p.InputPrice <= 0 || p.OutputPrice <= 0 {
		return skip("Automatic analysis requires AI consent, a tested investigation model, positive prices and daily/per-run budgets.")
	}
	summary, e := s.buildRecommendationInput(ctx, tx, a.WorkspaceID, a.BatchID, p, true)
	if e != nil {
		var unavailable recommendationUnavailable
		if errors.As(e, &unavailable) {
			return skip(unavailable.Error())
		}
		return e
	}
	reason := "Local rules admitted bounded analysis of the triggering evidence."
	if triageID != "" {
		var state string
		var report []byte
		var cancelled bool
		var expires, deadline time.Time
		e = tx.QueryRow(ctx, `SELECT state,report,expires_at,deadline,cancel_requested FROM ai_runs WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, triageID).Scan(&state, &report, &expires, &deadline, &cancelled)
		if e != nil {
			return e
		}
		if cancelled || state == "cancelled" {
			return skip("Triage was cancelled; automatic investigation was not started.")
		}
		if state == "queued" || state == "analyzing" {
			// Never overlap an unresolved triage dispatch. Its worker/retention pass
			// settles pre-dispatch failures or preserves unknown reservations.
			if time.Now().After(deadline.Add(30 * time.Second)) {
				return skip("Triage did not finish in time; no automatic investigation was started.")
			}
			return river.JobSnooze(time.Second)
		}
		var advice ai.TriageDecision
		if state == "completed" && expires.After(time.Now()) && json.Unmarshal(report, &advice) == nil {
			if advice.Decision != "investigate" {
				return skip("AI triage recommended " + advice.Decision + "; local recommendation and factual alerts are unchanged.")
			}
			reason = "AI triage recommended investigation; server policy admitted bounded analysis."
		} else {
			reason = "AI triage unavailable; local rules admitted bounded analysis within the remaining budget."
		}
	}
	var active bool
	var next *time.Time
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_runs WHERE workspace_id=$1 AND service_id=$2 AND state IN ('queued','analyzing'))`, a.WorkspaceID, summary.Service.ID).Scan(&active); e != nil {
		return e
	}
	if active {
		return skip("Another AI task is active for this service.")
	}
	e = tx.QueryRow(ctx, `SELECT next_investigation_at FROM automation_services WHERE workspace_id=$1 AND service_id=$2`, a.WorkspaceID, summary.Service.ID).Scan(&next)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if next != nil && next.After(time.Now()) {
		return skip("Service automatic investigation cooldown is active.")
	}

	// Roll back provisional report creation for any expected admission refusal.
	// The outer transaction retains the visible skip decision.
	if _, e = tx.Exec(ctx, "SAVEPOINT automatic_snapshot"); e != nil {
		return e
	}
	skipSnapshot := func(reason string) error {
		if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT automatic_snapshot"); err != nil {
			return err
		}
		return skip(reason)
	}
	run, e := s.automaticSnapshot(ctx, tx, a.WorkspaceID, summary)
	if e != nil {
		if errors.Is(e, errAutomaticEvidence) {
			return skipSnapshot("Triggering evidence cannot form a consistent, adequate investigation report.")
		}
		return e
	}
	input, e := s.buildAIInput(ctx, tx, a.WorkspaceID, run, p)
	if e != nil {
		return skipSnapshot("Triggering evidence is unavailable or exceeds the bounded analysis context.")
	}
	reserved := ai.Cost(int64(input.Bound), ai.OutputLimit, p.InputPrice, p.OutputPrice)
	var admitted int64
	if e = tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN accounting IN ('reserved','unknown') THEN reserved ELSE charged END),0) FROM ai_runs WHERE workspace_id=$1 AND (created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' OR accounting IN ('reserved','unknown'))`, a.WorkspaceID).Scan(&admitted); e != nil {
		return e
	}
	if reserved > ap.RunLimit || admitted+reserved > ap.DailyLimit {
		return skipSnapshot("AI budget unavailable, including unresolved reservations.")
	}
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO ai_runs(workspace_id,provider_id,provider_revision,policy_revision,run_id,service_id,purpose,model,origin,digest,payload,evidence_ids,reserved,input_bound,output_limit,input_price,output_price,price_date,expires_at,deadline,prompt_version,automation_batch_id) VALUES($1,$2,$3,$4,$5,$6,'automatic',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,now()+interval '180 seconds',$19,$20) RETURNING id::text`, a.WorkspaceID, p.ID, p.Revision, ap.Revision, run, input.Service.ID, p.Model, input.Service.Origin, input.Digest, input.Payload, input.IDs, reserved, input.Bound, ai.OutputLimit, p.InputPrice, p.OutputPrice, p.PriceDate, input.Expires, ai.PromptVersion, a.BatchID).Scan(&id)
	if e != nil {
		return e
	}
	job, e := s.jobs.InsertTx(ctx, tx, aiArgs{a.WorkspaceID, id}, &river.InsertOpts{Queue: "ai", MaxAttempts: 20})
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE ai_runs SET job_id=$2 WHERE id=$1`, id, job.Job.ID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE automation_batches SET automatic_ai_run_id=$3,admission_status='launched',admission_reason=$4 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.BatchID, id, reason); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE automation_services SET next_investigation_at=now()+$3*interval '1 minute' WHERE workspace_id=$1 AND service_id=$2`, a.WorkspaceID, input.Service.ID, policy.CooldownMinutes); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.automatic_queued')`, a.WorkspaceID); e != nil {
		return e
	}
	// Report-ready delivery uses existing independent routing, never model text.
	if e = s.notificationEvent(ctx, tx, a.WorkspaceID, "investigation.completed:"+run, "investigation.completed", input.Service.ID, "", run, input.Service.Origin, "notice", input.IDs); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

var errAutomaticEvidence = errors.New("inconsistent automatic evidence")

func (s *Server) automaticSnapshot(ctx context.Context, tx pgx.Tx, ws string, input aiInput) (string, error) {
	cfg, e := loadDetectorSettings(ctx, tx, ws)
	if e != nil {
		return "", e
	}
	items := []investigations.Evidence{}
	var end time.Time
	for _, id := range input.IDs {
		var ev investigations.Evidence
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,service_id::text,origin,mapping_revision,expires_at,document FROM evidence WHERE workspace_id=$1 AND id::text=$2`, ws, id).Scan(&ev.ID, &ev.WorkspaceID, &ev.ServiceID, &ev.Origin, &ev.MappingRevision, &ev.ExpiresAt, &raw)
		if e != nil {
			return "", e
		}
		if e = json.Unmarshal(raw, &ev.Observation); e != nil {
			return "", errAutomaticEvidence
		}
		if ev.Observation.End.After(end) {
			end = ev.Observation.End
		}
		items = append(items, ev)
	}
	start := end.Add(-time.Duration(cfg.CurrentMinutes) * time.Minute)
	report, e := investigations.Build(investigations.Scope{WorkspaceID: ws, ServiceID: input.Service.ID, Origin: input.Service.Origin, MappingRevision: input.Service.Revision, Start: start, End: end, BaselineStart: start.Add(-time.Duration(cfg.BaselineMinutes) * time.Minute)}, items)
	if e != nil || !report.Adequate {
		return "", errAutomaticEvidence
	}
	result, _ := json.Marshal(RunResult{Report: &report, EvidenceIDs: input.IDs, Issues: []string{"Automatic investigation uses the triggering scan evidence; no fresh telemetry was collected."}})
	snapshot, _ := json.Marshal(input.Service)
	config, _ := json.Marshal(cfg)
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO runs(workspace_id,service_id,kind,state,snapshot,config,window_start,window_end,scheduled,finished_at,result) VALUES($1,$2,'investigation','completed',$3,$4,$5,$6,true,now(),$7) RETURNING id::text`, ws, input.Service.ID, snapshot, config, start, end, result).Scan(&id)
	if e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `INSERT INTO run_events(workspace_id,run_id,stage,message) VALUES($1,$2,'completed','Automatic evidence report created from the triggering scan; bounded AI analysis queued.')`, ws, id)
	return id, e
}

func (s *Server) retainAutomatic(ctx context.Context) error {
	rows, e := s.pool.Query(ctx, `SELECT workspace_id::text,id::text FROM automation_batches b WHERE admission_status='pending' AND (created_at<now()-interval '15 minutes' OR EXISTS(SELECT 1 FROM river_job j WHERE j.id=b.admission_job_id AND j.state IN ('discarded','cancelled'))) LIMIT 100`)
	if e != nil {
		return e
	}
	args := []automaticArgs{}
	for rows.Next() {
		var a automaticArgs
		if e = rows.Scan(&a.WorkspaceID, &a.BatchID); e != nil {
			break
		}
		args = append(args, a)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	for _, a := range args {
		if e = s.admitAutomatic(ctx, a); e != nil {
			return e
		}
	}
	return nil
}
