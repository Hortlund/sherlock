package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"log/slog"
	"sherlock/internal/connectors"
	"sherlock/internal/detectors"
	"sherlock/internal/investigations"
	"time"
)

type runArgs struct {
	WorkspaceID string `json:"workspace_id"`
	RunID       string `json:"run_id"`
}

func (runArgs) Kind() string { return "sherlock_run" }

type runWorker struct {
	river.WorkerDefaults[runArgs]
	server *Server
}

func (w *runWorker) Work(ctx context.Context, j *river.Job[runArgs]) error {
	return w.server.execute(ctx, j.Args)
}
func (s *Server) initJobs(ctx context.Context) error {
	driver := riverpgxv5.New(s.pool)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73190004)`); e != nil {
		return e
	}
	m, e := rivermigrate.New(driver, nil)
	if e != nil {
		return e
	}
	if _, e = m.MigrateTx(ctx, tx, rivermigrate.DirectionUp, nil); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &runWorker{server: s})
	river.AddWorker(workers, &notificationWorker{server: s})
	river.AddWorker(workers, &aiWorker{server: s})
	s.jobs, e = river.NewClient(driver, &river.Config{Workers: workers, Queues: map[string]river.QueueConfig{"default": {MaxWorkers: 4}, "notifications": {MaxWorkers: 2}, "ai": {MaxWorkers: 2}}, JobTimeout: 100 * time.Second, RescueStuckJobsAfter: 4 * time.Minute, SoftStopTimeout: 5 * time.Second, Logger: slog.New(slog.NewTextHandler(discardWriter{}, nil))})
	return e
}

// River logs can contain driver diagnostics; application logs expose only safe run IDs/states.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error)     { return len(p), nil }
func (s *Server) StartJobs(ctx context.Context) error { return s.jobs.Start(ctx) }
func (s *Server) StopJobs(ctx context.Context) error  { return s.jobs.StopAndCancel(ctx) }
func (s *Server) settings(ctx context.Context, ws string) (detectors.Config, error) {
	c := detectors.Defaults()
	var b []byte
	e := s.pool.QueryRow(ctx, `SELECT settings FROM workspace_settings WHERE workspace_id=$1`, ws).Scan(&b)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}

var errRunConflict = errors.New("an active run has a different window, mapping, or settings")

func (s *Server) enqueue(ctx context.Context, tx pgx.Tx, ws string, v Service, c detectors.Config, kind string, scheduled bool, start, end time.Time) (string, error) {
	// Serialize launch requests and lock any active run until the caller commits.
	// A different investigation is a conflict, never a successful substitution.
	var revision int
	if e := tx.QueryRow(ctx, `SELECT revision FROM services WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, v.ID).Scan(&revision); e != nil {
		return "", e
	}
	if revision != v.Revision {
		return "", errRunConflict
	}
	start = start.Truncate(time.Microsecond)
	end = end.Truncate(time.Microsecond)
	snap, _ := json.Marshal(v)
	conf, _ := json.Marshal(c)
	var id string
	var oldStart, oldEnd time.Time
	var oldRevision int
	var sameConfig bool
	e := tx.QueryRow(ctx, `SELECT id::text,window_start,window_end,(snapshot->>'revision')::int,config=$4::jsonb FROM runs WHERE workspace_id=$1 AND service_id=$2 AND kind=$3 AND state IN ('queued','collecting','evaluating','reporting') FOR UPDATE`, ws, v.ID, kind, conf).Scan(&id, &oldStart, &oldEnd, &oldRevision, &sameConfig)
	if e == nil {
		if kind == "investigation" && (!start.Equal(oldStart) || !end.Equal(oldEnd) || oldRevision != v.Revision || !sameConfig) {
			return "", errRunConflict
		}
		return id, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", e
	}
	e = tx.QueryRow(ctx, `INSERT INTO runs(workspace_id,service_id,kind,snapshot,config,window_start,window_end,scheduled) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`, ws, v.ID, kind, snap, conf, start, end, scheduled).Scan(&id)
	if e != nil {
		return "", e
	}

	j, e := s.jobs.InsertTx(ctx, tx, runArgs{ws, id}, &river.InsertOpts{MaxAttempts: 5})
	if e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `UPDATE runs SET job_id=$3 WHERE workspace_id=$1 AND id=$2`, ws, id, j.Job.ID)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,$2)`, ws, kind+".queued")
	}
	return id, e
}

// Pump coalesces missed intervals. The row lock, cursor advance, run and job share one commit.
func (s *Server) Pump(ctx context.Context) error {
	rows, e := s.pool.Query(ctx, `SELECT workspace_id::text,id::text FROM services WHERE monitoring AND next_scan_at<=now() ORDER BY next_scan_at LIMIT 100`)
	if e != nil {
		return e
	}
	pairs := [][2]string{}
	for rows.Next() {
		var a [2]string
		if e = rows.Scan(&a[0], &a[1]); e != nil {
			rows.Close()
			return e
		}
		pairs = append(pairs, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, a := range pairs {
		if e = s.schedule(ctx, a[0], a[1]); e != nil {
			return e
		}
	}
	_, e = s.pool.Exec(ctx, `UPDATE runs SET state='failed',finished_at=now(),result='{"issues":["Job attempts exhausted; start a new run."]}'::jsonb WHERE state IN ('queued','collecting','evaluating','reporting') AND EXISTS(SELECT 1 FROM river_job WHERE river_job.id=runs.job_id AND river_job.state IN ('discarded','cancelled'))`)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `WITH expired AS (UPDATE findings SET state='new',snoozed_until=NULL WHERE state='snoozed' AND snoozed_until<=now() RETURNING workspace_id,id), events AS (INSERT INTO finding_events(workspace_id,finding_id,state) SELECT workspace_id,id,'new' FROM expired) INSERT INTO audit_events(workspace_id,action) SELECT workspace_id,'finding.snooze_expired' FROM expired`)
	return e
}
func (s *Server) schedule(ctx context.Context, ws, id string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	v, e := scanService(tx.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id=$2 AND monitoring AND next_scan_at<=now() FOR UPDATE SKIP LOCKED`, ws, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	c, e := s.settings(ctx, ws)
	if e != nil {
		return e
	}
	end := time.Now().UTC().Truncate(5 * time.Minute)
	if _, e = s.enqueue(ctx, tx, ws, v, c, "scan", true, end.Add(-time.Duration(c.CurrentMinutes)*time.Minute), end); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE services SET next_scan_at=now()+$3*interval '1 minute' WHERE workspace_id=$1 AND id=$2`, ws, id, c.CadenceMinutes); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type RunResult struct {
	Report      *investigations.Report `json:"report,omitempty"`
	Analysis    detectors.Result       `json:"analysis"`
	EvidenceIDs []string               `json:"evidence_ids"`
	Issues      []string               `json:"issues"`
}

func terminal(state string) bool {
	return state == "completed" || state == "cancelled" || state == "failed" || state == "insufficient_evidence"
}
func (s *Server) stage(ctx context.Context, a runArgs, stage, message string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE runs SET state=$3 WHERE workspace_id=$1 AND id=$2 AND NOT cancelled AND state IN ('queued','collecting','evaluating','reporting')`, a.WorkspaceID, a.RunID, stage)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return errors.New("run no longer active")
	}
	_, e = tx.Exec(ctx, `INSERT INTO run_events(workspace_id,run_id,stage,message) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, a.WorkspaceID, a.RunID, stage, message)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Server) execute(parent context.Context, a runArgs) error {
	// A session advisory lock also protects against overlapping rescue/duplicate delivery.
	lock, e := s.pool.Acquire(parent)
	if e != nil {
		return e
	}
	defer lock.Release()
	var acquired bool
	e = lock.QueryRow(parent, `SELECT pg_try_advisory_lock(hashtextextended($1,7319))`, a.RunID).Scan(&acquired)
	if e != nil {
		return e
	}
	if !acquired {
		return errors.New("run already executing")
	}
	defer func() {
		ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_, _ = lock.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,7319))`, a.RunID)
	}()
	var state, kind string
	var snap, conf []byte
	var start, end time.Time
	var deadline *time.Time
	var scheduled bool
	e = s.pool.QueryRow(parent, `SELECT state,kind,snapshot,config,window_start,window_end,deadline,scheduled FROM runs WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.RunID).Scan(&state, &kind, &snap, &conf, &start, &end, &deadline, &scheduled)
	if e != nil {
		return e
	}
	if terminal(state) {
		return nil
	}
	var v Service
	var c detectors.Config
	if e = json.Unmarshal(snap, &v); e != nil {
		return e
	}
	if e = json.Unmarshal(conf, &c); e != nil {
		return e
	}
	e = s.pool.QueryRow(parent, `UPDATE runs SET deadline=COALESCE(deadline,now()+interval '90 seconds') WHERE workspace_id=$1 AND id=$2 RETURNING deadline`, a.WorkspaceID, a.RunID).Scan(&deadline)
	if e != nil {
		return e
	}
	if time.Now().After(*deadline) {
		return s.finish(parent, a, "failed", RunResult{Issues: []string{"Persisted execution deadline exhausted; start a new run."}})
	}
	ctx, cancel := context.WithDeadline(parent, *deadline)
	defer cancel()
	ctx = connectors.WithRequestGuard(ctx, func(ctx context.Context) error {
		tag, e := s.pool.Exec(ctx, `UPDATE runs SET requests=requests+1 WHERE workspace_id=$1 AND id=$2 AND NOT cancelled AND requests<12 AND deadline>now() AND EXISTS(SELECT 1 FROM services WHERE workspace_id=$1 AND id=$3 AND revision=$4 AND (NOT $5 OR monitoring))`, a.WorkspaceID, a.RunID, v.ID, v.Revision, scheduled)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return &connectors.Error{Code: "budget_or_scope_changed", Message: "Collection stopped: budget exhausted, cancelled, or service mapping/monitoring changed."}
		}
		return nil
	})
	if e = s.stage(ctx, a, "collecting", "Collecting bounded current and comparison evidence."); e != nil {
		return e
	}
	current, issues := s.collect(ctx, a.WorkspaceID, v, start, end)
	baseline, more := s.collect(ctx, a.WorkspaceID, v, start.Add(-time.Duration(c.BaselineMinutes)*time.Minute), start)
	issues = append(issues, more...)
	if ctx.Err() != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		return s.finish(parent, a, "failed", RunResult{Issues: []string{"Execution deadline exhausted."}})
	}
	if e = s.stage(ctx, a, "evaluating", "Checking coverage and deterministic detector guards."); e != nil {
		return e
	}
	inputs := func(v []Evidence) []detectors.Input {
		out := []detectors.Input{}
		for _, e := range v {
			out = append(out, detectors.Input{ID: e.ID, Observation: e.Observation})
		}
		return out
	}
	result := RunResult{Analysis: detectors.Evaluate(inputs(current), inputs(baseline), c), EvidenceIDs: []string{}, Issues: issues}
	for _, ev := range append(current, baseline...) {
		result.EvidenceIDs = append(result.EvidenceIDs, ev.ID)
	}
	if kind == "scan" {
		if e = s.persistFindings(ctx, a, v, scheduled, end, c, result.Analysis.Candidates); e != nil {
			return e
		}
	}
	adequate := false
	for _, cover := range result.Analysis.Coverage {
		if cover.Status == "active" {
			adequate = true
		}
	}
	if kind == "investigation" {
		if e = s.stage(ctx, a, "reporting", "Validating evidence provenance and preparing the report."); e != nil {
			return e
		}
		report, err := s.buildReport(ctx, a, v, start, end, c.BaselineMinutes, append(current, baseline...))
		if err != nil {
			return s.finish(ctx, a, "failed", RunResult{Issues: []string{"Evidence validation failed; no report was published."}})
		}
		result.Report = report
		adequate = report.Adequate
	}
	final := "completed"
	if !adequate {
		final = "insufficient_evidence"
	}
	return s.finish(ctx, a, final, result)
}
func (s *Server) finish(ctx context.Context, a runArgs, state string, result RunResult) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	raw, _ := json.Marshal(result)
	tag, e := tx.Exec(ctx, `UPDATE runs SET state=$3,result=$4,finished_at=now() WHERE workspace_id=$1 AND id=$2 AND NOT cancelled AND state IN ('queued','collecting','evaluating','reporting')`, a.WorkspaceID, a.RunID, state, raw)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	_, e = tx.Exec(ctx, `INSERT INTO run_events(workspace_id,run_id,stage,message) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, a.WorkspaceID, a.RunID, state, "Run "+state+".")
	if e != nil {
		return e
	}
	if state == "completed" {
		var kind string
		var snap []byte
		if e = tx.QueryRow(ctx, `SELECT kind,snapshot FROM runs WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.RunID).Scan(&kind, &snap); e != nil {
			return e
		}
		if kind == "investigation" {
			var v Service
			if e = json.Unmarshal(snap, &v); e != nil {
				return e
			}
			if e = s.notificationEvent(ctx, tx, a.WorkspaceID, "investigation.completed:"+a.RunID, "investigation.completed", v.ID, "", a.RunID, v.Origin, "notice", result.EvidenceIDs); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
func (s *Server) persistFindings(ctx context.Context, a runArgs, v Service, scheduled bool, end time.Time, c detectors.Config, candidates []detectors.Candidate) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var revision int
	var monitoring bool
	e = tx.QueryRow(ctx, `SELECT revision,monitoring FROM services WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, v.ID).Scan(&revision, &monitoring)
	if e != nil {
		return e
	}
	if revision != v.Revision || (scheduled && !monitoring) {
		return nil
	}
	for _, candidate := range candidates {
		var id, state, oldSeverity string
		var occurrence int
		var last time.Time
		var closed *time.Time
		e = tx.QueryRow(ctx, `SELECT id::text,state,occurrence,last_seen,closed_at,severity FROM findings WHERE workspace_id=$1 AND service_id=$2 AND mapping_revision=$3 AND detector=$4 AND signal=$5 ORDER BY occurrence DESC LIMIT 1 FOR UPDATE`, a.WorkspaceID, v.ID, v.Revision, candidate.Detector, candidate.Signal).Scan(&id, &state, &occurrence, &last, &closed, &oldSeverity)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		raw, _ := json.Marshal(candidate)
		if id != "" && !end.After(last) {
			continue
		}
		if id != "" && state != "resolved" && state != "dismissed" {
			_, e = tx.Exec(ctx, `UPDATE findings SET last_seen=$3,description=$4,details=$5,severity=$6 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id, end, candidate.Description, raw, candidate.Severity)
			if e == nil && oldSeverity == "notice" && candidate.Severity == "warning" {
				e = s.notificationEvent(ctx, tx, a.WorkspaceID, "finding.escalated:"+id+":"+end.UTC().Format(time.RFC3339Nano), "finding.escalated", v.ID, id, "", v.Origin, candidate.Severity, candidate.EvidenceIDs)
			}
		} else {
			if closed != nil && end.Before(closed.Add(time.Duration(c.CooldownHours)*time.Hour)) {
				continue
			}
			e = tx.QueryRow(ctx, `INSERT INTO findings(workspace_id,service_id,mapping_revision,origin,detector,signal,occurrence,severity,title,description,details,first_seen,last_seen) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12) RETURNING id::text`, a.WorkspaceID, v.ID, v.Revision, v.Origin, candidate.Detector, candidate.Signal, occurrence+1, candidate.Severity, candidate.Title, candidate.Description, raw, end).Scan(&id)
			if e == nil {
				e = s.notificationForFinding(ctx, tx, a.WorkspaceID, id, "finding.created", candidate.EvidenceIDs)
			}
		}
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
