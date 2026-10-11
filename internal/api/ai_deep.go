package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"sherlock/internal/ai"
)

const deepInputLimit = 30000
const deepOutputLimit = 8000
const deepAttempts = 6
const deepTools = 6
const deepHTTP = 12

// Question and recent answers are data; neither can alter the immutable run scope.
func (s *Server) deepInput(ctx context.Context, q aiDB, ws, run string, p AIProvider, in aiStart, extra []string) (aiInput, error) {
	type turn struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	turns := []turn{}
	parent := in.Parent
	expires := time.Now().Add(24 * time.Hour)
	for i := 0; parent != "" && i < 3; i++ {
		var question, next string
		var raw []byte
		var ids []string
		var exp time.Time
		e := q.QueryRow(ctx, `SELECT COALESCE(question,''),report,evidence_ids,expires_at,COALESCE(parent_ai_run_id::text,'') FROM ai_runs WHERE workspace_id=$1 AND id::text=$2 AND run_id::text=$3 AND provider_id=$4 AND provider_revision=$5 AND state='completed' AND report IS NOT NULL AND expires_at>now() AND policy_revision=(SELECT revision FROM ai_policy WHERE workspace_id=$1)`, ws, parent, run, p.ID, p.Revision).Scan(&question, &raw, &ids, &exp, &next)
		if e != nil {
			return aiInput{}, errors.New("Follow-up context expired, was deleted or belongs to another scope/provider. Start a new analysis.")
		}
		var report ai.Report
		if json.Unmarshal(raw, &report) != nil {
			return aiInput{}, errors.New("Invalid prior report.")
		}
		turns = append(turns, turn{question, report.Summary})
		extra = append(extra, ids...)
		expires = minTime(expires, exp)
		parent = next
	}
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	out, e := s.buildAIInput(ctx, q, ws, run, p, extra...)
	if e != nil {
		return out, e
	}
	var payload map[string]any
	if e = json.Unmarshal(out.Payload, &payload); e != nil {
		return out, e
	}
	payload["version"] = ai.ExplorationVersion
	payload["question"] = ai.Clean(in.Question, out.Service.Mapping.SensitivePatterns)
	payload["recent_turns"] = turns
	payload["tools_enabled"] = in.Tools
	payload["scope_rule"] = "Original service and incident/comparison windows only. Wider scope requires a new investigation."
	payload["remaining"] = deepRemaining(deepAttempts, deepTools, deepHTTP, deepInputLimit, deepOutputLimit, []string{})
	out.Payload, _ = json.Marshal(payload)
	wire, e := ai.ExplorationBody(p.Kind, p.Model, out.Payload, ai.OutputLimit)
	if e != nil {
		return out, e
	}
	out.Bound = len(wire) + 1024
	if out.Bound > ai.InputLimit {
		return out, errors.New("Follow-up and evidence exceed the bounded context; start a new analysis.")
	}
	out.Expires = minTime(out.Expires, expires)
	// Bind consent to the configuration that supplied the preview's model,
	// prices and egress controls, just as the single-call analysis path does.
	sum := sha256.Sum256(append(wire, []byte(":"+in.Parent+":"+strconv.Itoa(p.Revision))...))
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}

func deepRemaining(calls, tools, http, input, output int, completed []string) map[string]any {
	return map[string]any{"provider_calls": calls, "tool_requests": tools, "telemetry_attempts": http, "input_tokens": input, "output_tokens": output, "completed_tools": completed}
}

// Only preceding persisted steps contribute, so a preview, reserved step and
// resumed dispatch produce the same bytes regardless of worker scheduling.
func (s *Server) deepProviderContext(ctx context.Context, q aiDB, ws, id string, seq int, p AIProvider, input aiInput) (aiInput, error) {
	var calls, tools, http, ib, ob int
	var raw []byte
	e := q.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='provider'),count(*) FILTER(WHERE kind<>'provider'),COALESCE(sum(telemetry_limit),0),COALESCE(sum(input_bound),0),COALESCE(sum(output_limit),0),COALESCE(jsonb_agg(kind ORDER BY sequence) FILTER(WHERE kind<>'provider'),'[]'::jsonb) FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2 AND sequence<$3`, ws, id, seq).Scan(&calls, &tools, &http, &ib, &ob, &raw)
	if e != nil {
		return input, e
	}
	completed := []string{}
	if e = json.Unmarshal(raw, &completed); e != nil {
		return input, e
	}
	var payload map[string]any
	if e = json.Unmarshal(input.Payload, &payload); e != nil {
		return input, e
	}
	payload["remaining"] = deepRemaining(deepAttempts-calls, deepTools-tools, deepHTTP-http, deepInputLimit-ib, deepOutputLimit-ob, completed)
	input.Payload, _ = json.Marshal(payload)
	wire, e := ai.ExplorationBody(p.Kind, p.Model, input.Payload, min(ai.OutputLimit, deepOutputLimit-ob))
	if e != nil {
		return input, e
	}
	input.Bound = len(wire) + 1024
	if input.Bound > ai.InputLimit {
		return input, errDeepBudget
	}
	return input, nil
}

func (s *Server) deepRequest(w http.ResponseWriter, r *http.Request, in aiStart, preview bool) {
	if len(in.Question) > 512 || strings.TrimSpace(in.Question) == "" && in.Parent != "" || in.Question != "" && in.Parent == "" || !preview && !in.Confirm {
		s.fail(w, r, 400, "invalid_followup", "Confirm processing; follow-up needs a retained parent report and a question of 1–512 bytes.")
		return
	}
	ctx, ws := r.Context(), workspace(r)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = lockWorkspaceScope(ctx, tx, ws); e == nil {
		e = aiWorkspaceLock(ctx, tx, ws)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	p, e := loadAIProvider(ctx, tx, ws, in.ProviderID)
	if e != nil {
		s.missing(w, r, e)
		return
	}
	ap, e := loadAIPolicy(ctx, tx, ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	input, e := s.deepInput(ctx, tx, ws, r.PathValue("id"), p, in, nil)
	if e != nil {
		s.fail(w, r, 409, "evidence_unavailable", e.Error())
		return
	}
	if !ap.Enabled || ap.RunLimit <= 0 || ap.DailyLimit <= 0 || p.Capability != "ready" || !aiAllowed(p, input.Service.ID, input.Service.Origin) || p.InputPrice <= 0 || p.OutputPrice <= 0 {
		s.fail(w, r, 403, "egress_denied", "Enable AI, test the model and allow this service/origin with positive budgets and prices.")
		return
	}
	if preview {
		wire, _ := ai.ExplorationBody(p.Kind, p.Model, input.Payload, ai.OutputLimit)
		s.json(w, 200, map[string]any{"payload": json.RawMessage(wire), "preview_digest": input.Digest, "provider_id": p.ID, "model": p.Model, "input_bound": input.Bound, "output_limit": ai.OutputLimit, "reservation": ai.Cost(int64(input.Bound), ai.OutputLimit, p.InputPrice, p.OutputPrice), "expires_at": input.Expires, "sends_data": false})
		return
	}
	if in.Digest != input.Digest {
		s.fail(w, r, 409, "preview_changed", "Preview the current scoped question and payload before sending.")
		return
	}
	var id string
	// Only completed automatic results can satisfy a manual request. Active
	// automatic work retains its policy-driven cancellation semantics.
	e = tx.QueryRow(ctx, `SELECT id::text FROM ai_runs WHERE workspace_id=$1 AND provider_id=$2 AND provider_revision=$3 AND policy_revision=$4 AND digest=$5 AND deep AND NOT cancel_requested AND (purpose='analysis' OR (purpose='automatic' AND state='completed')) AND (state IN ('queued','analyzing') OR (state='completed' AND NOT $6 AND report IS NOT NULL AND expires_at>now())) ORDER BY created_at DESC LIMIT 1`, ws, p.ID, p.Revision, ap.Revision, input.Digest, in.Fresh).Scan(&id)
	if e == nil {
		var ids []string
		if e = tx.QueryRow(ctx, `SELECT evidence_ids FROM ai_runs WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&ids); e == nil {
			if _, err := s.deepInput(ctx, tx, ws, r.PathValue("id"), p, in, ids); err != nil {
				e = pgx.ErrNoRows
			}
		}
	}
	if e == nil {
		s.json(w, 202, map[string]any{"id": id, "reused": true})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	var active bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_runs WHERE workspace_id=$1 AND service_id=$2 AND state IN ('queued','analyzing'))`, ws, input.Service.ID).Scan(&active); e != nil {
		s.internal(w, r, e)
		return
	}
	if active {
		s.fail(w, r, 409, "ai_active", "Another AI task is active for this service.")
		return
	}
	id, e = s.insertDeep(ctx, tx, ws, r.PathValue("id"), "", p, ap, input, in)
	if errors.Is(e, errDeepBudget) {
		s.fail(w, r, 409, "budget_exhausted", e.Error())
		return
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

var errDeepBudget = errors.New("Cumulative AI cost, token, tool or request budget exhausted; deterministic report remains available.")

func (s *Server) insertDeep(ctx context.Context, tx pgx.Tx, ws, run, batch string, p AIProvider, ap AIPolicy, input aiInput, in aiStart) (string, error) {
	base, e := s.buildAIInput(ctx, tx, ws, run, p)
	if e != nil {
		return "", e
	}
	purpose := "analysis"
	if batch != "" {
		purpose = "automatic"
	}
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO ai_runs(workspace_id,provider_id,provider_revision,policy_revision,run_id,service_id,purpose,model,origin,digest,payload,evidence_ids,reserved,input_bound,output_limit,input_price,output_price,price_date,expires_at,deadline,prompt_version,automation_batch_id,deep,tools_enabled,parent_ai_run_id,question,base_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,0,$13,$14,$15,$16,$17,$18,now()+interval '180 seconds',$19,NULLIF($20,'')::uuid,true,$21,NULLIF($22,'')::uuid,$23,$24) RETURNING id::text`, ws, p.ID, p.Revision, ap.Revision, run, input.Service.ID, purpose, p.Model, input.Service.Origin, input.Digest, input.Payload, input.IDs, input.Bound, ai.OutputLimit, p.InputPrice, p.OutputPrice, p.PriceDate, input.Expires, ai.ExplorationVersion, batch, in.Tools, in.Parent, ai.Clean(in.Question, input.Service.Mapping.SensitivePatterns), base.Digest).Scan(&id)
	if e != nil {
		return "", e
	}
	e = s.queueDeepProvider(ctx, tx, ws, id, p, ap, input)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.bounded_queued')`, ws)
	}
	return id, e
}

func (s *Server) queueDeepProvider(ctx context.Context, tx pgx.Tx, ws, id string, p AIProvider, ap AIPolicy, input aiInput) error {
	var seq, calls, inBound, outBound int
	var total, reserved int64
	e := tx.QueryRow(ctx, `SELECT COALESCE(max(sequence),0)+1,count(*) FILTER(WHERE kind='provider'),COALESCE(sum(input_bound),0),COALESCE(sum(output_limit),0) FROM ai_steps WHERE workspace_id=$1 AND ai_run_id=$2`, ws, id).Scan(&seq, &calls, &inBound, &outBound)
	if e != nil {
		return e
	}
	input, e = s.deepProviderContext(ctx, tx, ws, id, seq, p, input)
	if e != nil {
		return e
	}
	output := min(ai.OutputLimit, deepOutputLimit-outBound)
	if calls >= deepAttempts || output <= 0 || inBound+input.Bound > deepInputLimit {
		return errDeepBudget
	}
	cost := ai.Cost(int64(input.Bound), int64(output), p.InputPrice, p.OutputPrice)
	e = tx.QueryRow(ctx, `SELECT reserved FROM ai_runs WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&reserved)
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN accounting IN ('reserved','unknown') THEN reserved ELSE charged END),0) FROM ai_runs WHERE workspace_id=$1 AND (created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' OR accounting IN ('reserved','unknown'))`, ws).Scan(&total)
	if e != nil {
		return e
	}
	if reserved+cost > ap.RunLimit || total+cost > ap.DailyLimit {
		return errDeepBudget
	}
	_, e = tx.Exec(ctx, `INSERT INTO ai_steps(workspace_id,ai_run_id,sequence,kind,payload,input_bound,output_limit,reserved) VALUES($1,$2,$3,'provider',$4,$5,$6,$7)`, ws, id, seq, input.Payload, input.Bound, output, cost)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE ai_runs SET reserved=reserved+$3,accounting='reserved',evidence_ids=$4,expires_at=LEAST(expires_at,$5) WHERE workspace_id=$1 AND id=$2`, ws, id, cost, input.IDs, input.Expires)
	if e != nil {
		return e
	}
	return s.queueDeepJob(ctx, tx, deepArgs{ws, id, seq}, false)
}

type deepArgs struct {
	WorkspaceID string `json:"workspace_id"`
	ID          string `json:"id"`
	Sequence    int    `json:"sequence"`
}

func (deepArgs) Kind() string { return "ai_step" }

type deepToolArgs deepArgs

func (deepToolArgs) Kind() string { return "ai_tool" }

type deepWorker struct {
	river.WorkerDefaults[deepArgs]
	server *Server
}

func (w *deepWorker) Timeout(*river.Job[deepArgs]) time.Duration { return 190 * time.Second }
func (w *deepWorker) Work(ctx context.Context, j *river.Job[deepArgs]) error {
	return w.server.executeDeep(ctx, j.Args)
}

type deepToolWorker struct {
	river.WorkerDefaults[deepToolArgs]
	server *Server
}

func (w *deepToolWorker) Work(ctx context.Context, j *river.Job[deepToolArgs]) error {
	return w.server.executeDeep(ctx, deepArgs(j.Args))
}
func (s *Server) queueDeepJob(ctx context.Context, tx pgx.Tx, a deepArgs, tool bool) error {
	var args river.JobArgs = a
	queue := "ai"
	if tool {
		args = deepToolArgs(a)
		queue = "ai_tools"
	}
	j, e := s.jobs.InsertTx(ctx, tx, args, &river.InsertOpts{Queue: queue, MaxAttempts: 6})
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE ai_runs SET job_id=$3 WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, a.ID, j.Job.ID)
	return e
}

func (s *Server) listDeepSteps(w http.ResponseWriter, r *http.Request) {
	ctx, ws := r.Context(), workspace(r)
	var exists bool
	e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_runs WHERE workspace_id=$1 AND id::text=$2)`, ws, r.PathValue("id")).Scan(&exists)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if !exists {
		s.missing(w, r, pgx.ErrNoRows)
		return
	}
	rows, e := s.pool.Query(ctx, `SELECT sequence,kind,state,reason,input_bound,output_limit,telemetry_limit,reserved,charged,accounting,input_tokens,output_tokens,started_at,finished_at,evidence_ids FROM ai_steps WHERE workspace_id=$1 AND ai_run_id::text=$2 ORDER BY sequence LIMIT 13`, ws, r.PathValue("id"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var seq, ib, ol, tl int
		var kind, state, reason, accounting string
		var reserved, charged int64
		var input, output *int64
		var start, end *time.Time
		var ids []string
		if e = rows.Scan(&seq, &kind, &state, &reason, &ib, &ol, &tl, &reserved, &charged, &accounting, &input, &output, &start, &end, &ids); e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, map[string]any{"sequence": seq, "kind": kind, "state": state, "reason": reason, "input_bound": ib, "output_limit": ol, "telemetry_limit": tl, "reserved": reserved, "charged": charged, "accounting": accounting, "input_tokens": input, "output_tokens": output, "started_at": start, "finished_at": end, "evidence_ids": ids})
	}
	if e = rows.Err(); e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"items": items})
}
