// Package inbound defines Sherlock's signed generic alert contract. It does not
// interpret external assertions as observed evidence or grant execution authority.
package inbound

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const Version = "inbound-v1"
const BodyLimit = 16 * 1024

var eventID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Alert struct {
	Version string    `json:"version"`
	EventID string    `json:"event_id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Title   string    `json:"title"`
}

func Signature(secret []byte, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(timestamp + "."))
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}
func Verify(secret []byte, timestamp, signature string, body []byte, now time.Time) bool {
	seconds, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil || strconv.FormatInt(seconds, 10) != timestamp || len(body) > BodyLimit {
		return false
	}
	at := time.Unix(seconds, 0)
	if at.Before(now.Add(-5*time.Minute)) || at.After(now.Add(5*time.Minute)) {
		return false
	}
	return hmac.Equal([]byte(Signature(secret, timestamp, body)), []byte(signature))
}
func Decode(body []byte) (Alert, error) {
	var a Alert
	bad := errors.New("Use the inbound-v1 alert schema with an event ID, optional title and explicit incident window; outbound Sherlock and native Alertmanager envelopes are not accepted.")
	if len(body) > BodyLimit {
		return a, bad
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.Version != Version || !eventID.MatchString(a.EventID) || len(a.Title) > 512 || strings.ContainsRune(a.Title, 0) {
		return Alert{}, bad
	}
	return a, nil
}
func ValidWindow(a Alert, baseline int, now time.Time) bool {
	span := a.End.Sub(a.Start)
	return !a.Start.IsZero() && !a.End.IsZero() && !a.End.After(now) && !a.End.Before(now.Add(-7*24*time.Hour)) && span >= 5*time.Minute && span <= time.Hour && span+time.Duration(baseline)*time.Minute <= 2*time.Hour
}
