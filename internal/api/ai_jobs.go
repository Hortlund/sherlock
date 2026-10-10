package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"sherlock/internal/ai"
	"time"
)

type aiArgs struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"id"`
}

func (aiArgs) Kind() string { return "ai_analysis" }

type aiWorker struct {
	river.WorkerDefaults[aiArgs]
	server *Server
}

func (w *aiWorker) Timeout(*river.Job[aiArgs]) time.Duration { return 190 * time.Second }
func (w *aiWorker) Work(ctx context.Context, j *river.Job[aiArgs]) error {
	return w.server.executeAI(ctx, j.Args)
}

// Both analysis and triage must serialize mapping/privacy validation with edits.
// Take scope locks before accounting, and release them before provider HTTP.
func lockAIExecutionScope(ctx context.Context, tx pgx.Tx, ws, service, batch string) error {
	if batch != "" {
		return lockTriageScope(ctx, tx, ws, batch)
	}
	if service == "" {
		return nil // Synthetic capability test.
	}
	return lockWorkspaceScope(ctx, tx, ws)
}

func (s *Server) executeAI(ctx context.Context, a aiArgs) error {
	conn, e := s.pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	var providerID, serviceID, batchID string
	e = conn.QueryRow(ctx, `SELECT provider_id::text,COALESCE(service_id::text,''),COALESCE(triage_batch_id::text,'') FROM ai_runs WHERE workspace_id=$1 AND id::text=$2`, a.WorkspaceID, a.ID).Scan(&providerID, &serviceID, &batchID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var locked bool
	e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,731902))`, providerID).Scan(&locked)
	if e != nil {
		return e
	}
	if !locked {
		return river.JobSnooze(time.Second)
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if _, err := conn.Exec(c, `SELECT pg_advisory_unlock(hashtextextended($1,731902))`, providerID); err != nil {
			_ = conn.Conn().Close(c)
		}
	}()
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockAIExecutionScope(ctx, tx, a.WorkspaceID, serviceID, batchID); e != nil {
		return e
	}
	if e = aiWorkspaceLock(ctx, tx, a.WorkspaceID); e != nil {
		return e
	}
	var state, runID, purpose, model, digest string
	var providerRev, policyRev, inputBound, outputLimit int
	var started *time.Time
	var deadline, expires time.Time
	var payload []byte
	var inputPrice, outputPrice int64
	e = tx.QueryRow(ctx, `SELECT state,COALESCE(run_id::text,''),purpose,model,digest,provider_revision,policy_revision,input_bound,output_limit,started_at,deadline,expires_at,payload,input_price,output_price FROM ai_runs WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, a.ID).Scan(&state, &runID, &purpose, &model, &digest, &providerRev, &policyRev, &inputBound, &outputLimit, &started, &deadline, &expires, &payload, &inputPrice, &outputPrice)
	if e != nil {
		return e
	}
	if state != "queued" && state != "analyzing" {
		return nil
	}
	finishEarly := func(status, reason string) error {
		_, err := tx.Exec(ctx, `UPDATE ai_runs SET state=$3,reason=$4,finished_at=now(),payload=NULL,accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE 'unknown' END WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID, status, reason)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return err
	}
	if purpose == "triage" && batchID == "" {
		return finishEarly("cancelled", "Triage activity is no longer retained.")
	}
	if started != nil {
		return finishEarly("unknown", "Interrupted after dispatch intent; no automatic paid replay. Verify provider usage before reconciling.")
	}
	if !deadline.After(time.Now()) || !expires.After(time.Now()) {
		return finishEarly("cancelled", "AI deadline or evidence retention expired before dispatch.")
	}
	p, e := loadAIProvider(ctx, tx, a.WorkspaceID, providerID)
	if e != nil {
		return finishEarly("cancelled", "Provider unavailable before dispatch.")
	}
	policy, e := loadAIPolicy(ctx, tx, a.WorkspaceID)
	if e != nil {
		return e
	}
	if ai.IsTriage(purpose) {
		p = triageProvider(p)
	}
	if !p.Enabled || !policy.Enabled || p.Revision != providerRev || policy.Revision != policyRev {
		return finishEarly("cancelled", "Provider or workspace policy changed before dispatch.")
	}
	input, e := s.invocationInput(ctx, tx, a.WorkspaceID, runID, batchID, purpose, p)
	if e != nil || input.Digest != digest || payload == nil {
		return finishEarly("cancelled", "Evidence changed, expired, or became unavailable before dispatch.")
	}
	if (purpose == "analysis" || purpose == "triage") && (!aiAllowed(p, input.Service.ID, input.Service.Origin) || p.Capability != "ready") {
		return finishEarly("cancelled", "Service or provider is no longer eligible for AI.")
	}
	var encrypted []byte
	e = tx.QueryRow(ctx, `SELECT secret FROM ai_providers WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, providerID).Scan(&encrypted)
	if e != nil {
		return e
	}
	key, e := s.vault.Open(encrypted, a.WorkspaceID+":ai:"+providerID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE ai_runs SET state='analyzing',started_at=now(),accounting='unknown',reason='Provider request started; acknowledgement pending.' WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		return e
	}
	callCtx, stop := context.WithDeadline(ctx, minTime(deadline, expires))
	defer stop()
	// Poll only while a provider request is in flight; privacy edits also cancel the persisted run.
	watchDone, stopWatch := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-callCtx.Done():
				return
			case <-ticker.C:
				var allowed bool
				err := s.pool.QueryRow(callCtx, `SELECT a.state='analyzing' AND a.expires_at>now() AND (a.service_id IS NULL OR v.revision=$3) FROM ai_runs a LEFT JOIN services v ON v.workspace_id=a.workspace_id AND v.id=a.service_id WHERE a.workspace_id=$1 AND a.id=$2`, a.WorkspaceID, a.ID, input.Service.Revision).Scan(&allowed)
				if err == nil && allowed && purpose == "triage" {
					fresh, checkErr := s.buildTriageInput(callCtx, s.pool, a.WorkspaceID, batchID, p)
					allowed = checkErr == nil && fresh.Digest == digest
				}
				if err != nil || !allowed {
					stop()
					return
				}
			}
		}
	}()
	// A final persisted-state check narrows the interval between revocation and dispatch.
	var allowed bool
	e = conn.QueryRow(callCtx, `SELECT state='analyzing' FROM ai_runs WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID).Scan(&allowed)
	result := ai.Result{State: "cancelled", Reason: "Cancelled before the provider request.", UsageKnown: true}
	if e == nil && allowed {
		// Rebuild from verified evidence so dispatch uses the exact compact bytes
		// measured at admission, not PostgreSQL's JSONB serialization.
		if ai.IsTriage(purpose) {
			result = s.aiClient.Triage(callCtx, p.Kind, model, string(key), input.Payload)
		} else {
			result = s.aiClient.Analyze(callCtx, p.Kind, model, string(key), input.Payload)
		}
	} else if e != nil {
		result = ai.Result{State: "unknown", Reason: "Could not verify dispatch state; reservation retained."}
	}
	close(stopWatch)
	<-watchDone
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finalTx, e := conn.Begin(finishCtx)
	if e != nil {
		return e
	}
	defer finalTx.Rollback(finishCtx)
	if e = lockAIExecutionScope(finishCtx, finalTx, a.WorkspaceID, serviceID, batchID); e != nil {
		return e
	}
	if e = aiWorkspaceLock(finishCtx, finalTx, a.WorkspaceID); e != nil {
		return e
	}
	var current string
	e = finalTx.QueryRow(finishCtx, `SELECT state FROM ai_runs WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, a.ID).Scan(&current)
	if e != nil {
		return e
	}
	var report []byte
	if current != "analyzing" {
		result.State = current
		result.Reason = "Processing cancelled; transmitted requests may still incur charges."
	} else {
		freshP, err := loadAIProvider(finishCtx, finalTx, a.WorkspaceID, providerID)
		if ai.IsTriage(purpose) {
			freshP = triageProvider(freshP)
		}
		freshPolicy, errPolicy := loadAIPolicy(finishCtx, finalTx, a.WorkspaceID)
		valid := err == nil && errPolicy == nil && freshPolicy.Enabled && freshPolicy.Revision == policyRev && freshP.Enabled && freshP.Revision == providerRev
		if valid {
			fresh, err := s.invocationInput(finishCtx, finalTx, a.WorkspaceID, runID, batchID, purpose, freshP)
			valid = err == nil && fresh.Digest == digest && fresh.Expires.After(time.Now()) && (purpose == "test" || purpose == "triage_test" || aiAllowed(freshP, input.Service.ID, input.Service.Origin))
		}
		if !valid {
			result.State = "cancelled"
			result.Reason = "Evidence or release policy changed; generated content withheld."
		} else if result.State == "completed" {
			var doc any
			var err error
			if ai.IsTriage(purpose) {
				doc, err = ai.ValidateTriage(result.Text, input.IDs, input.Service.Mapping.SensitivePatterns)
			} else {
				doc, err = ai.Validate(result.Text, input.IDs, input.Service.Mapping.SensitivePatterns)
			}
			if err != nil {
				result.State = "fallback"
				result.Reason = err.Error()
			} else {
				report, _ = json.Marshal(doc)
				result.Reason = "AI suggestions validated for structure and evidence scope; causal claims still require review."
				if ai.IsTriage(purpose) {
					result.Reason = "Advisory assessment validated for structure and evidence scope; investigations remain manual."
				}
			}
		}
	}
	if result.UsageKnown && (result.Input > int64(inputBound) || result.Output > int64(outputLimit)) {
		report = nil
		if current == "analyzing" {
			result.State = "fallback"
		}
		result.Reason = "Provider usage exceeded the admission estimate/ceiling. Recheck this model configuration before further use."
		_, e = finalTx.Exec(finishCtx, `UPDATE ai_providers SET capability=CASE WHEN $4 THEN capability ELSE 'failed' END,triage_capability=CASE WHEN $4 THEN 'failed' ELSE triage_capability END WHERE workspace_id=$1 AND id=$2 AND revision=$3`, a.WorkspaceID, providerID, providerRev, ai.IsTriage(purpose))
		if e != nil {
			return e
		}
	}
	accounting := "unknown"
	var charged int64
	var inTokens, outTokens any
	if result.UsageKnown {
		accounting = "settled"
		charged = ai.Cost(result.Input, result.Output, inputPrice, outputPrice)
		inTokens = result.Input
		outTokens = result.Output
	}
	_, e = finalTx.Exec(finishCtx, `UPDATE ai_runs SET state=$3,reason=$4,report=$5,payload=NULL,finished_at=now(),accounting=$6,charged=$7,input_tokens=$8,output_tokens=$9,request_id=$10 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID, result.State, result.Reason, report, accounting, charged, inTokens, outTokens, result.RequestID)
	if e == nil && (purpose == "test" || purpose == "triage_test") {
		capability := "failed"
		if result.State == "completed" {
			capability = "ready"
		}
		_, e = finalTx.Exec(finishCtx, `UPDATE ai_providers SET capability=CASE WHEN $5 THEN capability ELSE $4 END,triage_capability=CASE WHEN $5 THEN $4 ELSE triage_capability END WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND NOT deleted`, a.WorkspaceID, providerID, providerRev, capability, purpose == "triage_test")
	}
	if e == nil {
		e = finalTx.Commit(finishCtx)
	}
	return e
}
func (s *Server) retainAI(ctx context.Context) error {
	_, e := s.pool.Exec(ctx, `WITH expired AS(SELECT id FROM ai_runs WHERE expires_at<=now() AND (payload IS NOT NULL OR report IS NOT NULL) LIMIT 1000 FOR UPDATE SKIP LOCKED) UPDATE ai_runs a SET payload=NULL,report=NULL,state=CASE WHEN state IN ('queued','analyzing') THEN 'cancelled' ELSE state END,accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE accounting END,reason=CASE WHEN state IN ('queued','analyzing') THEN 'Evidence retention expired.' ELSE reason END,finished_at=COALESCE(finished_at,now()) FROM expired WHERE a.id=expired.id`)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `UPDATE ai_runs SET state=CASE WHEN started_at IS NULL THEN 'failed' ELSE 'unknown' END,accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE 'unknown' END,reason='Execution deadline passed; interrupted calls are not replayed.',finished_at=now(),payload=NULL WHERE state IN ('queued','analyzing') AND deadline<now()-interval '30 seconds'`)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `DELETE FROM ai_runs WHERE id IN (SELECT id FROM ai_runs WHERE created_at<now()-interval '30 days' AND accounting IN ('settled','reconciled') LIMIT 1000)`)
	return e
}
