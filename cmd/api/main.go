// LogaLuxe API. Run with `go run ./cmd/api` or through docker compose.
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

	"logaluxe/api/internal/config"
	"logaluxe/api/internal/db"
	"logaluxe/api/internal/httpapi"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	if cfg.Seed {
		if err := db.Seed(ctx, pool); err != nil {
			slog.Error("seed", "err", err)
			os.Exit(1)
		}
		if err := db.SeedOps(ctx, pool); err != nil {
			slog.Error("seed ops", "err", err)
			os.Exit(1)
		}
		if err := db.SeedPlaces(ctx, pool); err != nil {
			slog.Error("seed places", "err", err)
			os.Exit(1)
		}
	}
	// Existing locations get their state in its stored form and the time zone of where they are. Once.
	if err := httpapi.BackfillGeo(ctx, pool); err != nil {
		slog.Error("places", "err", err)
		os.Exit(1)
	}
	if cfg.Seed {
		if err := httpapi.BootstrapMerchants(ctx, pool, cfg); err != nil {
			slog.Error("bootstrap merchants", "err", err)
			os.Exit(1)
		}
		if err := db.SeedMerchant(ctx, pool); err != nil {
			slog.Error("seed merchant", "err", err)
			os.Exit(1)
		}
	}
	httpapi.StartWorker(ctx, cfg, pool)
	if err := httpapi.BootstrapAdmin(ctx, pool, cfg); err != nil {
		slog.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.New(cfg, pool),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("api listening", "port", cfg.Port, "env", cfg.Env, "payments", cfg.PaymentsMode())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	slog.Info("stopped")
}
