package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"sherlock/internal/config"
	"sherlock/internal/maintenance"
	"strings"
	"time"
)

func main() {
	path := flag.String("new-key-file", "", "File containing a base64-encoded 32-byte new encryption key; stop the app first")
	flag.Parse()
	if *path == "" {
		flag.Usage()
		os.Exit(2)
	}
	cfg, e := config.Load()
	if e != nil {
		fmt.Fprintln(os.Stderr, "Invalid installation environment.")
		os.Exit(1)
	}
	raw, e := os.ReadFile(*path)
	if e != nil {
		fmt.Fprintln(os.Stderr, "Cannot read new key file.")
		os.Exit(1)
	}
	key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if e != nil || len(key) != 32 {
		fmt.Fprintln(os.Stderr, "New key must encode exactly 32 bytes.")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, e := pgxpool.New(ctx, cfg.DatabaseURL)
	if e == nil {
		defer pool.Close()
		e = maintenance.Rotate(ctx, pool, cfg.Key, key)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "Rotation failed. Database transaction was not committed.")
		os.Exit(1)
	}
	fmt.Println("Rotation committed. Configure the new encryption key before restarting the application.")
}
