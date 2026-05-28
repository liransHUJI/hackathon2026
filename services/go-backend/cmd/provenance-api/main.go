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

	"github.com/hnweb/provenance/internal/api"
	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/engine"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/providers"
	"github.com/hnweb/provenance/internal/providers/basicweb"
	"github.com/hnweb/provenance/internal/providers/brightdata"
	"github.com/hnweb/provenance/internal/scheduler"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect postgres", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.RunMigrations(ctx); err != nil {
		logger.Error("run migrations", "error", err)
		os.Exit(1)
	}

	natsClient, err := js.Connect(cfg, logger)
	if err != nil {
		logger.Error("connect nats", "error", err)
		os.Exit(1)
	}
	defer natsClient.Close()

	registry := providers.NewRegistry()
	registry.Register(brightdata.NewXWithDataset(cfg.BrightDataAPIKey, cfg.BrightDataXDatasetID, cfg.BrightDataBudgetUSD))
	registry.Register(brightdata.NewWebWithUnlocker(cfg.BrightDataAPIKey, cfg.BrightDataUnlockerZone, cfg.BrightDataBudgetUSD))
	registry.Register(basicweb.New())

	engineService := engine.New(cfg, store, registry)
	go retentionLoop(ctx, store, logger)
	go scheduler.New(cfg, engineService, logger).Start(ctx)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.New(cfg, store, natsClient, engineService, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	logger.Info("shutdown complete")
}

func retentionLoop(ctx context.Context, store *db.Store, logger *slog.Logger) {
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := store.CleanupExpired(ctx); err != nil {
				logger.Warn("retention cleanup failed", "error", err)
			}
		}
	}
}
