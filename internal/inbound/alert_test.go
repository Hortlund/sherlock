package inbound

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignatureAndContract(t *testing.T) {
	now := time.Unix(1800000000, 0)
	secret := []byte(strings.Repeat("s", 32))
	stamp := strconv.FormatInt(now.Unix(), 10)
	a := Alert{Version: Version, EventID: "monitor:42", Start: now.Add(-20 * time.Minute), End: now.Add(-5 * time.Minute), Title: "password=untrusted"}
	body, _ := json.Marshal(a)
	sig := Signature(secret, stamp, body)
	if !Verify(secret, stamp, sig, body, now) {
		t.Fatal("valid signature rejected")
	}
	for _, tc := range []struct {
		name, stamp, sig string
		body             []byte
	}{
		{"stale", strconv.FormatInt(now.Add(-301*time.Second).Unix(), 10), sig, body},
		{"future", strconv.FormatInt(now.Add(301*time.Second).Unix(), 10), sig, body},
		{"changed", stamp, sig, append(body, ' ')}, {"bad_signature", stamp, "v1=wrong", body},
		{"oversized", stamp, sig, []byte(strings.Repeat("x", BodyLimit+1))}, {"noncanonical_timestamp", "+" + stamp, sig, body},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if Verify(secret, tc.stamp, tc.sig, tc.body, now) {
				t.Fatal("accepted invalid signature")
			}
		})
	}
	if _, e := Decode(body); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{`{"version":"1","event_id":"x","type":"investigation.completed"}`, `{"version":"inbound-v1","event_id":"x","query":"up"}`, `{"version":"inbound-v1","event_id":"bad id"}`, string(body) + `{}`} {
		if _, e := Decode([]byte(raw)); e == nil {
			t.Fatal("accepted unauthorized envelope", raw)
		}
	}
	if !ValidWindow(a, 60, now) {
		t.Fatal("supported window rejected")
	}
	for _, span := range []time.Duration{-time.Minute, 4 * time.Minute, 61 * time.Minute} {
		a.Start = a.End.Add(-span)
		if ValidWindow(a, 60, now) {
			t.Fatal("unsupported span")
		}
	}
	a.Start = now.Add(-9 * 24 * time.Hour)
	a.End = a.Start.Add(15 * time.Minute)
	if ValidWindow(a, 60, now) {
		t.Fatal("stale incident")
	}
}
