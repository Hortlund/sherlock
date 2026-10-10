package auth

import "testing"

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("a long test password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("a long test password", hash) {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword("a wrong test password", hash) {
		t.Fatal("incorrect password accepted")
	}
	for _, bad := range []string{"", "$argon2id$v=19$m=999999999,t=999,p=1$aaa$bbb", hash + "$extra"} {
		if VerifyPassword("a long test password", bad) {
			t.Fatal("malformed hash accepted")
		}
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}
func TestTokens(t *testing.T) {
	a, b := Token(), Token()
	if len(a) != 43 || Equal(a, b) || !Equal(a, a) {
		t.Fatal("bad random tokens")
	}
}
