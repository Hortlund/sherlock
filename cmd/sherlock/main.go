package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sherlock/internal/api"
	"sherlock/internal/config"
	"sherlock/migrations"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("startup failed", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	poolcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	poolcfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(startup, poolcfg)
	if err != nil {
		return errors.New("could not create database pool")
	}
	defer pool.Close()
	if err = pool.Ping(startup); err != nil {
		return errors.New("database connection failed")
	}
	if err = migrations.Apply(startup, pool); err != nil {
		return errors.New("database migration failed")
	}
	app, err := api.New(startup, pool, cfg)
	if err != nil {
		return err
	}
	if err = app.StartJobs(ctx); err != nil {
		return errors.New("job worker startup failed")
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = app.StopJobs(c)
	}()
	server := &http.Server{Addr: cfg.Address, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	failed := make(chan error, 1)
	go func() { slog.Info("server listening", "address", cfg.Address); failed <- server.ListenAndServe() }()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case err = <-failed:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			cleanup, c := context.WithTimeout(ctx, 5*time.Second)
			app.Cleanup(cleanup)
			if e := app.Retain(cleanup); e != nil {
				slog.Warn("retention cleanup deferred")
			}
			if e := app.Pump(cleanup); e != nil {
				slog.Warn("schedule pump deferred")
			}
			c()
		case <-ctx.Done():
			shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
			defer c()
			return server.Shutdown(shutdown)
		}
	}
}
