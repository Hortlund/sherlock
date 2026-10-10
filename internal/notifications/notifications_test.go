package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRulesQuietHoursAndDST(t *testing.T) {
	r := Rules{Events: Events, Origins: []string{"live"}, Severities: []string{"notice", "warning"}, Timezone: "Europe/Stockholm", QuietStart: "22:00", QuietEnd: "07:00"}
	if e := r.Validate(); e != nil {
		t.Fatal(e)
	}
	if r.Matches("finding.created", "s", "demo", "warning") || !r.Matches("finding.created", "s", "live", "warning") {
		t.Fatal("origin filter")
	}
	for _, tc := range []struct{ now, want string }{
		{"2026-03-28T22:00:00Z", "2026-03-29T05:00:00Z"},
		{"2026-10-24T22:00:00Z", "2026-10-25T06:00:00Z"},
		{"2026-10-10T10:00:01Z", "2026-10-10T10:00:01Z"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := r.NextAllowed(now).UTC().Format(time.RFC3339); got != tc.want {
			t.Errorf("%s: %s", tc.now, got)
		}
	}
	r.QuietEnd = "22:00"
	if r.Validate() == nil {
		t.Fatal("whole day accepted")
	}
	r.QuietStart = ""
	if r.Validate() == nil {
		t.Fatal("partial quiet hours accepted")
	}
}
func TestPayloadMentionsAndSignature(t *testing.T) {
	events := []Event{{ID: "event-1", Type: "finding.created", Summary: "<!channel> @everyone <@123> & incident", URL: "https://sherlock.example/findings"}}
	for _, kind := range []string{"slack", "discord", "webhook"} {
		b, e := Payload(kind, "delivery-1", events)
		if e != nil {
			t.Fatal(e)
		}
		var v map[string]any
		if e = json.Unmarshal(b, &v); e != nil {
			t.Fatal(e)
		}
		if kind == "slack" {
			if v["mrkdwn"] != false || v["unfurl_links"] != false || strings.Contains(v["text"].(string), "<!channel>") {
				t.Fatal("slack mention controls")
			}
		}
		if kind == "discord" {
			if len(v["allowed_mentions"].(map[string]any)["parse"].([]any)) != 0 {
				t.Fatal("discord mentions")
			}
			if _, ok := v["flags"]; ok {
				t.Fatal("alerts must not silently suppress push notifications")
			}
		}
	}
	// Independent fixed vector: HMAC-SHA256("secret", "123.{}") from Python hashlib.
	if Signature("secret", "123", []byte("{}")) != "v1=4468c5e304ca107335ae2982d764fdf304214175c7dc3ad06921848d275abb92" {
		t.Fatal("signature vector changed")
	}
	if Signature("secret", "123", []byte("{}")) == Signature("secret", "124", []byte("{}")) {
		t.Fatal("timestamp not signed")
	}
}
func TestURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		kind, url string
		ok        bool
	}{
		{"slack", "https://hooks.slack.com/services/A/B/C", true}, {"slack", "https://evil.example/services/A/B/C", false},
		{"discord", "https://discord.com/api/webhooks/123/token?thread_id=123", true}, {"discord", "https://discord.com/api/webhooks/123/token?wait=false", false},
		{"webhook", "http://127.0.0.1/fixture", false}, {"webhook", "https://user:secret@example.com", false}, {"webhook", "https://example.com/#secret", false},
	} {
		if got := ValidateURL(tc.kind, tc.url, nil) == nil; got != tc.ok {
			t.Errorf("%s: %v", tc.kind, got)
		}
	}
	if ValidateURL("webhook", "http://127.0.0.1/fixture", map[string]bool{"127.0.0.1": true}) != nil {
		t.Fatal("explicit HTTP exception")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPlatformAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		kind, body     string
		status         int
		retry, unknown bool
		id             string
	}{
		{"slack", "ok", 200, false, false, ""}, {"slack", "invalid_payload", 400, false, false, ""}, {"slack", "not-ok", 200, true, true, ""},
		{"discord", `{"id":"12345"}`, 200, false, false, "12345"}, {"discord", "", 204, true, true, ""}, {"discord", `{"retry_after":2.5}`, 429, true, false, ""},
		{"slack", "secret response", 503, true, false, ""},
	} {
		t.Run(tc.kind+tc.body, func(t *testing.T) {
			s, _ := NewSender("", "", "")
			s.client = func() *http.Client {
				return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method != "POST" || (tc.kind == "discord" && r.URL.Query().Get("wait") != "true") {
						t.Error("invalid dispatch")
					}
					return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				})}
			}
			u := "https://hooks.slack.com/services/A/B/C"
			if tc.kind == "discord" {
				u = "https://discord.com/api/webhooks/123/token"
			}
			r := s.Send(context.Background(), tc.kind, Secret{URL: u}, "id", []byte("{}"))
			if r.Retry != tc.retry || r.Unknown != tc.unknown || r.MessageID != tc.id || strings.Contains(r.Detail, "secret response") {
				t.Fatalf("%+v", r)
			}
			if tc.status == 429 && r.After != 2500*time.Millisecond {
				t.Fatal("Discord retry_after ignored")
			}
		})
	}
}
func TestGenericDispatchNetworkAndSignature(t *testing.T) {
	calls := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Sherlock-Signature") != Signature("fixture-secret", r.Header.Get("X-Sherlock-Timestamp"), b) || r.Header.Get("X-Sherlock-Delivery-ID") != "delivery" {
			t.Error("unsigned body")
		}
		if calls == 1 {
			w.Header().Set("Location", "/redirect-secret")
			w.WriteHeader(302)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer receiver.Close()
	secret := Secret{URL: receiver.URL, SigningKey: "fixture-secret"}
	s, _ := NewSender("", "", "127.0.0.1")
	if r := s.Send(context.Background(), "webhook", secret, "delivery", []byte("{}")); r.Status != 0 || calls != 0 {
		t.Fatal("loopback policy bypass")
	}
	s, _ = NewSender("127.0.0.0/8", "", "127.0.0.1")
	if r := s.Send(context.Background(), "webhook", secret, "delivery", []byte("{}")); r.Status != 302 || r.Retry || calls != 1 {
		t.Fatalf("redirect followed: %+v", r)
	}
	if r := s.Send(context.Background(), "webhook", secret, "delivery", []byte("{}")); r.Status != 204 || r.Unknown {
		t.Fatal(r)
	}
}
