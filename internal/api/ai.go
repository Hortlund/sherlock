package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"net/http"
	"regexp"
	"sherlock/internal/ai"
	"slices"
	"strings"
	"time"
)

type aiDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type AIPolicy struct {
	Enabled    bool  `json:"enabled"`
	DailyLimit int64 `json:"daily_limit"`
	RunLimit   int64 `json:"run_limit"`
	Revision   int   `json:"revision"`
}
type AIProvider struct {
	TriageModel       string   `json:"triage_model"`
	TriageInputPrice  int64    `json:"triage_input_price"`
	TriageOutputPrice int64    `json:"triage_output_price"`
	TriageCapability  string   `json:"triage_capability"`
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	Revision          int      `json:"revision"`
	Capability        string   `json:"capability"`
	SecretSet         bool     `json:"secret_set"`
	Enabled           bool     `json:"enabled"`
	Model             string   `json:"model"`
	Services          []string `json:"services"`
	AllowDemo         bool     `json:"allow_demo"`
	AllowExcerpts     bool     `json:"allow_excerpts"`
	InputPrice        int64    `json:"input_price"`
	OutputPrice       int64    `json:"output_price"`
	PriceDate         string   `json:"price_date"`
}

var modelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,119}$`)

func loadAIPolicy(ctx context.Context, q aiDB, ws string) (AIPolicy, error) {
	var p AIPolicy
	e := q.QueryRow(ctx, `SELECT enabled,daily_limit,run_limit,revision FROM ai_policy WHERE workspace_id=$1`, ws).Scan(&p.Enabled, &p.DailyLimit, &p.RunLimit, &p.Revision)
	if errors.Is(e, pgx.ErrNoRows) {
		e = nil
	}
	return p, e
}
func loadAIProvider(ctx context.Context, q aiDB, ws, id string) (AIProvider, error) {
	var p AIProvider
	var raw []byte
	var revision int
	var capability, triageCapability string
	var secret bool
	e := q.QueryRow(ctx, `SELECT id::text,kind,config,revision,capability,triage_capability,octet_length(secret)>0 FROM ai_providers WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted`, ws, id).Scan(&p.ID, &p.Kind, &raw, &revision, &capability, &triageCapability, &secret)
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(raw, &p)
	p.Revision = revision
	p.Capability = capability
	p.TriageCapability = triageCapability
	p.SecretSet = secret
	if p.Services == nil {
		p.Services = []string{}
	}
	return p, e
}
func aiAllowed(p AIProvider, service, origin string) bool {
	return p.Enabled && slices.Contains(p.Services, service) && (origin != "demo" || p.AllowDemo)
}

// Acquire before service/rule scope locks and AI accounting. Mapping/source
// editors take the workspace row exclusively before services; this shared guard
// keeps that order while allowing accounting writes' workspace FK checks.
// Never retain this transaction lock across provider HTTP.
func lockWorkspaceScope(ctx context.Context, tx pgx.Tx, ws string) error {
	_, e := tx.Exec(ctx, `SELECT id FROM workspaces WHERE id=$1 FOR KEY SHARE`, ws)
	return e
}

func aiWorkspaceLock(ctx context.Context, tx pgx.Tx, ws string) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,731904))`, ws)
	return e
}
func (s *Server) aiRoutes(m *http.ServeMux) {
	for path, h := range map[string]http.HandlerFunc{
		"GET /api/v1/ai/settings": s.getAISettings, "POST /api/v1/ai/policy": s.saveAIPolicy, "POST /api/v1/ai/providers": s.saveAIProvider, "POST /api/v1/ai/providers/{id}": s.saveAIProvider, "POST /api/v1/ai/providers/{id}/delete": s.deleteAIProvider,
		"POST /api/v1/ai/providers/{id}/test": s.startAITest, "POST /api/v1/ai/providers/{id}/triage-test": s.startAITriageTest, "POST /api/v1/ai/providers/{id}/triage-preview": s.previewTriage, "POST /api/v1/investigations/{id}/ai/preview": s.previewAI, "POST /api/v1/investigations/{id}/ai": s.startAIAnalysis,
		"GET /api/v1/ai/runs": s.listAIRuns, "POST /api/v1/ai/runs/{id}/cancel": s.cancelAI, "POST /api/v1/ai/runs/{id}/delete": s.deleteAIContent, "POST /api/v1/ai/runs/{id}/reconcile": s.reconcileAI,
	} {
		m.Handle(path, s.requireSession(h))
	}
}
func (s *Server) getAISettings(w http.ResponseWriter, r *http.Request) {
	ctx, ws := r.Context(), workspace(r)
	p, e := loadAIPolicy(ctx, s.pool, ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	rows, e := s.pool.Query(ctx, `SELECT id::text FROM ai_providers WHERE workspace_id=$1 AND NOT deleted ORDER BY kind`, ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		s.internal(w, r, e)
		return
	}
	providers := []AIProvider{}
	for _, id := range ids {
		v, e := loadAIProvider(ctx, s.pool, ws, id)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		providers = append(providers, v)
	}
	var spent, reserved int64
	e = s.pool.QueryRow(ctx, `SELECT COALESCE(sum(charged) FILTER(WHERE created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AND accounting IN ('settled','reconciled')),0),COALESCE(sum(reserved) FILTER(WHERE accounting IN ('reserved','unknown')),0) FROM ai_runs WHERE workspace_id=$1`, ws).Scan(&spent, &reserved)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"policy": p, "providers": providers, "spent_today": spent, "reserved": reserved, "input_limit": ai.InputLimit, "output_limit": ai.OutputLimit, "prompt_version": ai.PromptVersion, "fixture": s.aiFixture})
}
func (s *Server) saveAIPolicy(w http.ResponseWriter, r *http.Request) {
	var p AIPolicy
	if !s.decode(w, r, &p) {
		return
	}
	if p.DailyLimit < 0 || p.DailyLimit > 1000000000 || p.RunLimit < 0 || p.RunLimit > p.DailyLimit || (p.Enabled && (p.RunLimit == 0 || p.DailyLimit == 0)) {
		s.fail(w, r, 400, "invalid_budget", "Set positive per-run and daily limits, with per-run at most daily and daily at most $1,000.")
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
	old, e := loadAIPolicy(ctx, tx, ws)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if old.Revision != p.Revision {
		s.fail(w, r, 409, "revision_conflict", "AI policy changed; refresh before saving.")
		return
	}
	p.Revision++
	_, e = tx.Exec(ctx, `INSERT INTO ai_policy(workspace_id,enabled,daily_limit,run_limit,revision) VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id) DO UPDATE SET enabled=$2,daily_limit=$3,run_limit=$4,revision=$5`, ws, p.Enabled, p.DailyLimit, p.RunLimit, p.Revision)
	if e == nil {
		e = cancelAIQueued(ctx, tx, ws, "", "Workspace AI policy changed.", !p.Enabled)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.policy_saved')`, ws)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, p)
}

// AI accounting/configuration writes share a workspace advisory lock, then provider/run locks. No transaction spans provider HTTP.
func cancelAIQueued(ctx context.Context, tx pgx.Tx, ws, provider, reason string, purge bool) error {
	_, e := tx.Exec(ctx, `UPDATE ai_runs SET state=CASE WHEN state IN ('queued','analyzing') THEN 'cancelled' ELSE state END,reason=CASE WHEN state IN ('queued','analyzing') THEN $3 ELSE reason END,finished_at=CASE WHEN state IN ('queued','analyzing') THEN now() ELSE finished_at END,accounting=CASE WHEN started_at IS NULL THEN 'settled' ELSE accounting END,payload=NULL,report=CASE WHEN $4 THEN NULL ELSE report END WHERE workspace_id=$1 AND ($2='' OR provider_id::text=$2)`, ws, provider, reason, purge)
	return e
}
func (s *Server) saveAIProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider AIProvider `json:"provider"`
		APIKey   string     `json:"api_key"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	p := in.Provider
	if p.TriageModel != "" && (!modelID.MatchString(p.TriageModel) || p.TriageInputPrice <= 0 || p.TriageOutputPrice <= 0 || p.TriageInputPrice > 10000000000 || p.TriageOutputPrice > 10000000000) {
		s.fail(w, r, 400, "invalid_triage_model", "Supply a valid triage model and positive input/output prices.")
		return
	}
	if (p.Kind != "openai" && p.Kind != "anthropic") || !modelID.MatchString(p.Model) || len(p.Services) > 100 || p.InputPrice < 0 || p.OutputPrice < 0 || p.InputPrice > 10000000000 || p.OutputPrice > 10000000000 || len(in.APIKey) > 1024 || strings.ContainsAny(in.APIKey, "\r\n") {
		s.fail(w, r, 400, "invalid_provider", "Choose OpenAI or Anthropic, a model ID, valid prices and at most 100 services.")
		return
	}
	date, e := time.Parse("2006-01-02", p.PriceDate)
	if e != nil || date.After(time.Now()) || (p.Enabled && (p.InputPrice == 0 || p.OutputPrice == 0)) {
		s.fail(w, r, 400, "invalid_pricing", "Supply a price date no later than today and positive input/output prices before enabling.")
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
	id := r.PathValue("id")
	var encrypted []byte
	revision := 1
	if id != "" {
		old, e := loadAIProvider(ctx, tx, ws, id)
		if e != nil {
			s.missing(w, r, e)
			return
		}
		if old.Kind != p.Kind || old.Revision != p.Revision {
			s.fail(w, r, 409, "revision_conflict", "Provider changed, or provider kind differs. Refresh before saving.")
			return
		}
		revision = old.Revision + 1
		if e = tx.QueryRow(ctx, `SELECT secret FROM ai_providers WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&encrypted); e != nil {
			s.internal(w, r, e)
			return
		}
	} else {
		var exists bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_providers WHERE workspace_id=$1 AND kind=$2 AND NOT deleted)`, ws, p.Kind).Scan(&exists); e != nil {
			s.internal(w, r, e)
			return
		}
		if exists {
			s.fail(w, r, 409, "provider_exists", "Edit the existing configuration for this provider.")
			return
		}
		if e = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); e != nil {
			s.internal(w, r, e)
			return
		}
	}
	if in.APIKey != "" {
		encrypted = s.vault.Seal([]byte(in.APIKey), ws+":ai:"+id)
	}
	if len(encrypted) == 0 {
		s.fail(w, r, 400, "missing_key", "Supply the provider API key.")
		return
	}
	if p.Services == nil {
		p.Services = []string{}
	}
	for _, sid := range p.Services {
		var exists bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE workspace_id=$1 AND id::text=$2)`, ws, sid).Scan(&exists)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		if !exists {
			s.fail(w, r, 400, "invalid_service", "Choose services in this workspace.")
			return
		}
	}
	p.ID = id
	p.Revision = revision
	p.Capability = "untested"
	p.TriageCapability = "untested"
	p.SecretSet = true
	raw, _ := json.Marshal(p)
	_, e = tx.Exec(ctx, `INSERT INTO ai_providers(id,workspace_id,kind,config,revision,secret) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET config=$4,revision=$5,secret=$6,capability='untested',triage_capability='untested'`, id, ws, p.Kind, raw, revision, encrypted)
	if e == nil {
		e = cancelAIQueued(ctx, tx, ws, id, "Provider configuration changed.", true)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.provider_saved')`, ws)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, p)
}
func (s *Server) deleteAIProvider(w http.ResponseWriter, r *http.Request) {
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
		tag, e = tx.Exec(ctx, `UPDATE ai_providers SET deleted=true,secret=''::bytea,revision=revision+1,capability='deleted' WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted`, ws, r.PathValue("id"))
		if e == nil && tag.RowsAffected() == 0 {
			s.missing(w, r, pgx.ErrNoRows)
			return
		}
	}
	if e == nil {
		e = cancelAIQueued(ctx, tx, ws, r.PathValue("id"), "Provider deleted.", true)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) VALUES($1,'ai.provider_deleted')`, ws)
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
