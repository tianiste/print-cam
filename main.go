package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("load .env failed", "error", err)
	}
	cfg := loadConfig()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	err = cfg.validate()
	if err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database pool failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	err = pool.Ping(ctx)
	if err != nil {
		logger.Error("database ping failed", "error", err)
		os.Exit(1)
	}
	err = runMigrations(ctx, pool)
	if err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}
	store := newPostgresStore(pool)
	err = store.DeleteExpiredSessions(ctx, time.Now())
	if err != nil {
		logger.Warn("expired session cleanup failed", "error", err)
	}
	err = bootstrap(ctx, store, cfg)
	if err != nil {
		logger.Error("bootstrap failed", "error", err)
		os.Exit(1)
	}

	app := newApp(cfg, logger, store, newBroker(logger))
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		errCh <- server.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-stop:
		logger.Info("shutdown requested", "signal", sig.String())
	case err = <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
		return
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	err = server.Shutdown(shutdownCtx)
	if err != nil {
		logger.Error("shutdown failed", "error", err)
		os.Exit(1)
	}
}
