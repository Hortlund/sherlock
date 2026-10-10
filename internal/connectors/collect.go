package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var LabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var MetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
var sensitive = regexp.MustCompile(`(?i)(authorization|password|passwd|secret|token|api[_-]?key|cookie)(["\s:=]+)([^\s,;"}]+)`)
var authHeader = regexp.MustCompile(`(?i)(authorization|cookie)(["\s:=]+)([^\r\n,;}]+)`)
var emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
var volatile = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F-]{27,}\b|\b\d+(?:\.\d+)*\b`)

// Match against the complete original line: truncation or an earlier replacement
// must not remove the context needed by a later sensitive-pattern match.
func redactWithPatterns(s string, custom []*regexp.Regexp) (string, bool) {
	type span struct {
		start, end  int
		replacement string
		custom      bool
	}
	patterns := append([]*regexp.Regexp{authHeader, sensitive, emailPattern}, custom...)
	spans := []span{}
	for kind, pattern := range patterns {
		matches := pattern.FindAllStringSubmatchIndex(s, 4097)
		if len(matches) > 4096 {
			return "[redacted: pattern match limit]", false
		}
		for _, match := range matches {
			if match[0] == match[1] {
				continue
			}
			replacement := "[redacted]"
			if kind < 2 {
				// Keep the original v1 field marker, including its case.
				replacement = s[match[2]:match[3]] + "=[redacted]"
			} else if kind == 2 {
				replacement = "[email]"
			}
			spans = append(spans, span{match[0], match[1], replacement, kind >= 3})
		}
	}
	// Outer built-in matches take precedence over nested built-in matches,
	// as authorization/cookie redaction did in v1. Custom overlap always hides
	// the whole union, since even a preserved field name might be sensitive.
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end > spans[j].end
		}
		return spans[i].start < spans[j].start
	})
	var out strings.Builder
	cursor := 0
	for i := 0; i < len(spans); {
		current := spans[i]
		i++
		for i < len(spans) && spans[i].start < current.end {
			next := spans[i]
			if current.custom || next.custom {
				current.replacement = "[redacted]"
			}
			current.custom = current.custom || next.custom
			current.end = max(current.end, next.end)
			i++
		}
		out.WriteString(s[cursor:current.start])
		out.WriteString(current.replacement)
		cursor = current.end
	}
	out.WriteString(s[cursor:])
	result := out.String()
	if len(result) > 512 {
		end := 509
		for !utf8.RuneStart(result[end]) {
			end--
		}
		result = result[:end] + "…"
	}
	return result, true
}
func Redact(s string) string {
	result, _ := redactWithPatterns(s, nil)
	return result
}
func Fingerprint(s string) string {
	normalized := volatile.ReplaceAllString(strings.ToLower(Redact(s)), "<value>")
	digest := sha256.Sum256([]byte(normalized))
	return "v1:" + hex.EncodeToString(digest[:])
}

type Mapping struct {
	Labels            map[string]string `json:"labels,omitempty"` // Legacy shared selector; retained for stored snapshots.
	PrometheusLabels  map[string]string `json:"prometheus_labels,omitempty"`
	LokiLabels        map[string]string `json:"loki_labels,omitempty"`
	Metric            string            `json:"metric"`
	ErrorLabels       map[string]string `json:"error_labels"`
	ErrorPattern      string            `json:"error_pattern"`
	WarningPattern    string            `json:"warning_pattern"`
	SensitivePatterns []string          `json:"sensitive_patterns,omitempty"`
	StoreExcerpts     bool              `json:"store_excerpts"`
}

func (m Mapping) MetricsLabels() map[string]string {
	if m.PrometheusLabels != nil {
		return m.PrometheusLabels
	}
	return m.Labels
}
func (m Mapping) LogsLabels() map[string]string {
	if m.LokiLabels != nil {
		return m.LokiLabels
	}
	return m.Labels
}

// Normalize legacy and explicit selectors so representation-only edits do not
// change mapping identity. Clear settings for unconfigured sources.
func (m Mapping) Canonical(hasMetrics, hasLogs bool) Mapping {
	if hasMetrics {
		m.PrometheusLabels = m.MetricsLabels()
	} else {
		m.PrometheusLabels = nil
		m.Metric = ""
		m.ErrorLabels = nil
	}
	if hasLogs {
		m.LokiLabels = m.LogsLabels()
	} else {
		m.LokiLabels = nil
		m.ErrorPattern = ""
		m.WarningPattern = ""
		m.SensitivePatterns = nil
		m.StoreExcerpts = false
	}
	m.Labels = nil
	if len(m.SensitivePatterns) == 0 {
		m.SensitivePatterns = nil
	}
	return m
}

func (m Mapping) Validate(hasMetrics, hasLogs bool) error {
	if len(m.SensitivePatterns) > 10 {
		return errors.New("Use at most ten redaction patterns.")
	}
	for _, p := range m.SensitivePatterns {
		if len(p) > 200 {
			return errors.New("Redaction pattern too long.")
		}
		if _, e := regexp.Compile(p); e != nil {
			return errors.New("Invalid redaction pattern.")
		}
	}
	if hasMetrics {
		if _, e := Selector(m.MetricsLabels()); e != nil {
			return e
		}
		if !MetricName.MatchString(m.Metric) || len(m.Metric) > 150 {
			return errors.New("Choose a counter metric with a valid metric name.")
		}
		if len(m.ErrorLabels) == 0 {
			return errors.New("Choose the labels identifying error responses.")
		}
		if _, e := Selector(m.ErrorLabels); e != nil {
			return e
		}
		for k, v := range m.ErrorLabels {
			if old, ok := m.MetricsLabels()[k]; ok && old != v {
				return errors.New("Error labels conflict with service labels.")
			}
		}
	}
	if hasLogs {
		if _, e := Selector(m.LogsLabels()); e != nil {
			return e
		}
		for _, pattern := range []string{m.ErrorPattern, m.WarningPattern} {
			if len(pattern) == 0 || len(pattern) > 200 {
				return errors.New("Error and warning patterns must contain 1–200 characters.")
			}
			if _, e := regexp.Compile(pattern); e != nil {
				return errors.New("Invalid error or warning pattern.")
			}
		}
	}
	return nil
}

type Pattern struct {
	Fingerprint string    `json:"fingerprint"`
	Count       int       `json:"count"`
	Sample      string    `json:"sample,omitempty"`
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
}
type Observation struct {
	Kind             string    `json:"kind"`
	Query            string    `json:"query"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	Complete         bool      `json:"complete"`
	CoverageFraction float64   `json:"coverage_fraction"`
	SampleCount      int       `json:"sample_count"`
	Value            float64   `json:"value"`
	Unit             string    `json:"unit"`
	Errors           []Pattern `json:"errors,omitempty"`
	Warnings         []Pattern `json:"warnings,omitempty"`
	Notes            string    `json:"notes"`
	RedactionVersion string    `json:"redaction_version"`
}

func (p *Policy) Count(ctx context.Context, s Source, m Mapping, start, end time.Time, errorCount bool) (Observation, error) {
	o := Observation{Kind: "request_total", Start: start, End: end, Unit: "requests", RedactionVersion: "v2"}
	labels := map[string]string{}
	for k, v := range m.MetricsLabels() {
		labels[k] = v
	}
	if errorCount {
		o.Kind = "request_errors"
		for k, v := range m.ErrorLabels {
			labels[k] = v
		}
	}
	selector, err := Selector(labels)
	if err != nil {
		return o, err
	}
	seconds := int(end.Sub(start).Seconds())
	if seconds < 60 || seconds > 7200 {
		return o, errors.New("invalid query window")
	}
	o.Query = "sum(increase(" + m.Metric + selector + "[" + strconv.Itoa(seconds) + "s]))"
	o.Query += " and on() (min(count_over_time(" + m.Metric + selector + "[" + strconv.Itoa(seconds) + "s])) >= " + strconv.Itoa(seconds/75) + ") and on() (min(timestamp(" + m.Metric + selector + ")) > " + strconv.FormatInt(end.Add(-2*time.Minute).Unix(), 10) + ")"
	var env Envelope
	err = p.Get(ctx, s, "/api/v1/query", url.Values{"query": {o.Query}, "time": {strconv.FormatInt(end.Unix(), 10)}, "timeout": {"10s"}}, &env)
	if err != nil {
		return o, err
	}
	var data struct {
		Type   string `json:"resultType"`
		Result []struct {
			Value []json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if env.Status != "success" || json.Unmarshal(env.Data, &data) != nil || data.Type != "vector" || len(data.Result) != 1 || len(data.Result[0].Value) != 2 {
		return o, &Error{"insufficient_signal", "No usable request counter was found for this mapping."}
	}
	var value string
	if json.Unmarshal(data.Result[0].Value[1], &value) != nil {
		return o, &Error{"invalid_response", "Invalid counter response."}
	}
	o.Value, err = strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(o.Value) || math.IsInf(o.Value, 0) || o.Value < 0 {
		return o, &Error{"insufficient_signal", "Counter result is not a finite nonnegative value."}
	}
	o.Complete = len(env.Warnings) == 0
	o.SampleCount = 1
	if !o.Complete {
		o.Notes = "Source returned query warnings; this observation is incomplete."
	}
	return o, nil
}
func (p *Policy) Logs(ctx context.Context, s Source, m Mapping, start, end time.Time) (Observation, error) {
	o := Observation{Kind: "log_patterns", Start: start, End: end, Unit: "log lines", RedactionVersion: "v2"}
	selector, err := Selector(m.LogsLabels())
	if err != nil {
		return o, err
	}
	o.Query = selector
	if end.Sub(start) < time.Minute || end.Sub(start) > 2*time.Hour {
		return o, errors.New("invalid query window")
	}
	var env Envelope
	err = p.Get(ctx, s, "/loki/api/v1/query_range", url.Values{"query": {selector}, "start": {strconv.FormatInt(start.UnixNano(), 10)}, "end": {strconv.FormatInt(end.Add(-time.Nanosecond).UnixNano(), 10)}, "limit": {"2000"}, "direction": {"forward"}}, &env)
	if err != nil {
		return o, err
	}
	var data struct {
		Type   string `json:"resultType"`
		Result []struct {
			Values [][]json.RawMessage `json:"values"`
		} `json:"result"`
	}
	if env.Status != "success" || json.Unmarshal(env.Data, &data) != nil || data.Type != "streams" {
		return o, &Error{"invalid_response", "Expected Loki log streams."}
	}
	errorRE, err := regexp.Compile(m.ErrorPattern)
	if err != nil {
		return o, err
	}
	warningRE, err := regexp.Compile(m.WarningPattern)
	if err != nil {
		return o, err
	}
	redactors := []*regexp.Regexp{}
	for _, pattern := range m.SensitivePatterns {
		compiled, e := regexp.Compile(pattern)
		if e != nil {
			return o, e
		}
		redactors = append(redactors, compiled)
	}
	groups := []map[string]*Pattern{{}, {}}
	var first, last time.Time
	for _, stream := range data.Result {
		for _, pair := range stream.Values {
			if err := ctx.Err(); err != nil {
				return o, err
			}
			if len(pair) < 2 {
				return o, &Error{"invalid_response", "Invalid log entry."}
			}
			var ts, line string
			if json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &line) != nil {
				return o, &Error{"invalid_response", "Invalid log entry."}
			}
			n, e := strconv.ParseInt(ts, 10, 64)
			at := time.Unix(0, n)
			if e != nil || at.Before(start) || !at.Before(end) {
				return o, &Error{"invalid_response", "Log timestamp lies outside the requested window."}
			}
			if first.IsZero() || at.Before(first) {
				first = at
			}
			if last.IsZero() || at.After(last) {
				last = at
			}
			o.SampleCount++
			if o.SampleCount > 2000 {
				return o, &Error{"response_limit", "Source exceeded the log sample limit."}
			}
			idx := -1
			if errorRE.MatchString(line) {
				idx = 0
			} else if warningRE.MatchString(line) {
				idx = 1
			}
			if idx < 0 {
				continue
			}
			var redacted bool
			line, redacted = redactWithPatterns(line, redactors)
			if !redacted {
				o.Notes = "Log lines withheld after the redaction match limit; pattern evidence is incomplete."
				continue
			}
			fp := Fingerprint(line)
			g := groups[idx][fp]
			if g == nil {
				if len(groups[idx]) >= 100 {
					o.Notes = "Fingerprint cardinality limit reached."
					continue
				}
				sample := ""
				if m.StoreExcerpts {
					sample = Redact(line)
				}
				g = &Pattern{Fingerprint: fp, Sample: sample, First: at, Last: at}
				groups[idx][fp] = g
			}
			g.Count++
			if at.Before(g.First) {
				g.First = at
			}
			if at.After(g.Last) {
				g.Last = at
			}
		}
	}
	for _, g := range groups[0] {
		o.Errors = append(o.Errors, *g)
	}
	for _, g := range groups[1] {
		o.Warnings = append(o.Warnings, *g)
	}
	if !first.IsZero() {
		o.CoverageFraction = last.Sub(first).Seconds() / end.Sub(start).Seconds()
	}
	o.Complete = o.SampleCount < 2000 && len(env.Warnings) == 0 && o.Notes == ""
	if !o.Complete && o.Notes == "" {
		o.Notes = "Log sample may be truncated; counts are lower bounds."
	}
	if o.SampleCount == 0 {
		o.Notes = "No logs in this window; coverage is unknown."
	}
	return o, nil
}

// RedactText applies the same bounded full-line policy to AI inputs and generated text.
func RedactText(text string, patterns []*regexp.Regexp) string {
	result, _ := redactWithPatterns(text, patterns)
	return result
}
