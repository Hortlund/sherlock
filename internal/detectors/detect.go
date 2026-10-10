// Package detectors contains deterministic, conservative comparison rules.
package detectors

import (
	"fmt"
	"math"
	"sherlock/internal/connectors"
)

type Config struct {
	CadenceMinutes    int      `json:"cadence_minutes"`
	CurrentMinutes    int      `json:"current_minutes"`
	BaselineMinutes   int      `json:"baseline_minutes"`
	RetentionDays     int      `json:"retention_days"`
	CooldownHours     int      `json:"cooldown_hours"`
	MinOccurrences    int      `json:"min_occurrences"`
	MinWarnings       int      `json:"min_warnings"`
	WarningMultiplier float64  `json:"warning_multiplier"`
	MinTraffic        float64  `json:"min_traffic"`
	ErrorDelta        float64  `json:"error_delta"`
	ErrorMultiplier   float64  `json:"error_multiplier"`
	Disabled          []string `json:"disabled"`
}

func Defaults() Config { return Config{5, 15, 60, 7, 24, 3, 10, 3, 100, .02, 2, []string{}} }
func (c Config) Valid() bool {
	return c.CadenceMinutes >= 5 && c.CadenceMinutes <= 60 && c.CurrentMinutes >= 5 && c.CurrentMinutes <= 60 && c.BaselineMinutes >= 15 && c.CurrentMinutes+c.BaselineMinutes <= 120 && c.RetentionDays >= 1 && c.RetentionDays <= 30 && c.CooldownHours >= 1 && c.CooldownHours <= 168 && c.MinOccurrences >= 2 && c.MinOccurrences <= 1000 && c.MinWarnings >= 3 && c.MinWarnings <= 1000 && c.WarningMultiplier >= 2 && c.WarningMultiplier <= 100 && c.MinTraffic >= 20 && c.MinTraffic <= 1e9 && c.ErrorDelta >= .001 && c.ErrorDelta <= 1 && c.ErrorMultiplier >= 1.1 && c.ErrorMultiplier <= 100 && len(c.Disabled) <= 3
}

type Input struct {
	ID          string
	Observation connectors.Observation
}
type Candidate struct {
	Detector    string   `json:"detector"`
	Signal      string   `json:"signal"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Severity    string   `json:"severity"`
	EvidenceIDs []string `json:"evidence_ids"`
	Current     float64  `json:"current"`
	Baseline    float64  `json:"baseline"`
	Thresholds  Config   `json:"thresholds"`
}
type Coverage struct {
	Detector string `json:"detector"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}
type Result struct {
	Candidates []Candidate `json:"candidates"`
	Coverage   []Coverage  `json:"coverage"`
}

func Evaluate(current, baseline []Input, c Config) Result {
	r := Result{Candidates: []Candidate{}, Coverage: []Coverage{}}
	find := func(a []Input, k string) *Input {
		for i := range a {
			if a[i].Observation.Kind == k {
				return &a[i]
			}
		}
		return nil
	}
	for _, d := range []string{"new_exception", "warning_burst", "error_rate"} {
		disabled := false
		for _, v := range c.Disabled {
			if v == d {
				disabled = true
			}
		}
		coverage := Coverage{d, "insufficient_signal", "Provide complete current and comparison observations with adequate volume."}
		if disabled {
			coverage.Status = "disabled"
			coverage.Reason = "Disabled in detector settings."
			r.Coverage = append(r.Coverage, coverage)
			continue
		}
		if d == "error_rate" {
			ct, ce, bt, be := find(current, "request_total"), find(current, "request_errors"), find(baseline, "request_total"), find(baseline, "request_errors")
			if ct != nil && ce != nil && bt != nil && be != nil && ct.Observation.Complete && ce.Observation.Complete && bt.Observation.Complete && be.Observation.Complete && ct.Observation.Value >= c.MinTraffic && bt.Observation.Value >= c.MinTraffic && ce.Observation.Value <= ct.Observation.Value && be.Observation.Value <= bt.Observation.Value {
				coverage.Status = "active"
				coverage.Reason = "Fresh counters and minimum traffic in both windows."
				now, old := ce.Observation.Value/ct.Observation.Value, be.Observation.Value/bt.Observation.Value
				if now-old >= c.ErrorDelta && now >= old*c.ErrorMultiplier {
					r.Candidates = append(r.Candidates, Candidate{d, "request_errors", "Request error proportion increased", fmt.Sprintf("Mapped errors were %.2f%% of requests, compared with %.2f%% in the comparison window. This does not establish a cause.", now*100, old*100), "warning", []string{ct.ID, ce.ID, bt.ID, be.ID}, now, old, c})
				}
			}
		} else {
			a, b := find(current, "log_patterns"), find(baseline, "log_patterns")
			if a != nil && b == nil {
				coverage.Status = "warming_up"
				coverage.Reason = "A sufficiently covered comparison window is not available yet."
			}
			if a != nil && b != nil && a.Observation.Complete && b.Observation.Complete && a.Observation.SampleCount >= 10 && b.Observation.SampleCount >= 10 && a.Observation.CoverageFraction >= .8 && b.Observation.CoverageFraction >= .8 {
				coverage.Status = "active"
				coverage.Reason = "Complete nonempty log samples in both windows; absence outside these windows is unknown."
				if d == "new_exception" {
					for _, p := range a.Observation.Errors {
						seen := false
						for _, q := range b.Observation.Errors {
							if p.Fingerprint == q.Fingerprint {
								seen = true
							}
						}
						if !seen && p.Count >= c.MinOccurrences {
							r.Candidates = append(r.Candidates, Candidate{d, p.Fingerprint, "Error pattern new in comparison window", fmt.Sprintf("An error pattern occurred %d times in the current window and was absent from the available comparison logs. Underlying cause is undetermined.", p.Count), "warning", []string{a.ID, b.ID}, float64(p.Count), 0, c})
						}
					}
				} else {
					n, o := 0, 0
					for _, p := range a.Observation.Warnings {
						n += p.Count
					}
					for _, p := range b.Observation.Warnings {
						o += p.Count
					}
					expected := float64(o) * a.Observation.End.Sub(a.Observation.Start).Seconds() / b.Observation.End.Sub(b.Observation.Start).Seconds()
					if n >= c.MinWarnings && float64(n) >= math.Max(1, expected)*c.WarningMultiplier {
						r.Candidates = append(r.Candidates, Candidate{d, "warnings", "Warning volume increased", fmt.Sprintf("%d warning lines compared with %.1f expected at the comparison-window rate; total sampled logs: %d versus %d. An outage is not established.", n, expected, a.Observation.SampleCount, b.Observation.SampleCount), "notice", []string{a.ID, b.ID}, float64(n), expected, c})
					}
				}
			}
		}
		r.Coverage = append(r.Coverage, coverage)
	}
	return r
}
