package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sherlock/internal/ai"
	"sherlock/internal/config"
	"sherlock/migrations"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Only compiled into the test executable. Production has no fake-provider switch or URL override.
func TestBrowserFixtureServer(t *testing.T) {
	if os.Getenv("SHERLOCK_E2E_SERVER") != "1" {
		t.Skip("isolated browser fixture server only")
	}
	cfg, e := config.Load()
	if e != nil {
		t.Fatal(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pc, e := pgxpool.ParseConfig(cfg.DatabaseURL)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(pc.ConnConfig.RuntimeParams["search_path"], "e2e_") || pc.ConnConfig.Database != "sherlock_test" || cfg.Origin != "http://127.0.0.1:18080" {
		t.Fatal("refusing non-isolated fixture configuration")
	}
	pc.MaxConns = 10
	pool, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = migrations.Apply(ctx, pool); e != nil {
		t.Fatal(e)
	}
	s, e := New(ctx, pool, cfg)
	if e != nil {
		t.Fatal(e)
	}
	s.aiFixture = true
	s.aiClient = &ai.Client{HTTP: &http.Client{Transport: aiTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer fixture-only" && r.Header.Get("x-api-key") != "fixture-only" {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var b struct {
			Model string `json:"model"`
		}
		if e = json.Unmarshal(raw, &b); e != nil {
			return nil, e
		}
		mode := "success"
		if !bytes.Contains(raw, []byte("synthetic-evidence")) {
			switch b.Model {
			case "fixture-refusal":
				mode = "refused"
			case "fixture-invalid":
				mode = "foreign"
			case "fixture-wait":
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
		}
		return fixtureAIResponse(r, mode)
	})}}
	if e = s.StartJobs(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.StopJobs(c)
	}()
	server := &http.Server{Addr: cfg.Address, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	select {
	case e = <-failed:
		if !errors.Is(e, http.ErrServerClosed) {
			t.Fatal(e)
		}
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}
}
