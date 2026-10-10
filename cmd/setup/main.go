// setup creates installation secrets without ever printing their values.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func urlSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func main() {
	f, err := os.OpenFile(".env", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not create .env; it may already exist. Existing secrets were not changed.")
		os.Exit(1)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "POSTGRES_PASSWORD=%s\nSHERLOCK_ENCRYPTION_KEY=%s\nSHERLOCK_SETUP_TOKEN=%s\nSHERLOCK_ORIGIN=http://localhost:8080\nSHERLOCK_PORT=8080\n", urlSecret(), secret(), urlSecret())
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not write installation configuration")
		os.Exit(1)
	}
	fmt.Println("Created .env with private permissions. Keep it safe; it contains the encryption key and setup token.")
}
