package api

import (
	"context"
	"encoding/json"
	"errors"
	"sherlock/internal/automation"
	"sherlock/internal/detectors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type automationArgs struct {
	WorkspaceID string `json:"workspace_id"`
	BatchID     string `json:"batch_id"`
}

func (automationArgs) Kind() string { return "automation" }

type automationWorker struct {
	river.WorkerDefaults[automationArgs]
	server *Server
}

func (w *automationWorker) Work(ctx context.Context, j *river.Job[automationArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return w.server.evaluateAutomation(ctx, j.Args)
}

// Called with the service/finding locks held by the finding publication transaction.
// No external calls or investigation jobs are made by the recommendation pipeline.
func (s *Server) automationObservation(ctx context.Context, tx pgx.Tx, ws string, v Service, finding string, isNew bool, at time.Time, c detectors.Candidate) error {
	m, valid := automation.Measure(c)
	if !valid {
		return nil
	}
	p, e := loadAutomationPolicy(ctx, tx, ws)
	if e != nil {
		return e
	}
	var raw []byte
	var before automation.Measurement
	var checkpointRevision int
	var checkpointAt time.Time
	e = tx.QueryRow(ctx, `SELECT measurement,policy_revision,observed_at FROM automation_checkpoints WHERE workspace_id=$1 AND finding_id=$2`, ws, finding).Scan(&raw, &checkpointRevision, &checkpointAt)
	if errors.Is(e, pgx.ErrNoRows) {
		raw, _ = json.Marshal(m)
		if _, e = tx.Exec(ctx, `INSERT INTO automation_checkpoints(workspace_id,finding_id,measurement,observed_at,policy_revision) VALUES($1,$2,$3,$4,$5)`, ws, finding, raw, at, p.Revision); e != nil {
			return e
		}
		// Installing/enabling recommendations must not replay existing findings.
		if !isNew {
			return nil
		}
		before = m
	} else if e != nil {
		return e
	} else if e = json.Unmarshal(raw, &before); e != nil {
		return e
	}
	// A completed skip may include a newer healthy scan than this delayed candidate.
	if !isNew && !at.After(checkpointAt) {
		return nil
	}
	// Policy changes start a fresh comparison baseline on the next observation.
	// This prevents re-enabling/reconfiguring from replaying accumulated growth.
	if !isNew && checkpointRevision != p.Revision {
		raw, _ = json.Marshal(m)
		_, e = tx.Exec(ctx, `UPDATE automation_checkpoints SET measurement=$3,observed_at=$4,policy_revision=$5 WHERE workspace_id=$1 AND finding_id=$2`, ws, finding, raw, at, p.Revision)
		return e
	}
	trigger := automation.Change(before, m, p.GrowthMultiplier)
	if isNew {
		trigger = "new_occurrence"
	}
	measurement, _ := json.Marshal(m)
	ids := c.EvidenceIDs
	if ids == nil {
		ids = []string{}
	}
	evidence, _ := json.Marshal(ids)
	// Keep a pending item's latest comparable observation even if a burst subsides.
	tag, e := tx.Exec(ctx, `UPDATE automation_items i SET measurement=$3,observed_at=$4,evidence_ids=$5,cleared_at=CASE WHEN cleared_at<$4 THEN NULL ELSE cleared_at END,trigger=CASE WHEN i.is_new THEN 'new_occurrence' ELSE $6 END
 FROM automation_batches b WHERE i.workspace_id=$1 AND i.finding_id=$2 AND b.workspace_id=i.workspace_id AND b.id=i.batch_id AND b.state='pending' AND b.policy_revision=$7 AND b.mapping_revision=$8`, ws, finding, measurement, at, evidence, trigger, p.Revision, v.Revision)
	if e != nil || tag.RowsAffected() > 0 || trigger == "" {
		return e
	}
	var batch string
	var due time.Time
	e = tx.QueryRow(ctx, `SELECT id::text,due_at FROM automation_batches WHERE workspace_id=$1 AND service_id=$2 AND policy_revision=$3 AND mapping_revision=$4 AND state='pending'`, ws, v.ID, p.Revision, v.Revision).Scan(&batch, &due)
	if errors.Is(e, pgx.ErrNoRows) {
		if e = tx.QueryRow(ctx, `INSERT INTO automation_batches(workspace_id,service_id,policy_revision,mapping_revision) VALUES($1,$2,$3,$4) RETURNING id::text,due_at`, ws, v.ID, p.Revision, v.Revision).Scan(&batch, &due); e != nil {
			return e
		}
		job, err := s.jobs.InsertTx(ctx, tx, automationArgs{ws, batch}, &river.InsertOpts{Queue: "automation", MaxAttempts: 10, ScheduledAt: due})
		if err != nil {
			return err
		}
		if _, e = tx.Exec(ctx, `UPDATE automation_batches SET job_id=$2 WHERE id=$1`, batch, job.Job.ID); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO automation_items(workspace_id,batch_id,finding_id,is_new,measurement,observed_at,trigger,evidence_ids) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, ws, batch, finding, isNew, measurement, at, trigger, evidence)
	return e
}

// A complete scan can disqualify an earlier candidate without emitting a new one.
// Called under the same service lock/transaction as finding publication. Missing
// coverage is never treated as recovery, and older scans cannot clear newer items.
func (s *Server) automationClearMissing(ctx context.Context, tx pgx.Tx, ws string, v Service, at time.Time, candidates []detectors.Candidate, coverage []detectors.Coverage) error {
	active := []string{}
	for _, c := range coverage {
		if c.Status == "active" {
			active = append(active, c.Detector)
		}
	}
	if len(active) == 0 {
		return nil
	}
	if candidates == nil {
		candidates = []detectors.Candidate{}
	}
	raw, e := json.Marshal(candidates)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE automation_items i SET cleared_at=$4
 FROM automation_batches b,findings f
 WHERE b.id=i.batch_id AND b.workspace_id=i.workspace_id AND b.state='pending'
 AND f.id=i.finding_id AND f.workspace_id=i.workspace_id
 AND b.workspace_id=$1 AND b.service_id=$2 AND b.mapping_revision=$3
 AND i.observed_at<$4 AND (i.cleared_at IS NULL OR i.cleared_at<$4)
 AND f.detector=ANY($5)
 AND NOT EXISTS(SELECT 1 FROM jsonb_to_recordset($6::jsonb) AS candidate(detector text,signal text) WHERE candidate.detector=f.detector AND candidate.signal=f.signal)`, ws, v.ID, v.Revision, at, active, raw)
	return e
}

func (s *Server) evaluateAutomation(ctx context.Context, a automationArgs) error {
	// Service first, matching persistFindings; then batch, policy and findings.
	var service string
	e := s.pool.QueryRow(ctx, `SELECT service_id::text FROM automation_batches WHERE workspace_id=$1 AND id::text=$2`, a.WorkspaceID, a.BatchID).Scan(&service)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var monitoring bool
	var mapping int
	var origin string
	if e = tx.QueryRow(ctx, `SELECT monitoring,revision,origin FROM services WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, service).Scan(&monitoring, &mapping, &origin); e != nil {
		return e
	}
	var state string
	var exhausted bool
	var revision, snapshotMapping int
	var due, created, now time.Time
	if e = tx.QueryRow(ctx, `SELECT state,policy_revision,mapping_revision,due_at,created_at,clock_timestamp(),EXISTS(SELECT 1 FROM river_job j WHERE j.id=automation_batches.job_id AND j.state IN ('discarded','cancelled')) FROM automation_batches WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, a.BatchID).Scan(&state, &revision, &snapshotMapping, &due, &created, &now, &exhausted); e != nil {
		return e
	}
	if state != "pending" {
		return nil
	}
	if now.Before(due) {
		return river.JobSnooze(due.Sub(now))
	}
	// Serialize evaluation with policy edits; saved edits invalidate pending versions.
	if _, e = tx.Exec(ctx, `SELECT workspace_id FROM automation_policy WHERE workspace_id=$1 FOR SHARE`, a.WorkspaceID); e != nil {
		return e
	}
	p, e := loadAutomationPolicy(ctx, tx, a.WorkspaceID)
	if e != nil {
		return e
	}
	// Hold a shared settings lock until the decision commits. Settings writes use
	// the exclusive form, so a completed disable/edit cannot race this publication.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,731903))`, a.WorkspaceID); e != nil {
		return e
	}
	config, e := loadDetectorSettings(ctx, tx, a.WorkspaceID)
	if e != nil {
		return e
	}
	blocked := ""
	switch {
	case p.Revision != revision:
		blocked = "Settings changed after this batch was queued; waiting for a new meaningful change."
	case p.Mode == "off":
		blocked = "Recommendations are off."
	case !slices.Contains(p.Services, service):
		blocked = "Service is not selected for recommendations."
	case origin == "demo" && !p.AllowDemo:
		blocked = "Simulated demo findings are excluded."
	case !monitoring:
		blocked = "Scheduled monitoring is disabled for this service."
	case mapping != snapshotMapping:
		blocked = "Service mapping changed; this observation is no longer comparable."
	case exhausted:
		blocked = "Evaluation exhausted its retries; waiting for a new meaningful change."
	case now.Sub(created) > 15*time.Minute:
		blocked = "This batch expired before evaluation; old work is not replayed."
	}
	var next *time.Time
	e = tx.QueryRow(ctx, `SELECT next_eligible_at FROM automation_services WHERE workspace_id=$1 AND service_id=$2`, a.WorkspaceID, service).Scan(&next)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT i.finding_id::text,i.is_new,i.measurement,i.observed_at,i.evidence_ids,f.state,c.measurement,c.observed_at,f.detector,i.cleared_at
 FROM automation_items i JOIN findings f ON f.workspace_id=i.workspace_id AND f.id=i.finding_id JOIN automation_checkpoints c ON c.workspace_id=i.workspace_id AND c.finding_id=i.finding_id
 WHERE i.workspace_id=$1 AND i.batch_id=$2 ORDER BY i.finding_id FOR UPDATE OF f`, a.WorkspaceID, a.BatchID)
	if e != nil {
		return e
	}
	type item struct {
		id                  string
		fresh               bool
		measure, checkpoint automation.Measurement
		at, checkpointAt    time.Time
		ids                 []string
		state               string
		detector            string
		clearedAt           *time.Time
	}
	items := []item{}
	for rows.Next() {
		var i item
		var m, c, ids []byte
		if e = rows.Scan(&i.id, &i.fresh, &m, &i.at, &ids, &i.state, &c, &i.checkpointAt, &i.detector, &i.clearedAt); e != nil {
			break
		}
		if e = json.Unmarshal(m, &i.measure); e != nil {
			break
		}
		if e = json.Unmarshal(c, &i.checkpoint); e != nil {
			break
		}
		if e = json.Unmarshal(ids, &i.ids); e != nil {
			break
		}
		items = append(items, i)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	recommended := false
	for _, i := range items {
		trigger := automation.Change(i.checkpoint, i.measure, p.GrowthMultiplier)
		if i.fresh {
			trigger = "new_occurrence"
		}
		currentMeasurement, _ := automation.Measure(detectors.Candidate{Detector: i.detector, Thresholds: config})
		reason, rec := blocked, "observe"
		if reason == "" {
			switch {
			case i.state != "new" && i.state != "acknowledged":
				reason = "Finding is snoozed, dismissed or resolved."
			case now.Sub(i.at) > 15*time.Minute:
				reason = "Scan observation is older than 15 minutes; old work is not replayed."
			case slices.Contains(config.Disabled, i.detector):
				reason = "Detector is disabled in Monitoring settings."
			case i.measure.Key != currentMeasurement.Key:
				reason = "Monitoring windows or thresholds changed after this observation; a fresh comparison is required."
				rec = "need_more_data"
			case i.clearedAt != nil:
				reason = "A newer complete scan no longer qualifies for this detector signal."
			case i.at.Before(i.checkpointAt):
				reason = "A newer observation has already been evaluated."
			case trigger == "comparison_changed":
				reason = "Measurement windows or thresholds changed; comparison baseline reset."
				rec = "need_more_data"
			case trigger == "":
				reason = "Latest observation no longer meets the material-change gate."
			default:
				var available int
				if e = tx.QueryRow(ctx, `SELECT count(*) FROM evidence WHERE workspace_id=$1 AND service_id=$2 AND mapping_revision=$3 AND id::text=ANY($4) AND expires_at>now() AND document<>'{}'::jsonb AND (document->>'complete')::boolean`, a.WorkspaceID, service, mapping, i.ids).Scan(&available); e != nil {
					return e
				}
				if len(i.ids) == 0 || available != len(i.ids) {
					reason = "Supporting evidence is missing, incomplete or expired."
					rec = "need_more_data"
				} else if next != nil && now.Before(*next) {
					reason = "Service recommendation cooldown is active."
				} else {
					rec = "investigate"
					reason = "Meaningful detector-backed change; manual investigation recommended."
					recommended = true
				}
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE automation_items SET trigger=$3,recommendation=$4,reason=$5 WHERE batch_id=$1 AND finding_id=$2`, a.BatchID, i.id, trigger, rec, reason); e != nil {
			return e
		}
		// Never let an older policy batch overwrite a newer evaluation's baseline.
		raw, _ := json.Marshal(i.measure)
		checkpointAt := i.at
		if i.clearedAt != nil && i.clearedAt.After(checkpointAt) {
			checkpointAt = *i.clearedAt
		}
		if _, e = tx.Exec(ctx, `UPDATE automation_checkpoints SET measurement=$3,observed_at=$4 WHERE workspace_id=$1 AND finding_id=$2 AND observed_at<=$4 AND policy_revision=$5`, a.WorkspaceID, i.id, raw, checkpointAt, revision); e != nil {
			return e
		}
	}
	state = "skipped"
	reason := "No recommendation; see each finding's reason."
	if blocked != "" {
		reason = blocked
	}
	if recommended {
		state = "recommended"
		reason = "Investigation recommended. No investigation or AI call was started."
		eligible := now.Add(time.Duration(p.CooldownMinutes) * time.Minute)
		next = &eligible
		if _, e = tx.Exec(ctx, `INSERT INTO automation_services(workspace_id,service_id,next_eligible_at) VALUES($1,$2,$3) ON CONFLICT(workspace_id,service_id) DO UPDATE SET next_eligible_at=EXCLUDED.next_eligible_at`, a.WorkspaceID, service, next); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE automation_batches SET state=$3,reason=$4,evaluated_at=$5,next_eligible_at=$6 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.BatchID, state, reason, now, next); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Server) retainAutomation(ctx context.Context) error {
	// Use the same service-first locking and checkpoint advancement as normal work.
	rows, e := s.pool.Query(ctx, `SELECT workspace_id::text,id::text FROM automation_batches b WHERE state='pending' AND due_at<=now() AND (created_at<now()-interval '15 minutes' OR EXISTS(SELECT 1 FROM river_job j WHERE j.id=b.job_id AND j.state IN ('discarded','cancelled'))) ORDER BY created_at LIMIT 100`)
	if e != nil {
		return e
	}
	args := []automationArgs{}
	for rows.Next() {
		var a automationArgs
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
		if e = s.evaluateAutomation(ctx, a); e != nil {
			return e
		}
	}
	_, e = s.pool.Exec(ctx, `DELETE FROM automation_batches WHERE id IN (SELECT id FROM automation_batches WHERE state<>'pending' AND created_at<now()-interval '30 days' LIMIT 1000)`)
	return e
}
