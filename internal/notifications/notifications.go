// Package notifications defines bounded, deterministic notification payloads and routing.
package notifications

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
)

var Events = []string{"finding.created", "finding.escalated", "finding.resolved", "investigation.completed"}

type Rules struct {
	Events     []string `json:"events"`
	Services   []string `json:"services"`
	Origins    []string `json:"origins"`
	Severities []string `json:"severities"`
	Immediate  bool     `json:"immediate"`
	QuietStart string   `json:"quiet_start"`
	QuietEnd   string   `json:"quiet_end"`
	Timezone   string   `json:"timezone"`
}

func (r Rules) Validate() error {
	if len(r.Events) == 0 || len(r.Events) > 4 || len(r.Origins) == 0 || len(r.Origins) > 2 || len(r.Severities) == 0 || len(r.Severities) > 2 || len(r.Services) > 100 {
		return errors.New("Choose events, origins and severities within the supported limits.")
	}
	for _, pair := range []struct{ values, allowed []string }{{r.Events, Events}, {r.Origins, []string{"live", "demo"}}, {r.Severities, []string{"notice", "warning"}}} {
		for _, value := range pair.values {
			if !slices.Contains(pair.allowed, value) {
				return errors.New("Unknown notification filter.")
			}
		}
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil || r.Timezone == "" || r.Timezone == "Local" {
		return errors.New("Choose an IANA timezone, such as Europe/Stockholm.")
	}
	if (r.QuietStart == "") != (r.QuietEnd == "") {
		return errors.New("Set both quiet-hour times or leave both empty.")
	}
	if r.QuietStart != "" {
		if _, e := time.Parse("15:04", r.QuietStart); e != nil || len(r.QuietStart) != 5 {
			return errors.New("Quiet hours must use HH:MM.")
		}
		if _, e := time.Parse("15:04", r.QuietEnd); e != nil || len(r.QuietEnd) != 5 {
			return errors.New("Quiet hours must use HH:MM.")
		}
		if r.QuietStart == r.QuietEnd {
			return errors.New("Quiet-hour start and end must differ.")
		}
	}
	return nil
}
func (r Rules) Matches(kind, service, origin, severity string) bool {
	return slices.Contains(r.Events, kind) && slices.Contains(r.Origins, origin) && slices.Contains(r.Severities, severity) && (len(r.Services) == 0 || slices.Contains(r.Services, service))
}

// Search actual local minutes so DST gaps and repeated hours never resolve to a past instant.
func (r Rules) NextAllowed(now time.Time) time.Time {
	if r.QuietStart == "" {
		return now
	}
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return now.Add(24 * time.Hour)
	}
	quiet := func(t time.Time) bool {
		v := t.In(loc).Format("15:04")
		if r.QuietStart < r.QuietEnd {
			return v >= r.QuietStart && v < r.QuietEnd
		}
		return v >= r.QuietStart || v < r.QuietEnd
	}
	if !quiet(now) {
		return now
	}
	t := now.Truncate(time.Minute).Add(time.Minute)
	for range 26 * 60 {
		if !quiet(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return t
}

type Secret struct {
	URL        string `json:"url"`
	SigningKey string `json:"signing_key"`
}
type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	At        time.Time `json:"at"`
	ServiceID string    `json:"service_id,omitempty"`
	FindingID string    `json:"finding_id,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	Origin    string    `json:"origin"`
	Severity  string    `json:"severity"`
	Summary   string    `json:"summary"`
	URL       string    `json:"url"`
}

// Summary text is generated from templates; only the short service display name is user data.
func Display(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return string([]rune(s)[:min(len([]rune(s)), 100)])
}
func Payload(kind, id string, events []Event) ([]byte, error) {
	if len(events) == 0 || len(events) > 10 {
		return nil, errors.New("A delivery requires 1–10 events.")
	}
	text := "Sherlock"
	for _, e := range events {
		text += "\n" + e.Summary + "\n" + e.URL + "\nEvent: " + e.ID
	}
	switch kind {
	case "slack":
		// Plain text blocks, escaped fallback, no automatic channel/user mentions or URL unfurls.
		fallback := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
		blocks := []any{}
		for _, event := range events {
			line := "Sherlock\n" + event.Summary + "\n" + event.URL + "\nEvent: " + event.ID
			if len([]rune(line)) > 2900 {
				return nil, errors.New("Notification exceeds Slack text limit.")
			}
			blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "plain_text", "text": line, "emoji": false}})
		}
		return json.Marshal(map[string]any{"text": fallback, "mrkdwn": false, "parse": "none", "link_names": 0, "unfurl_links": false, "unfurl_media": false, "blocks": blocks})
	case "discord":
		embeds := []any{}
		for _, e := range events {
			embeds = append(embeds, map[string]any{"title": "Sherlock · " + e.Type, "description": discordText(e.Summary), "url": e.URL, "footer": map[string]string{"text": "Event: " + e.ID}})
		}
		return json.Marshal(map[string]any{"embeds": embeds, "allowed_mentions": map[string]any{"parse": []string{}}})
	case "webhook":
		return json.Marshal(struct {
			Version int     `json:"version"`
			ID      string  `json:"delivery_id"`
			Events  []Event `json:"events"`
		}{1, id, events})
	}
	return nil, errors.New("Unknown destination type.")
}
func discordText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "~", "\\~", "`", "\\`", "|", "\\|", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", ">", "\\>").Replace(s)
}
func Signature(key, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(timestamp + "."))
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}

var slackPath = regexp.MustCompile(`^/services/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)
var discordPath = regexp.MustCompile(`^/api/(v[0-9]+/)?webhooks/[0-9]+/[A-Za-z0-9._-]+$`)

func ValidateURL(kind, raw string, httpHosts map[string]bool) error {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Port() != "" && u.Port() != "443" && kind != "webhook" {
		return errors.New("Invalid webhook URL.")
	}
	if u.Scheme != "https" && !(kind == "webhook" && u.Scheme == "http" && httpHosts[strings.ToLower(u.Hostname())]) {
		return errors.New("Webhook URLs require HTTPS; local HTTP receivers need a deployment exception.")
	}
	switch kind {
	case "slack":
		if u.Hostname() != "hooks.slack.com" || !slackPath.MatchString(u.Path) || u.RawQuery != "" {
			return errors.New("Use an official hooks.slack.com incoming webhook URL.")
		}
	case "discord":
		if u.Hostname() != "discord.com" || !discordPath.MatchString(u.Path) {
			return errors.New("Use an official discord.com webhook URL.")
		}
		q, e := url.ParseQuery(u.RawQuery)
		if e != nil {
			return errors.New("Invalid Discord query.")
		}
		for k, v := range q {
			if k != "thread_id" || len(v) != 1 {
				return errors.New("Only a Discord thread_id query is supported.")
			}
			if _, e := strconv.ParseUint(v[0], 10, 64); e != nil {
				return errors.New("Invalid Discord thread ID.")
			}
		}
	case "webhook":
	default:
		return fmt.Errorf("Unknown notification type.")
	}
	return nil
}
