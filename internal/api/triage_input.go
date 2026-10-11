package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sherlock/internal/ai"
	"sherlock/internal/automation"
	"sherlock/internal/connectors"
	"sherlock/internal/detectors"
	"slices"
	"strconv"
	"time"
)

type triageSignal struct {
	ID              string    `json:"id"`
	Detector        string    `json:"detector"`
	Severity        string    `json:"severity"`
	Current         float64   `json:"current"`
	Baseline        float64   `json:"baseline"`
	Unit            string    `json:"unit"`
	CurrentMinutes  int       `json:"current_minutes"`
	BaselineMinutes int       `json:"baseline_minutes"`
	ObservedAt      time.Time `json:"observed_at"`
	EvidenceIDs     []string  `json:"evidence_ids"`
}
type triagePayload struct {
	Version         string         `json:"version"`
	Origin          string         `json:"origin"`
	ServiceID       string         `json:"service_id,omitempty"`
	MappingRevision int            `json:"mapping_revision"`
	Signals         []triageSignal `json:"signals"`
}

func triageProvider(p AIProvider) AIProvider {
	p.Model = p.TriageModel
	p.InputPrice = p.TriageInputPrice
	p.OutputPrice = p.TriageOutputPrice
	p.Capability = p.TriageCapability
	p.AllowExcerpts = false
	return p
}
func (s *Server) invocationInput(ctx context.Context, q aiDB, ws, run, batch, purpose string, p AIProvider) (aiInput, error) {
	if ai.IsTriage(purpose) {
		return s.buildTriageInput(ctx, q, ws, batch, p)
	}
	if purpose == "automatic" {
		if batch == "" {
			return aiInput{}, errors.New("Automatic activity is no longer retained.")
		}
		if _, e := s.buildRecommendationInput(ctx, q, ws, batch, p, true); e != nil {
			return aiInput{}, e
		}
	}
	return s.buildAIInput(ctx, q, ws, run, p)
}

// Triage transmits numbers, detector names and opaque IDs only. No service names,
// log fingerprints, excerpts, selectors, URLs, or user-authored text are copied.
func (s *Server) buildTriageInput(ctx context.Context, q aiDB, ws, batch string, p AIProvider) (aiInput, error) {
	return s.buildRecommendationInput(ctx, q, ws, batch, p, false)
}

type recommendationUnavailable string

func (e recommendationUnavailable) Error() string { return string(e) }

func (s *Server) buildRecommendationInput(ctx context.Context, q aiDB, ws, batch string, p AIProvider, automatic bool) (aiInput, error) {
	out := aiInput{Expires: time.Now().Add(24 * time.Hour), IDs: []string{}}
	payload := triagePayload{Version: ai.TriageVersion, Origin: "demo", Signals: []triageSignal{}}
	revision := 0
	bad := recommendationUnavailable("Supporting evidence is missing, incomplete, expired, oversized or unavailable from its source.")
	if batch == "" {
		out.IDs = []string{"synthetic-evidence"}
		payload.Signals = []triageSignal{{ID: "synthetic-finding", Detector: "error_rate", Severity: "warning", Current: .12, Baseline: .01, Unit: "error_proportion", CurrentMinutes: 15, BaselineMinutes: 60, EvidenceIDs: out.IDs}}
	} else {
		var sid, state string
		var mapping int
		var created time.Time
		if e := q.QueryRow(ctx, `SELECT service_id::text,state,policy_revision,mapping_revision,created_at FROM automation_batches WHERE workspace_id=$1 AND id::text=$2`, ws, batch).Scan(&sid, &state, &revision, &mapping, &created); e != nil {
			return out, e
		}
		policy, e := loadAutomationPolicy(ctx, q, ws)
		if e != nil {
			return out, e
		}
		v, e := scanService(q.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id=$2`, ws, sid))
		if e != nil {
			return out, e
		}
		providerAllowed := policy.TriageProviderID == p.ID && policy.Mode != "off"
		if automatic {
			providerAllowed = policy.Mode == "auto_investigate" && policy.AutomaticConsent && policy.InvestigationProviderID == p.ID
		}
		switch {
		case state != "recommended":
			return out, recommendationUnavailable("Local rules did not recommend investigation.")
		case time.Since(created) > 15*time.Minute:
			return out, recommendationUnavailable("Recommendation expired after 15 minutes; old work is not replayed.")
		case revision != policy.Revision || !providerAllowed:
			return out, recommendationUnavailable("Automation policy, selected provider or recurring consent changed.")
		case !slices.Contains(policy.Services, sid):
			return out, recommendationUnavailable("Service is no longer selected for automation.")
		case !v.Monitoring:
			return out, recommendationUnavailable("Scheduled monitoring is disabled for this service.")
		case mapping != v.Revision:
			return out, recommendationUnavailable("Service mapping changed; a fresh observation is required.")
		case v.Origin == "demo" && !policy.AllowDemo:
			return out, recommendationUnavailable("Simulated demo findings are excluded from automation.")
		case !aiAllowed(p, sid, v.Origin):
			return out, recommendationUnavailable("Provider service or demo consent does not allow this evidence.")
		}
		out.Service = v
		payload.Origin = v.Origin
		payload.ServiceID = v.ID
		payload.MappingRevision = v.Revision
		cfg, e := loadDetectorSettings(ctx, q, ws)
		if e != nil {
			return out, e
		}
		var raw []byte
		e = q.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',i.finding_id,'at',i.observed_at,'cleared',i.cleared_at,'measurement',i.measurement,'evidence',i.evidence_ids,'details',f.details,'state',f.state,'last',f.last_seen) ORDER BY i.finding_id),'[]'::jsonb) FROM automation_items i JOIN findings f ON f.workspace_id=i.workspace_id AND f.id=i.finding_id WHERE i.workspace_id=$1 AND i.batch_id=$2 AND i.recommendation='investigate'`, ws, batch).Scan(&raw)
		if e != nil {
			return out, e
		}
		var items []struct {
			ID          string
			At          time.Time
			Cleared     *time.Time
			Measurement automation.Measurement
			Evidence    []string
			Details     detectors.Candidate
			State       string
			Last        time.Time
		}
		if json.Unmarshal(raw, &items) != nil || len(items) == 0 || len(items) > 8 {
			return out, bad
		}
		seen := map[string]bool{}
		for _, i := range items {
			c := i.Details
			current, valid := automation.Measure(detectors.Candidate{Detector: c.Detector, Thresholds: cfg})
			switch {
			case !valid || current.Key != i.Measurement.Key || slices.Contains(cfg.Disabled, c.Detector):
				return out, recommendationUnavailable("Monitoring detector, windows or thresholds changed.")
			case i.Cleared != nil:
				return out, recommendationUnavailable("A newer complete scan no longer qualifies for this signal.")
			case i.Last.After(i.At):
				return out, recommendationUnavailable("A newer finding observation superseded this recommendation.")
			case time.Since(i.At) > 15*time.Minute:
				return out, recommendationUnavailable("Scan observation is older than 15 minutes.")
			case i.State != "new" && i.State != "acknowledged":
				return out, recommendationUnavailable("Finding is snoozed, dismissed or resolved.")
			case len(i.Evidence) == 0:
				return out, bad
			}
			baseline := c.Baseline
			if i.Measurement.Unit == "sampled_lines_per_minute" {
				baseline /= float64(c.Thresholds.CurrentMinutes)
			}
			payload.Signals = append(payload.Signals, triageSignal{i.ID, c.Detector, c.Severity, i.Measurement.Value, baseline, i.Measurement.Unit, c.Thresholds.CurrentMinutes, c.Thresholds.BaselineMinutes, i.At, i.Evidence})
			for _, id := range i.Evidence {
				if seen[id] {
					continue
				}
				seen[id] = true
				if len(seen) > 12 {
					return out, bad
				}
				var document []byte
				var expires time.Time
				e = q.QueryRow(ctx, `SELECT e.document,e.expires_at FROM evidence e JOIN connections c ON c.workspace_id=e.workspace_id AND c.id=e.connection_id WHERE e.workspace_id=$1 AND e.id::text=$2 AND e.service_id=$3 AND e.mapping_revision=$4 AND e.origin=$5 AND e.expires_at>now() AND c.enabled AND NOT c.deleted`, ws, id, v.ID, v.Revision, v.Origin).Scan(&document, &expires)
				var o connectors.Observation
				if e != nil || json.Unmarshal(document, &o) != nil || !o.Complete {
					return out, bad
				}
				out.IDs = append(out.IDs, id)
				out.Expires = minTime(out.Expires, expires)
			}
		}
	}
	var e error
	out.Payload, e = json.Marshal(payload)
	if e != nil {
		return out, e
	}
	wire, e := ai.RequestBody(p.Kind, p.Model, out.Payload, "triage")
	if e != nil {
		return out, e
	}
	out.Bound = len(wire) + 1024
	if !automatic && out.Bound > ai.TriageInputLimit {
		return out, errors.New("Triage summary exceeds the compact input limit; local recommendation remains available.")
	}
	sum := sha256.Sum256(append(wire, []byte(":"+strconv.Itoa(p.Revision)+":"+strconv.Itoa(revision))...))
	out.Digest = hex.EncodeToString(sum[:])
	return out, nil
}
