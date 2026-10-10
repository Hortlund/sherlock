package crypto

import (
	"bytes"
	"testing"
)

func TestAuthenticatedEncryption(t *testing.T) {
	v, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	one := v.Seal([]byte("private-token"), "workspace:connection")
	two := v.Seal([]byte("private-token"), "workspace:connection")
	if bytes.Equal(one, two) {
		t.Fatal("nonce reused")
	}
	plain, err := v.Open(one, "workspace:connection")
	if err != nil || string(plain) != "private-token" {
		t.Fatal("round trip failed")
	}
	if _, err = v.Open(one, "other-workspace:connection"); err == nil {
		t.Fatal("wrong scope accepted")
	}
	one[len(one)-1] ^= 1
	if _, err = v.Open(one, "workspace:connection"); err == nil {
		t.Fatal("tampering accepted")
	}
	if _, err = New([]byte("short")); err == nil {
		t.Fatal("invalid key accepted")
	}
}
