package api

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"sherlock/internal/automation"
	"sherlock/internal/inbound"
)

// Used in the final provider preflight and tool request-budget update. Receipt
// revocation must take effect before HTTP even when the earlier scope check ran
// before a concurrent service save. Manual AI rows have no inbound receipt.
const inboundAIDispatchScope = `(ai_runs.inbound_receipt_id IS NULL OR EXISTS(SELECT 1 FROM inbound_receipts x JOIN services v ON v.workspace_id=x.workspace_id AND v.id=ai_runs.service_id WHERE x.workspace_id=$1 AND x.id=ai_runs.inbound_receipt_id AND x.state='ai_queued' AND x.deadline>now() AND v.monitoring AND v.revision=x.mapping_revision))`

// Collection precedes ai_runs creation, so AI configuration saves cannot cancel
// it through that table. Recheck the arrival-time consent revisions in the final
// request-budget statement, including disable/re-enable between scope and HTTP.
// These are snapshot reads, adding no scope/accounting lock-order dependency.
const inboundCollectionDispatchScope = `(runs.inbound_receipt_id IS NULL OR EXISTS(
 SELECT 1 FROM inbound_receipts x
 JOIN ai_policy ap ON ap.workspace_id=x.workspace_id AND ap.enabled AND ap.revision=x.ai_policy_revision
 JOIN automation_policy policy ON policy.workspace_id=x.workspace_id AND policy.revision=x.policy_revision
 JOIN ai_providers p ON p.workspace_id=x.workspace_id AND p.id::text=policy.config->>'investigation_provider_id'
 WHERE x.workspace_id=$1 AND x.id=runs.inbound_receipt_id AND x.state='collecting' AND x.deadline>now()
 AND p.revision=x.provider_revision AND NOT p.deleted AND p.capability='ready' AND (p.config->>'enabled')::boolean))`

type inboundArgs struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"id"`
}

func (inboundArgs) Kind() string { return "inbound_admission" }

type inboundWorker struct {
	river.WorkerDefaults[inboundArgs]
	server *Server
}

func (w *inboundWorker) Work(ctx context.Context, j *river.Job[inboundArgs]) error {
	return w.server.admitInbound(ctx, j.Args)
}
func inboundPolicyReason(p automation.Policy, v Service, b InboundBinding) string {
	switch {
	case !b.Enabled:
		return "Inbound binding is disabled."
	case p.Mode != "auto_investigate" || !p.AutomaticConsent:
		return "Automatic investigations are Off or recurring consent is absent; this receipt will not replay later."
	case !slices.Contains(p.Services, v.ID):
		return "Service is not selected for automatic investigations."
	case !v.Monitoring:
		return "Service monitoring is disabled."
	case b.Origin != v.Origin:
		return "Binding origin does not match the service."
	case v.Origin == "demo" && !p.AllowDemo:
		return "Simulated demo investigations are excluded."
	}
	return ""
}

// AI consent is required even before collecting telemetry for an inbound request.
func inboundAIConsent(ctx context.Context, q aiDB, ws string, v Service, p automation.Policy) (AIProvider, AIPolicy, error) {
	provider, e := loadAIProvider(ctx, q, ws, p.InvestigationProviderID)
	if e != nil {
		return provider, AIPolicy{}, errors.New("Investigation provider unavailable.")
	}
	ap, e := loadAIPolicy(ctx, q, ws)
	if e != nil {
		return provider, ap, e
	}
	if !ap.Enabled || !provider.Enabled || provider.Capability != "ready" || !aiAllowed(provider, v.ID, v.Origin) || ap.RunLimit <= 0 || ap.DailyLimit <= 0 || provider.InputPrice <= 0 || provider.OutputPrice <= 0 {
		return provider, ap, errors.New("AI processing requires a tested provider, service/origin consent and positive budgets/prices.")
	}
	return provider, ap, nil
}

// The receipt snapshots consent at arrival. Settings changes never replay a backlog.
func (s *Server) inboundScope(ctx context.Context, q aiDB, ws, id string) (InboundBinding, Service, automation.Policy, error) {
	var bid, state string
	var br, pr, mr, ar, vr int
	var deadline time.Time
	e := q.QueryRow(ctx, `SELECT binding_id::text,binding_revision,policy_revision,mapping_revision,ai_policy_revision,provider_revision,state,deadline FROM inbound_receipts WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&bid, &br, &pr, &mr, &ar, &vr, &state, &deadline)
	if e != nil {
		return InboundBinding{}, Service{}, automation.Policy{}, e
	}
	b, e := scanBinding(q.QueryRow(ctx, `SELECT `+bindingColumns+` FROM inbound_bindings WHERE workspace_id=$1 AND id=$2 AND NOT deleted`, ws, bid))
	if e != nil {
		return b, Service{}, automation.Policy{}, errors.New("Inbound binding is unavailable.")
	}
	v, e := scanService(q.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id=$2`, ws, b.ServiceID))
	if e != nil {
		return b, v, automation.Policy{}, e
	}
	p, e := loadAutomationPolicy(ctx, q, ws)
	if e != nil {
		return b, v, p, e
	}
	if reason := inboundPolicyReason(p, v, b); reason != "" {
		return b, v, p, errors.New(reason)
	}
	if state == "skipped" || state == "cancelled" || state == "failed" || b.Revision != br || p.Revision != pr || v.Revision != mr || !deadline.After(time.Now()) {
		return b, v, p, errors.New("Inbound scope, consent or admission deadline changed.")
	}
	provider, ap, e := inboundAIConsent(ctx, q, ws, v, p)
	if e != nil {
		return b, v, p, e
	}
	if ap.Revision != ar || provider.Revision != vr {
		return b, v, p, errors.New("AI consent or provider configuration changed; this receipt will not replay.")
	}
	return b, v, p, nil
}
func (s *Server) admitInbound(ctx context.Context, a inboundArgs) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockInboundReceipt(ctx, tx, a.WorkspaceID, a.ID); errors.Is(e, pgx.ErrNoRows) {
		return nil
	} else if e != nil {
		return e
	}
	if e = aiWorkspaceLock(ctx, tx, a.WorkspaceID); e != nil {
		return e
	}
	var state string
	var start, end time.Time
	var exhausted bool
	e = tx.QueryRow(ctx, `SELECT state,window_start,window_end,EXISTS(SELECT 1 FROM river_job j WHERE j.id=x.job_id AND j.state IN ('cancelled','discarded')) FROM inbound_receipts x WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, a.ID).Scan(&state, &start, &end, &exhausted)
	if e != nil {
		return e
	}
	if state != "queued" && state != "collected" {
		return nil
	}
	skip := func(reason string) error {
		_, err := tx.Exec(ctx, `UPDATE inbound_receipts SET state='skipped',reason=$3 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID, reason)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return err
	}
	b, v, p, e := s.inboundScope(ctx, tx, a.WorkspaceID, a.ID)
	if e != nil {
		return skip(e.Error())
	}
	if exhausted {
		return skip("Admission job exhausted; this receipt will not replay.")
	}
	provider, ap, e := inboundAIConsent(ctx, tx, a.WorkspaceID, v, p)
	if e != nil {
		return skip(e.Error())
	}
	var active bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_runs WHERE workspace_id=$1 AND service_id=$2 AND state IN ('queued','analyzing'))`, a.WorkspaceID, v.ID).Scan(&active); e != nil {
		return e
	}
	if active {
		return skip("Another AI task is active for this service.")
	}
	if state == "queued" {
		cfg, e := loadDetectorSettings(ctx, tx, a.WorkspaceID)
		if e != nil {
			return e
		}
		if !inbound.ValidWindow(inbound.Alert{Start: start, End: end}, cfg.BaselineMinutes, time.Now()) {
			return skip("Incident window is stale or unsupported by current comparison settings.")
		}
		var count int
		var next *time.Time
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM inbound_receipts WHERE workspace_id=$1 AND binding_id=$2 AND started_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, a.WorkspaceID, b.ID).Scan(&count); e != nil {
			return e
		}
		if count >= b.DailyLimit {
			return skip("Binding daily investigation limit reached.")
		}
		e = tx.QueryRow(ctx, `SELECT next_investigation_at FROM automation_services WHERE workspace_id=$1 AND service_id=$2`, a.WorkspaceID, v.ID).Scan(&next)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if next != nil && next.After(time.Now()) {
			return skip("Service automatic investigation cooldown is active.")
		}
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE workspace_id=$1 AND service_id=$2 AND kind='investigation' AND state IN ('queued','collecting','evaluating','reporting'))`, a.WorkspaceID, v.ID).Scan(&active); e != nil {
			return e
		}
		// Never attach inbound cancellation semantics to someone else's manual run.
		if active {
			return skip("Another investigation is active for this service.")
		}
		run, e := s.enqueue(ctx, tx, a.WorkspaceID, v, cfg, "investigation", true, start, end)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE runs SET inbound_receipt_id=$3 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, run, a.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE inbound_receipts SET state='collecting',reason='Bounded telemetry investigation queued; external text is not evidence.',started_at=now() WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO automation_services(workspace_id,service_id,next_investigation_at) VALUES($1,$2,now()+$3*interval '1 minute') ON CONFLICT(workspace_id,service_id) DO UPDATE SET next_investigation_at=EXCLUDED.next_investigation_at`, a.WorkspaceID, v.ID, p.CooldownMinutes); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	var run string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM runs WHERE workspace_id=$1 AND inbound_receipt_id=$2 AND state='completed' AND NOT cancelled`, a.WorkspaceID, a.ID).Scan(&run); errors.Is(e, pgx.ErrNoRows) {
		return skip("Deterministic investigation did not complete; no AI processing.")
	} else if e != nil {
		return e
	}
	input, e := s.deepInput(ctx, tx, a.WorkspaceID, run, provider, aiStart{Tools: p.ToolsEnabled}, nil)
	if e != nil {
		return skip("Collected evidence is unavailable or exceeds the bounded AI context.")
	}
	// Keep collection and its factual report when AI admission lacks budget.
	if _, e = tx.Exec(ctx, `SAVEPOINT inbound_ai`); e != nil {
		return e
	}
	id, e := s.insertDeep(ctx, tx, a.WorkspaceID, run, "", provider, ap, input, aiStart{Tools: p.ToolsEnabled})
	if errors.Is(e, errDeepBudget) {
		if _, e = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT inbound_ai`); e != nil {
			return e
		}
		return skip(errDeepBudget.Error())
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE ai_runs SET purpose='automatic',inbound_receipt_id=$3 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id, a.ID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE inbound_receipts SET state='ai_queued',reason='Validated telemetry collected; bounded automatic analysis queued.' WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Called under the run's scope/accounting locks in the same transaction as its
// terminal report and notification event. A crash cannot lose AI continuation.
func (s *Server) finishInbound(ctx context.Context, tx pgx.Tx, a runArgs, receipt, state string) error {
	if receipt == "" {
		return nil
	}
	status, reason := "failed", "Telemetry investigation did not produce an adequate report; no AI processing."
	if state == "completed" {
		status, reason = "collected", "Telemetry report ready; awaiting bounded AI admission."
	}
	if state == "cancelled" {
		status, reason = "cancelled", "Inbound investigation scope or consent changed."
	}
	_, e := tx.Exec(ctx, `UPDATE inbound_receipts SET state=$3,reason=$4 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, receipt, status, reason)
	if e == nil && state == "completed" {
		e = s.queueInbound(ctx, tx, inboundArgs{a.WorkspaceID, receipt})
	}
	return e
}
func (s *Server) retainInbound(ctx context.Context) error {
	// Mark exhausted/expired admission visibly without creating replacement work.
	_, e := s.pool.Exec(ctx, `UPDATE inbound_receipts x SET state='failed',reason='Inbound processing expired, was cancelled or exhausted retries.' WHERE state IN ('queued','collecting','collected') AND (deadline<=now() OR EXISTS(SELECT 1 FROM river_job j WHERE j.id=x.job_id AND j.state IN ('cancelled','discarded')) OR EXISTS(SELECT 1 FROM runs r WHERE r.inbound_receipt_id=x.id AND r.state IN ('cancelled','failed','insufficient_evidence')))`)
	if e != nil {
		return e
	}
	if _, e = s.pool.Exec(ctx, `UPDATE inbound_receipts SET title=NULL WHERE expires_at<=now() AND title IS NOT NULL`); e != nil {
		return e
	}
	return s.pruneLinkedAIHistory(ctx, `DELETE FROM inbound_receipts WHERE id IN (SELECT x.id FROM inbound_receipts x WHERE created_at<now()-interval '30 days' AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.inbound_receipt_id=x.id AND r.state IN ('queued','collecting','evaluating','reporting')) AND NOT EXISTS(SELECT 1 FROM ai_runs a WHERE a.inbound_receipt_id=x.id AND a.state IN ('queued','analyzing')) LIMIT 1000)`)
}
