// Package automation evaluates local recommendation rules. It never calls a model
// or starts an investigation; those are separate, explicitly enabled capabilities.
package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sherlock/internal/detectors"
)

type Policy struct {
	ToolsEnabled            bool     `json:"tools_enabled"`
	InvestigationProviderID string   `json:"investigation_provider_id"`
	AutomaticConsent        bool     `json:"automatic_consent"`
	TriageProviderID        string   `json:"triage_provider_id"`
	Mode                    string   `json:"mode"`
	Services                []string `json:"services"`
	AllowDemo               bool     `json:"allow_demo"`
	CooldownMinutes         int      `json:"cooldown_minutes"`
	GrowthMultiplier        float64  `json:"growth_multiplier"`
	Revision                int      `json:"revision"`
}

func Defaults() Policy {
	return Policy{Mode: "off", Services: []string{}, CooldownMinutes: 30, GrowthMultiplier: 2}
}
func (p Policy) Valid() bool {
	return (p.Mode == "off" || p.Mode == "triage_only" || (p.Mode == "auto_investigate" && p.AutomaticConsent && p.InvestigationProviderID != "")) && len(p.InvestigationProviderID) <= 36 && p.CooldownMinutes >= 5 && p.CooldownMinutes <= 1440 && p.GrowthMultiplier >= 1.5 && p.GrowthMultiplier <= 10 && len(p.TriageProviderID) <= 36 && len(p.Services) <= 100 && p.Revision >= 0
}

type Measurement struct {
	Value        float64 `json:"value"`
	Unit         string  `json:"unit"`
	Key          string  `json:"comparison_key"`
	Severity     string  `json:"severity"`
	MinimumDelta float64 `json:"minimum_delta"`
}

func Measure(c detectors.Candidate) (Measurement, bool) {
	m := Measurement{Value: c.Current, Severity: c.Severity}
	if c.Current < 0 || math.IsNaN(c.Current) || math.IsInf(c.Current, 0) || !c.Thresholds.Valid() {
		return m, false
	}
	switch c.Detector {
	case "error_rate":
		if c.Current > 1 {
			return m, false
		}
		m.Unit = "error_proportion"
		m.MinimumDelta = c.Thresholds.ErrorDelta
	case "new_exception":
		m.Unit = "sampled_lines_per_minute"
		m.Value /= float64(c.Thresholds.CurrentMinutes)
		m.MinimumDelta = float64(c.Thresholds.MinOccurrences) / float64(c.Thresholds.CurrentMinutes)
	case "warning_burst":
		m.Unit = "sampled_lines_per_minute"
		m.Value /= float64(c.Thresholds.CurrentMinutes)
		m.MinimumDelta = float64(c.Thresholds.MinWarnings) / float64(c.Thresholds.CurrentMinutes)
	default:
		return m, false
	}
	// Windows/thresholds affect comparability; scheduling and retention do not.
	config := c.Thresholds
	config.CadenceMinutes = 0
	config.RetentionDays = 0
	config.CooldownHours = 0
	raw, _ := json.Marshal(struct {
		Detector string
		Config   detectors.Config
	}{c.Detector, config})
	sum := sha256.Sum256(raw)
	m.Key = hex.EncodeToString(sum[:])
	return m, true
}
func Change(before, after Measurement, multiplier float64) string {
	if before.Key != after.Key || before.Unit != after.Unit {
		return "comparison_changed"
	}
	if before.Severity != after.Severity {
		return "severity_changed"
	}
	if after.Value > before.Value && after.Value >= before.Value*multiplier && after.Value-before.Value >= after.MinimumDelta {
		return "growth"
	}
	return ""
}
