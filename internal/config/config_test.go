package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestConfigSecureDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("SHERLOCK_SETUP_TOKEN", strings.Repeat("t", 32))
	t.Setenv("SHERLOCK_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, origin := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "https://sherlock.example.com"} {
		t.Setenv("SHERLOCK_ORIGIN", origin)
		c, e := Load()
		if e != nil {
			t.Fatal(e)
		}
		if c.SecureCookies != strings.HasPrefix(origin, "https:") {
			t.Fatal("bad cookie security")
		}
	}
	for _, origin := range []string{"http://sherlock.example.com", "https://user:pass@example.com", "https://example.com/path", "file:///tmp/x"} {
		t.Setenv("SHERLOCK_ORIGIN", origin)
		if _, e := Load(); e == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
	t.Setenv("SHERLOCK_ORIGIN", "http://localhost:8080")
	t.Setenv("SHERLOCK_ENCRYPTION_KEY", "")
	if _, e := Load(); e == nil {
		t.Fatal("missing key accepted")
	}
}
