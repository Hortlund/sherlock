package connectors

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactionPreservesV1FingerprintGoldens(t *testing.T) {
	// Fixed values from the original field-preserving v1 normalization, not
	// computed through the redactor under test.
	for _, tc := range []struct{ raw, redacted, fingerprint string }{
		{"error authentication password=example-value", "error authentication password=[redacted]", "v1:a79354cc23c009793a1f8c5788a8bf91b4ad0777efd42c609812690c91e5b65b"},
		{"error password=Authorization: Bearer private-token", "error password=[redacted]", "v1:aba6908b739a49bce183d6888095247f091af5ba29d1d4acf5df0d7407835205"},
		{"error Authorization: Bearer private-token", "error Authorization=[redacted]", "v1:a594f0e6f99588fef4ad9551cbab46abc79b5935752e0a14ae6e8592e13e6786"},
		{"error user owner@example.com request 123", "error user [email] request 123", "v1:17ecaf06c1054d41519f843c40ec57d4fa69d5a35b462f9f63b9cf442b5329a4"},
		{`{"token":"private-value"}`, `{"token=[redacted]"}`, "v1:f89de2b2c64f36a1af2b7866da5e7b13adb76cb42a12c27350238ba2e7ba9ef3"},
		{"error Cookie: session=private; password=private", "error Cookie=[redacted]; password=[redacted]", "v1:6813d9e81495d167b5f24d51150842b61cdee8ef72cca6fc5971413d4b7e53e8"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := Redact(tc.raw); got != tc.redacted {
				t.Fatalf("redaction: %q", got)
			}
			for _, input := range []string{tc.raw, tc.redacted} {
				if got := Fingerprint(input); got != tc.fingerprint {
					t.Fatalf("fingerprint: %s", got)
				}
			}
		})
	}
}

func TestRedactionOverlapAndMatchLimit(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		custom []string
		want   string
	}{
		{"error password=private", []string{"password"}, "error [redacted]"},
		{"error password=private tail", []string{"private tail"}, "error [redacted]"},
		{"error Authorization: Bearer token=private", nil, "error Authorization=[redacted]"},
		{"error alphabeta", []string{"alpha", "beta"}, "error [redacted][redacted]"},
		{"error password=private;id[secret]", []string{`id\[[^]]+\]`, "secret"}, "error password=[redacted];[redacted]"},
	} {
		var patterns []*regexp.Regexp
		for _, pattern := range tc.custom {
			patterns = append(patterns, regexp.MustCompile(pattern))
		}
		got, ok := redactWithPatterns(tc.raw, patterns)
		if !ok || got != tc.want {
			t.Fatalf("%q: %q, %v", tc.raw, got, ok)
		}
	}
	for _, n := range []int{4096, 4097} {
		got, ok := redactWithPatterns(strings.Repeat("x", n), []*regexp.Regexp{regexp.MustCompile("x")})
		if ok != (n == 4096) || strings.Contains(got, "x") {
			t.Fatalf("limit %d: %q, %v", n, got, ok)
		}
	}
	got := Redact(strings.Repeat("界", 200))
	if len(got) > 512 || !utf8.ValidString(got) {
		t.Fatal("invalid UTF-8 excerpt boundary")
	}
}
