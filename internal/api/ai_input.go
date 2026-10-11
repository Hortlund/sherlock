package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sherlock/internal/ai"
	"sherlock/internal/connectors"
	"sherlock/internal/detectors"
	"sherlock/internal/investigations"
	"sort"
	"strconv"
	"time"
)

type aiFact struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Complete bool      `json:"complete"`
	Coverage float64   `json:"coverage"`
	Samples  int       `json:"samples"`
	Value    float64   `json:"value"`
	Errors   int       `json:"errors"`
	Warnings int       `json:"warnings"`
	Excerpts []string  `json:"excerpts,omitempty"`
}
type aiPayload struct {
	Version          string   `json:"version"`
	Origin           string   `json:"origin"`
	ServiceID        string   `json:"service_id,omitempty"`
	Evidence         []aiFact `json:"evidence"`
	Limitations      []string `json:"limitations"`
	ExcerptsIncluded bool     `json:"excerpts_included"`
}
type aiInput struct {
	Payload json.RawMessage
	IDs     []string
	Expires time.Time
	Service Service
	Digest  string
	Bound   int
}

func (s *Server) buildAIInput(ctx context.Context, q aiDB, ws, runID string, p AIProvider, extra ...string) (aiInput, error) {
	out := aiInput{Expires: time.Now().Add(24 * time.Hour), IDs: []string{}}
	payload := aiPayload{Version: ai.PromptVersion, Origin: "demo", Evidence: []aiFact{}, Limitations: []string{"No dependency health, traces, deployment history, database or pool instrumentation is available. Causality is unverified."}}
	if runID == "" {
		out.IDs = []string{"synthetic-evidence"}
		payload.Evidence = []aiFact{{ID: "synthetic-evidence", Kind: "request_total", Complete: true, Coverage: 1, Value: 100, Samples: 100}}
		payload.Limitations = []string{"Synthetic capability test. No real service data or incident."}
	} else {
		run, e := scanRun(q.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE workspace_id=$1 AND id::text=$2 AND kind='investigation' AND state='completed'`, ws, runID))
		if e != nil {
			return out, errors.New("Complete a deterministic investigation first.")
		}
		current, e := scanService(q.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id=$2`, ws, run.ServiceID))
		if e != nil {
			return out, e
		}
		if current.Revision != run.Snapshot.Revision {
			return out, errors.New("Mapping changed; collect a new investigation before AI analysis.")
		}
		out.Service = current
		var cfg detectors.Config
		var raw []byte
		e = q.QueryRow(ctx, `SELECT config FROM runs WHERE workspace_id=$1 AND id=$2`, ws, runID).Scan(&raw)
		if e != nil {
			return out, e
		}
		if e = json.Unmarshal(raw, &cfg); e != nil {
			return out, e
		}
		if len(run.Result.EvidenceIDs) == 0 || len(run.Result.EvidenceIDs) > 12 {
			return out, errors.New("No bounded evidence set is available.")
		}
		seen := map[string]bool{}
		for _, id := range run.Result.EvidenceIDs {
			seen[id] = true
		}
		for _, id := range extra {
			if !seen[id] {
				run.Result.EvidenceIDs = append(run.Result.EvidenceIDs, id)
				seen[id] = true
			}
		}
		if len(run.Result.EvidenceIDs) > 48 {
			return out, errors.New("Evidence context limit reached.")
		}
		verified := []investigations.Evidence{}
		out.Expires = time.Now().Add(time.Duration(cfg.RetentionDays) * 24 * time.Hour)
		payload.Origin = current.Origin
		payload.ServiceID = current.ID
		for _, id := range run.Result.EvidenceIDs {
			var ev investigations.Evidence
			var document []byte
			var available bool
			e = q.QueryRow(ctx, `SELECT e.id::text,e.workspace_id::text,e.service_id::text,e.origin,e.mapping_revision,e.expires_at,e.document,c.enabled AND NOT c.deleted FROM evidence e JOIN connections c ON c.workspace_id=e.workspace_id AND c.id=e.connection_id WHERE e.workspace_id=$1 AND e.id::text=$2`, ws, id).Scan(&ev.ID, &ev.WorkspaceID, &ev.ServiceID, &ev.Origin, &ev.MappingRevision, &ev.ExpiresAt, &document, &available)
			if e != nil || !available {
				return out, errors.New("Evidence source is unavailable for AI processing.")
			}
			if e = json.Unmarshal(document, &ev.Observation); e != nil {
				return out, e
			}
			verified = append(verified, ev)
			out.Expires = minTime(out.Expires, ev.ExpiresAt)
			out.IDs = append(out.IDs, id)
			o := ev.Observation
			fact := aiFact{ID: id, Kind: o.Kind, Start: o.Start, End: o.End, Complete: o.Complete, Coverage: o.CoverageFraction, Samples: o.SampleCount, Value: o.Value}
			for _, v := range o.Errors {
				fact.Errors += v.Count
			}
			for _, v := range o.Warnings {
				fact.Warnings += v.Count
			}
			if p.AllowExcerpts && current.Mapping.StoreExcerpts {
				patterns := append(append([]connectors.Pattern{}, o.Errors...), o.Warnings...)
				sort.SliceStable(patterns, func(i, j int) bool { return patterns[i].Count > patterns[j].Count })
				for _, v := range patterns[:min(len(patterns), 2)] {
					if v.Sample != "" {
						fact.Excerpts = append(fact.Excerpts, ai.Clean(v.Sample, current.Mapping.SensitivePatterns))
						payload.ExcerptsIncluded = true
					}
				}
			}
			payload.Evidence = append(payload.Evidence, fact)
		}
		if _, e = investigations.Build(investigations.Scope{WorkspaceID: ws, ServiceID: current.ID, Origin: current.Origin, MappingRevision: current.Revision, Start: run.Start, End: run.End, BaselineStart: run.Start.Add(-time.Duration(cfg.BaselineMinutes) * time.Minute)}, verified); e != nil {
			return out, e
		}
	}
	var e error
	out.Payload, e = json.Marshal(payload)
	if e != nil {
		return out, e
	}
	out.Bound, e = ai.InputBound(p.Kind, p.Model, out.Payload)
	if e != nil {
		return out, e
	}
	if out.Bound > ai.InputLimit {
		for i := range payload.Evidence {
			payload.Evidence[i].Excerpts = nil
		}
		payload.ExcerptsIncluded = false
		payload.Limitations = append(payload.Limitations, "Excerpts omitted to fit the bounded context.")
		out.Payload, _ = json.Marshal(payload)
		out.Bound, _ = ai.InputBound(p.Kind, p.Model, out.Payload)
	}
	if out.Bound > ai.InputLimit {
		return out, errors.New("Evidence exceeds the bounded AI context.")
	}
	wire, e := ai.RequestBody(p.Kind, p.Model, out.Payload)
	if e != nil {
		return out, e
	}
	sum := sha256.Sum256(append(wire, []byte(strconv.Itoa(p.Revision))...))
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}
