package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"sherlock/internal/ai"
	"sherlock/internal/connectors"
	"sherlock/internal/detectors"
)

type deepRun struct {
	Provider, Service, Run, Batch, Purpose, State, Digest string
	Parent, Question, Receipt                             string
	Tools                                                 bool
	ProviderRevision, PolicyRevision                      int
	Evidence                                              []string
	Deadline, Expires                                     time.Time
}

func loadDeep(ctx context.Context, q aiDB, a deepArgs) (deepRun, error) {
	var r deepRun
	e := q.QueryRow(ctx, `SELECT provider_id::text,service_id::text,run_id::text,COALESCE(automation_batch_id::text,''),purpose,state,digest,COALESCE(parent_ai_run_id::text,''),COALESCE(question,''),tools_enabled,provider_revision,policy_revision,evidence_ids,deadline,expires_at,COALESCE(inbound_receipt_id::text,'') FROM ai_runs WHERE workspace_id=$1 AND id::text=$2 AND deep`, a.WorkspaceID, a.ID).Scan(&r.Provider, &r.Service, &r.Run, &r.Batch, &r.Purpose, &r.State, &r.Digest, &r.Parent, &r.Question, &r.Tools, &r.ProviderRevision, &r.PolicyRevision, &r.Evidence, &r.Deadline, &r.Expires, &r.Receipt)
	return r, e
}
func (s *Server) checkDeep(ctx context.Context, q aiDB, a deepArgs, r deepRun) (AIProvider, AIPolicy, aiInput, error) {
	bad := errors.New("Scope, evidence, consent or execution deadline changed; processing stopped.")
	p, e := loadAIProvider(ctx, q, a.WorkspaceID, r.Provider)
	if e != nil {
		return p, AIPolicy{}, aiInput{}, bad
	}
	ap, e := loadAIPolicy(ctx, q, a.WorkspaceID)
	if e != nil {
		return p, ap, aiInput{}, e
	}
	if (r.State != "queued" && r.State != "analyzing") || !ap.Enabled || ap.Revision != r.PolicyRevision || p.Revision != r.ProviderRevision || !p.Enabled || p.Capability != "ready" || !r.Deadline.After(time.Now()) || !r.Expires.After(time.Now()) {
		return p, ap, aiInput{}, bad
	}
	if r.Purpose == "automatic" {
		if r.Receipt != "" {
			_, _, policy, err := s.inboundScope(ctx, q, a.WorkspaceID, r.Receipt)
			if err != nil || policy.InvestigationProviderID != p.ID || (r.Tools && !policy.ToolsEnabled) {
				return p, ap, aiInput{}, bad
			}
		} else {
			policy, err := loadAutomationPolicy(ctx, q, a.WorkspaceID)
			if err != nil || !policy.ToolsEnabled || r.Batch == "" {
				return p, ap, aiInput{}, bad
			}
			if _, e = s.buildRecommendationInput(ctx, q, a.WorkspaceID, r.Batch, p, true); e != nil {
				return p, ap, aiInput{}, bad
			}
		}
	}
	in := aiStart{Tools: r.Tools, Parent: r.Parent, Question: r.Question}
	base, e := s.deepInput(ctx, q, a.WorkspaceID, r.Run, p, in, nil)
	if e != nil || base.Digest != r.Digest || !aiAllowed(p, r.Service, base.Service.Origin) {
		return p, ap, base, bad
	}
	input, e := s.deepInput(ctx, q, a.WorkspaceID, r.Run, p, in, r.Evidence)
	return p, ap, input, e
}

// A step's intent is never replayed. Its accepted result and next job commit
// together; redelivering any accepted step is a no-op on every replica.
func (s *Server) executeDeep(ctx context.Context, a deepArgs) error {
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	r, e := loadDeep(ctx, conn, a)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var held bool
	// All AI engines share the same provider semaphore. Tool jobs also take it;
	// they live on a separate queue and never block a telemetry scan worker.
	e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,731902))`, r.Provider).Scan(&held)
	if e != nil {
		return e
	}
	if !held {
		return river.JobSnooze(time.Second)
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if _, err := conn.Exec(c, `SELECT pg_advisory_unlock(hashtextextended($1,731902))`, r.Provider); err != nil {
			_ = conn.Conn().Close(c)
		}
	}()
	begin := func(c context.Context) (pgx.Tx, error) {
		tx, err := conn.Begin(c)
		if err != nil {
			return nil, err
		}
		if r.Receipt != "" {
			err = lockInboundReceipt(c, tx, a.WorkspaceID, r.Receipt)
		} else {
			err = lockAIExecutionScope(c, tx, a.WorkspaceID, r.Service, r.Batch)
		}
		if err == nil {
			err = aiWorkspaceLock(c, tx, a.WorkspaceID)
		}
		if err != nil {
			_ = tx.Rollback(c)
			return nil, err
		}
		if _, err = tx.Exec(c, `SELECT id FROM ai_runs WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, a.ID); err != nil {
			_ = tx.Rollback(c)
			return nil, err
		}
		return tx, nil
	}
	tx, e := begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	r, e = loadDeep(ctx, tx, a)
	if e != nil {
		return e
	}
	var kind, state string
	var payload []byte
	var output, bound int
	e = tx.QueryRow(ctx, `SELECT kind,state,payload,output_limit,input_bound FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2 AND sequence=$3 FOR UPDATE`, a.WorkspaceID, a.ID, a.Sequence).Scan(&kind, &state, &payload, &output, &bound)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if state != "queued" && state != "running" {
		return nil
	}
	if r.State != "queued" && r.State != "analyzing" {
		return nil
	}
	stopEarly := func(state, reason string) error {
		if err := finishDeep(ctx, tx, a, state, reason, nil); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if state == "running" {
		return stopEarly("unknown", "Interrupted after step intent; provider or telemetry calls are not automatically replayed.")
	}
	p, ap, input, e := s.checkDeep(ctx, tx, a, r)
	if e != nil {
		return stopEarly("cancelled", e.Error())
	}
	if kind == "provider" {
		input, e = s.deepProviderContext(ctx, tx, a.WorkspaceID, a.ID, a.Sequence, p, input)
		if e != nil {
			return stopEarly("budget_exhausted", e.Error())
		}
		var same bool
		if e = tx.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, payload, input.Payload).Scan(&same); e != nil {
			return e
		}
		if !same {
			return stopEarly("cancelled", "Admitted evidence context changed before dispatch.")
		}
	}
	var key []byte
	if kind == "provider" {
		var encrypted []byte
		if e = tx.QueryRow(ctx, `SELECT secret FROM ai_providers WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, p.ID).Scan(&encrypted); e != nil {
			return e
		}
		key, e = s.vault.Open(encrypted, a.WorkspaceID+":ai:"+p.ID)
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE ai_steps SET state='running',started_at=now(),accounting=CASE WHEN kind='provider' THEN 'unknown' ELSE 'settled' END,reason='Dispatch intent persisted.' WHERE workspace_id=$1 AND ai_run_id=$2 AND sequence=$3`, a.WorkspaceID, a.ID, a.Sequence)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE ai_runs SET state='analyzing',started_at=COALESCE(started_at,now()),accounting=CASE WHEN $3='provider' THEN 'unknown' ELSE accounting END,reason=CASE WHEN $3='provider' THEN 'Analyzing bounded evidence.' ELSE 'Collecting with approved read-only tool.' END WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID, kind)
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	callCtx, cancel := context.WithDeadline(ctx, minTime(r.Deadline, r.Expires))
	defer cancel()
	done := make(chan struct{})
	watchCtx, stopWatch := context.WithCancel(callCtx)
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				fresh, err := loadDeep(watchCtx, s.pool, a)
				if err == nil {
					_, _, _, err = s.checkDeep(watchCtx, s.pool, a, fresh)
				}
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	// Final check is shared with cancellation and policy saves at the same boundary
	// as the single-call engine. No transaction is held across network IO.
	var allowed bool
	e = conn.QueryRow(callCtx, `SELECT state='analyzing' AND NOT cancel_requested AND deadline>now() AND `+inboundAIDispatchScope+` FROM ai_runs WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID).Scan(&allowed)
	result := ai.Result{State: "cancelled", Reason: "Stopped before dispatch.", UsageKnown: true}
	var collected []deepEvidence
	var toolErr error
	if e == nil && allowed {
		if kind == "provider" {
			result = s.aiClient.Explore(callCtx, p.Kind, p.Model, string(key), input.Payload, output)
		} else {
			collected, toolErr = s.collectDeep(callCtx, a, r, input.Service, kind)
			result = ai.Result{State: "completed", UsageKnown: true}
			if toolErr != nil {
				result.State = "insufficient_evidence"
				result.Reason = "Approved collection failed or exceeded its limits; deterministic report remains available."
			}
		}
	}
	stopWatch()
	<-done
	finishCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	ft, e := begin(finishCtx)
	if e != nil {
		return e
	}
	defer ft.Rollback(finishCtx)
	fresh, e := loadDeep(finishCtx, ft, a)
	if e != nil {
		return e
	}
	_, _, _, validation := s.checkDeep(finishCtx, ft, a, fresh)
	accounting := "unknown"
	var charged int64
	var inTokens, outTokens any
	if result.UsageKnown {
		accounting = "settled"
		charged = ai.Cost(result.Input, result.Output, p.InputPrice, p.OutputPrice)
		inTokens = result.Input
		outTokens = result.Output
	}
	_, e = ft.Exec(finishCtx, `UPDATE ai_steps SET state=$4,accounting=$5,charged=$6,input_tokens=$7,output_tokens=$8,request_id=$9,reason=$10,finished_at=now(),payload=NULL WHERE workspace_id=$1 AND ai_run_id=$2 AND sequence=$3`, a.WorkspaceID, a.ID, a.Sequence, stepResultState(result.State), accounting, charged, inTokens, outTokens, result.RequestID, result.Reason)
	if e != nil {
		return e
	}
	finish := func(state, reason string, report []byte) error {
		if err := finishDeep(finishCtx, ft, a, state, reason, report); err != nil {
			return err
		}
		return ft.Commit(finishCtx)
	}
	if validation != nil || fresh.State != "analyzing" {
		return finish("cancelled", "Scope or consent changed; step output withheld and incurred usage retained.", nil)
	}
	if result.Input > int64(bound) && kind == "provider" || result.Output > int64(output) && kind == "provider" {
		if _, e = ft.Exec(finishCtx, `UPDATE ai_providers SET capability='failed' WHERE workspace_id=$1 AND id=$2 AND revision=$3`, a.WorkspaceID, p.ID, p.Revision); e != nil {
			return e
		}
		return finish("fallback", "Provider exceeded its admitted token ceiling; retest this model.", nil)
	}
	if result.State != "completed" {
		return finish(result.State, result.Reason, nil)
	}
	if kind == "provider" {
		action, err := ai.ValidateAction(result.Text, input.IDs, input.Service.Mapping.SensitivePatterns, r.Tools)
		if err != nil {
			return finish("fallback", err.Error(), nil)
		}
		accepted, _ := json.Marshal(action)
		if _, e = ft.Exec(finishCtx, `UPDATE ai_steps SET result=$4,reason='Validated response accepted.' WHERE workspace_id=$1 AND ai_run_id=$2 AND sequence=$3`, a.WorkspaceID, a.ID, a.Sequence, accepted); e != nil {
			return e
		}
		if action.Action == "report" {
			doc, _ := json.Marshal(action.Report)
			return finish("completed", "Validated report; hypotheses still require independent review.", doc)
		}
		e = s.queueDeepTool(finishCtx, ft, a, action.Tool, input.Service)
	} else {
		ids := []string{}
		for _, ev := range collected {
			raw, _ := json.Marshal(ev.Observation)
			var id string
			e = ft.QueryRow(finishCtx, `INSERT INTO evidence(workspace_id,service_id,connection_id,origin,mapping_revision,kind,window_start,window_end,document,expires_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10 WHERE EXISTS(SELECT 1 FROM connections WHERE workspace_id=$1 AND id=$3 AND revision=$11 AND enabled AND NOT deleted) RETURNING id::text`, a.WorkspaceID, r.Service, ev.Connection, input.Service.Origin, input.Service.Revision, ev.Observation.Kind, ev.Observation.Start, ev.Observation.End, raw, r.Expires, ev.Revision).Scan(&id)
			if errors.Is(e, pgx.ErrNoRows) {
				return finish("cancelled", "Source changed during collection; result withheld.", nil)
			}
			if e != nil {
				return e
			}
			ids = append(ids, id)
		}
		// Preserve successful collection citations even if no further paid
		// step fits the remaining context or budget.
		if _, e = ft.Exec(finishCtx, `UPDATE ai_steps SET evidence_ids=$4 WHERE workspace_id=$1 AND ai_run_id=$2 AND sequence=$3`, a.WorkspaceID, a.ID, a.Sequence, ids); e != nil {
			return e
		}
		updated, err := s.deepInput(finishCtx, ft, a.WorkspaceID, r.Run, p, aiStart{Tools: r.Tools, Parent: r.Parent, Question: r.Question}, append(r.Evidence, ids...))
		if err != nil {
			return finish("budget_exhausted", "Collected context is unavailable or exceeds the input ceiling.", nil)
		}
		var parsed aiPayload
		_ = json.Unmarshal(updated.Payload, &parsed)
		facts := []aiFact{}
		for _, f := range parsed.Evidence {
			for _, id := range ids {
				if f.ID == id {
					facts = append(facts, f)
				}
			}
		}
		if kind == "evidence" {
			facts = parsed.Evidence
			ids = updated.IDs
		}
		sanitized, _ := json.Marshal(facts)
		if len(sanitized) > 8192 {
			return finish("budget_exhausted", "Tool output exceeds 8 KiB; no additional provider call.", nil)
		}
		if _, e = ft.Exec(finishCtx, `UPDATE ai_steps SET evidence_ids=$4,result=$5,reason='Scoped evidence persisted.' WHERE workspace_id=$1 AND ai_run_id=$2 AND sequence=$3`, a.WorkspaceID, a.ID, a.Sequence, ids, sanitized); e != nil {
			return e
		}
		e = s.queueDeepProvider(finishCtx, ft, a.WorkspaceID, a.ID, p, ap, updated)
	}
	if errors.Is(e, errDeepBudget) {
		return finish("budget_exhausted", e.Error(), nil)
	}
	if e != nil {
		return e
	}
	// Known usage remains charged while the next reservation is outstanding.
	if e = aggregateDeep(finishCtx, ft, a); e != nil {
		return e
	}
	return ft.Commit(finishCtx)
}

func stepResultState(state string) string {
	if state == "completed" {
		return "completed"
	}
	if state == "unknown" {
		return "unknown"
	}
	if state == "cancelled" {
		return "cancelled"
	}
	return "failed"
}
func aggregateDeep(ctx context.Context, tx pgx.Tx, a deepArgs) error {
	_, e := tx.Exec(ctx, `UPDATE ai_runs SET charged=(SELECT COALESCE(sum(charged),0) FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2),input_tokens=(SELECT COALESCE(sum(input_tokens),0) FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2),output_tokens=(SELECT COALESCE(sum(output_tokens),0) FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2) WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID)
	return e
}
func finishDeep(ctx context.Context, tx pgx.Tx, a deepArgs, state, reason string, report []byte) error {
	_, e := tx.Exec(ctx, `UPDATE ai_steps SET state=CASE WHEN state='running' THEN 'unknown' ELSE 'cancelled' END,accounting=CASE WHEN state='queued' THEN 'settled' ELSE accounting END,reason=$3,payload=NULL,finished_at=now() WHERE workspace_id=$1 AND ai_run_id=$2 AND state IN ('queued','running')`, a.WorkspaceID, a.ID, reason)
	if e != nil {
		return e
	}
	if e = aggregateDeep(ctx, tx, a); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE ai_runs SET state=$3,reason=$4,report=$5,payload=NULL,finished_at=now(),accounting=CASE WHEN EXISTS(SELECT 1 FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2 AND accounting='unknown') THEN 'unknown' ELSE 'settled' END WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID, state, reason, report)
	return e
}
func (s *Server) queueDeepTool(ctx context.Context, tx pgx.Tx, a deepArgs, kind string, v Service) error {
	limit := 0
	switch kind {
	case "prometheus_counts":
		if v.PrometheusID == "" {
			return errDeepBudget
		}
		limit = 8
	case "loki_patterns":
		if v.LokiID == "" {
			return errDeepBudget
		}
		limit = 4
	case "evidence":
	default:
		return errDeepBudget
	}
	var count, requests int
	if e := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(telemetry_limit),0) FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2 AND kind<>'provider'`, a.WorkspaceID, a.ID).Scan(&count, &requests); e != nil {
		return e
	}
	if count >= deepTools || requests+limit > deepHTTP {
		return errDeepBudget
	}
	_, e := tx.Exec(ctx, `INSERT INTO ai_steps(workspace_id,ai_run_id,sequence,kind,telemetry_limit,accounting) VALUES($1,$2,$3,$4,$5,'settled')`, a.WorkspaceID, a.ID, a.Sequence+1, kind, limit)
	if e != nil {
		return e
	}
	return s.queueDeepJob(ctx, tx, deepArgs{a.WorkspaceID, a.ID, a.Sequence + 1}, true)
}

type deepEvidence struct {
	Connection  string
	Revision    int
	Observation connectors.Observation
}

func (s *Server) collectDeep(ctx context.Context, a deepArgs, r deepRun, v Service, kind string) ([]deepEvidence, error) {
	if kind == "evidence" {
		return nil, nil
	}
	var start, end time.Time
	var raw []byte
	e := s.pool.QueryRow(ctx, `SELECT window_start,window_end,config FROM runs WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, r.Run).Scan(&start, &end, &raw)
	if e != nil {
		return nil, e
	}
	var cfg detectors.Config
	if e = json.Unmarshal(raw, &cfg); e != nil {
		return nil, e
	}
	sourceID := v.PrometheusID
	if kind == "loki_patterns" {
		sourceID = v.LokiID
	}
	source, c, e := s.source(ctx, a.WorkspaceID, sourceID)
	if e != nil {
		return nil, e
	}
	if c.Origin != v.Origin || kind == "prometheus_counts" && c.Kind != "prometheus" || kind == "loki_patterns" && c.Kind != "loki" {
		return nil, errors.New("invalid mapped source")
	}
	attempts := 0
	limit := 8
	if kind == "loki_patterns" {
		limit = 4
	}
	ctx = connectors.WithRequestGuard(ctx, func(guardCtx context.Context) error {
		if attempts >= limit {
			return errDeepBudget
		}
		fresh, err := loadDeep(guardCtx, s.pool, a)
		if err == nil {
			_, _, _, err = s.checkDeep(guardCtx, s.pool, a, fresh)
		}
		if err != nil {
			return err
		}
		// Every underlying attempt, including the collector's retry, consumes budget.
		tag, err := s.pool.Exec(guardCtx, `UPDATE ai_runs SET telemetry_attempts=telemetry_attempts+1 WHERE workspace_id=$1 AND id=$2 AND state='analyzing' AND NOT cancel_requested AND deadline>now() AND telemetry_attempts<12 AND `+inboundAIDispatchScope+` AND EXISTS(SELECT 1 FROM connections WHERE workspace_id=$1 AND id=$3 AND revision=$4 AND enabled AND NOT deleted)`, a.WorkspaceID, a.ID, sourceID, c.Revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errDeepBudget
		}
		attempts++
		return nil
	})
	result := []deepEvidence{}
	for _, window := range [][2]time.Time{{start, end}, {start.Add(-time.Duration(cfg.BaselineMinutes) * time.Minute), start}} {
		if kind == "loki_patterns" {
			o, err := s.policy.Logs(ctx, source, v.Mapping, window[0], window[1])
			if err != nil {
				return nil, err
			}
			result = append(result, deepEvidence{sourceID, c.Revision, o})
		} else {
			for _, errorsOnly := range []bool{false, true} {
				o, err := s.policy.Count(ctx, source, v.Mapping, window[0], window[1], errorsOnly)
				if err != nil {
					return nil, err
				}
				result = append(result, deepEvidence{sourceID, c.Revision, o})
			}
		}
	}
	return result, nil
}
