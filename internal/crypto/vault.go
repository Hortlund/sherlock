package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// Version 1 binds every ciphertext to a record-specific associated-data context.
type Vault struct{ aead cipher.AEAD }

func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must decode to exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead}, nil
}
func (v *Vault) Seal(plaintext []byte, scope string) []byte {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	out := append([]byte{1}, nonce...)
	return v.aead.Seal(out, nonce, plaintext, []byte("sherlock:v1:"+scope))
}
func (v *Vault) Open(data []byte, scope string) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(data) < 1+n+v.aead.Overhead() || data[0] != 1 {
		return nil, errors.New("invalid encrypted value")
	}
	return v.aead.Open(nil, data[1:1+n], data[1+n:], []byte("sherlock:v1:"+scope))
}
