package investigations

import (
	"fmt"
	"sherlock/internal/connectors"
	"sort"
	"time"
)

type Evidence struct {
	ID, WorkspaceID, ServiceID, Origin string
	MappingRevision                    int
	Observation                        connectors.Observation
	ExpiresAt                          time.Time
}
type Scope struct {
	WorkspaceID, ServiceID, Origin string
	MappingRevision                int
	Start, End, BaselineStart      time.Time
}
type Statement struct {
	Text        string    `json:"text"`
	EvidenceIDs []string  `json:"evidence_ids"`
	At          time.Time `json:"at"`
	Window      string    `json:"window"`
}
type Report struct {
	Summary      string      `json:"summary"`
	Observations []Statement `json:"observations"`
	Unknowns     []string    `json:"unknowns"`
	Checks       []string    `json:"checks"`
	Adequate     bool        `json:"adequate"`
}

// Build validates every citation's scope and window before constructing claims.
func Build(scope Scope, items []Evidence) (Report, error) {
	r := Report{Summary: "Underlying cause is undetermined. Observations below describe the available telemetry.", Observations: []Statement{}, Unknowns: []string{"No database, connection-pool, deployment, trace, or dependency-health evidence was collected. Temporal coincidence does not establish causality."}, Checks: []string{"Inspect connection-pool utilization and acquisition wait time during this window.", "Compare database reachability and server-side latency with the timeout timestamps.", "Check request volume and retry configuration; confirm the mapping selects the intended service.", "Review deployment history separately before attributing a change to a release."}}
	byWindow := map[string]map[string]Evidence{"incident": {}, "comparison": {}}
	for _, ev := range items {
		o := ev.Observation
		window := "incident"
		if ev.WorkspaceID != scope.WorkspaceID || ev.ServiceID != scope.ServiceID || ev.Origin != scope.Origin || ev.MappingRevision != scope.MappingRevision || time.Now().After(ev.ExpiresAt) {
			return Report{}, fmt.Errorf("evidence scope or retention mismatch")
		}
		if o.Start.Equal(scope.BaselineStart) && o.End.Equal(scope.Start) {
			window = "comparison"
		} else if !o.Start.Equal(scope.Start) || !o.End.Equal(scope.End) {
			return Report{}, fmt.Errorf("evidence query window mismatch")
		}
		byWindow[window][o.Kind] = ev
		qualifier := ""
		if !o.Complete {
			qualifier = "This result is incomplete; counts are lower bounds. "
		}
		if o.Kind == "log_patterns" {
			errors, warnings := 0, 0
			for _, p := range o.Errors {
				errors += p.Count
			}
			for _, p := range o.Warnings {
				warnings += p.Count
			}
			text := fmt.Sprintf("%s%d log lines were returned in the %s window, including %d configured error matches and %d warning matches.", qualifier, o.SampleCount, window, errors, warnings)
			r.Observations = append(r.Observations, Statement{text, []string{ev.ID}, o.End, window})
			if window == "incident" && o.Complete && o.SampleCount > 0 {
				r.Adequate = true
			}
			if o.SampleCount == 0 || o.CoverageFraction < .8 {
				r.Unknowns = append(r.Unknowns, "Log coverage is sparse in the "+window+" window; absence is not evidence of health.")
			}
		}
		if !o.Complete {
			r.Unknowns = append(r.Unknowns, "Incomplete "+o.Kind+" evidence in the "+window+" window cannot support an exact comparison.")
		}
	}
	for _, window := range []string{"comparison", "incident"} {
		m := byWindow[window]
		total, hasTotal := m["request_total"]
		errors, hasErrors := m["request_errors"]
		if hasTotal && hasErrors && total.Observation.Complete && errors.Observation.Complete && total.Observation.Value >= 100 && errors.Observation.Value <= total.Observation.Value {
			rate := errors.Observation.Value / total.Observation.Value
			r.Observations = append(r.Observations, Statement{fmt.Sprintf("Mapped request error proportion was %.2f%% (%.1f of %.1f requests) in the %s window.", rate*100, errors.Observation.Value, total.Observation.Value, window), []string{total.ID, errors.ID}, total.Observation.End, window})
			if window == "incident" {
				r.Adequate = true
			}
		} else {
			r.Unknowns = append(r.Unknowns, "The "+window+" request error proportion is unavailable or has insufficient traffic.")
		}
		if _, ok := m["log_patterns"]; !ok {
			r.Unknowns = append(r.Unknowns, "No usable logs were collected for the "+window+" window.")
		}
	}
	sort.SliceStable(r.Observations, func(i, j int) bool { return r.Observations[i].At.Before(r.Observations[j].At) })
	if !r.Adequate {
		r.Summary = "Insufficient evidence to characterize this incident. Review source access, mapping, and query coverage."
	}
	return r, nil
}
