package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"sherlock/internal/connectors"
	"strconv"
	"strings"
	"time"
)

type Sender struct {
	Policy    *connectors.Policy
	HTTPHosts map[string]bool
	client    func() *http.Client // Optional transport factory for deterministic contract tests.
}

func NewSender(cidrs, hosts, httpHosts string) (*Sender, error) {
	p, e := connectors.NewPolicy(cidrs, hosts)
	if e != nil {
		return nil, e
	}
	s := &Sender{Policy: p, HTTPHosts: map[string]bool{}}
	for _, h := range strings.Split(httpHosts, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			s.HTTPHosts[h] = true
		}
	}
	return s, nil
}

type Result struct {
	Status    int
	Retry     bool
	Unknown   bool
	After     time.Duration
	MessageID string
	Detail    string
}

func RetryAfter(value string, now time.Time) time.Duration {
	if n, e := strconv.ParseFloat(value, 64); e == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 {
		return time.Duration(math.Min(n, 86400) * float64(time.Second))
	}
	if t, e := http.ParseTime(value); e == nil && t.After(now) {
		return min(t.Sub(now), 24*time.Hour)
	}
	return 0
}
func (s *Sender) Send(ctx context.Context, kind string, secret Secret, id string, body []byte) Result {
	if ValidateURL(kind, secret.URL, s.HTTPHosts) != nil || len(body) > 32*1024 {
		return Result{Detail: "Destination or payload is invalid."}
	}
	u, _ := url.Parse(secret.URL)
	if kind == "discord" {
		q := u.Query()
		q.Set("wait", "true")
		u.RawQuery = q.Encode()
	}
	var client *http.Client
	if s.client != nil {
		client = s.client()
	} else {
		client = s.Policy.HTTPClient(10 * time.Second)
	}
	defer client.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(body))
	if e != nil {
		return Result{Detail: "Invalid destination."}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sherlock/0.2")
	if kind == "webhook" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set("X-Sherlock-Timestamp", ts)
		req.Header.Set("X-Sherlock-Signature", Signature(secret.SigningKey, ts, body))
		req.Header.Set("X-Sherlock-Delivery-ID", id)
	}
	res, e := client.Do(req)
	if e != nil {
		return Result{Retry: true, Unknown: true, Detail: "No acknowledgement: check network policy, TLS, or connectivity. The receiver may have accepted the message."}
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 64*1024+1))
	r := Result{Status: res.StatusCode}
	if res.StatusCode == 429 || res.StatusCode >= 500 {
		r.Retry = true
		r.After = RetryAfter(res.Header.Get("Retry-After"), time.Now())
		r.Detail = "Receiver temporarily unavailable or rate limited."
		if kind == "discord" && r.After == 0 {
			var v struct {
				RetryAfter float64 `json:"retry_after"`
			}
			if json.Unmarshal(b, &v) == nil {
				r.After = RetryAfter(strconv.FormatFloat(v.RetryAfter, 'f', -1, 64), time.Now())
			}
		}
		return r
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		r.Detail = "Receiver rejected the request; check destination configuration."
		return r
	}
	if e != nil || len(b) > 64*1024 {
		r.Retry = true
		r.Unknown = true
		r.Detail = "Acknowledgement could not be read; delivery outcome is unknown."
		return r
	}
	if kind == "slack" && (res.StatusCode != 200 || strings.TrimSpace(string(b)) != "ok") {
		r.Unknown = true
		r.Retry = true
		r.Detail = "Slack did not return its expected acknowledgement."
		return r
	}
	if kind == "discord" {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(b, &v) != nil || v.ID == "" || len(v.ID) > 32 {
			r.Unknown = true
			r.Retry = true
			r.Detail = "Discord did not confirm a message ID."
			return r
		}
		for _, c := range v.ID {
			if c < '0' || c > '9' {
				r.Unknown = true
				r.Retry = true
				r.Detail = "Discord returned an invalid message ID."
				return r
			}
		}
		r.MessageID = v.ID
	}
	r.Detail = "Receiver acknowledged delivery."
	return r
}
