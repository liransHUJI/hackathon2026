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
	"github.com/hnweb/provenance/internal/llm/gemini"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline/aidetect"
	"github.com/hnweb/provenance/internal/pipeline/enrich"
	"github.com/hnweb/provenance/internal/pipeline/experts"
	"github.com/hnweb/provenance/internal/pipeline/finalize"
	"github.com/hnweb/provenance/internal/pipeline/normalize"
	"github.com/hnweb/provenance/internal/pipeline/rank"
	"github.com/hnweb/provenance/internal/pipeline/search"
	"github.com/hnweb/provenance/internal/pipeline/searchplan"
	"github.com/hnweb/provenance/internal/pipeline/semantic"
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
	registry.Register(basicweb.New(cfg.BasicWebDomainRPS))

	geminiClient := gemini.New(cfg.GeminiAPIKey, cfg.GeminiModel, cfg.GeminiFastModel)
	if err := startProvenanceWorkers(ctx, cfg, store, natsClient, registry, geminiClient, logger); err != nil {
		logger.Error("start provenance workers", "error", err)
		os.Exit(1)
	}

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

func startProvenanceWorkers(
	ctx context.Context,
	cfg config.Config,
	store *db.Store,
	natsClient *js.Client,
	registry *providers.Registry,
	geminiClient *gemini.Client,
	logger *slog.Logger,
) error {
	deps := js.WorkerDependencies{NATS: natsClient, Store: store, Logger: logger}
	workers := []*js.Worker{
		js.NewWorker(deps, js.SubjectNormalize, "provenance-normalize", normalize.New(cfg, store, natsClient).Handle),
		js.NewWorker(deps, js.SubjectSemantic, "provenance-semantic", semantic.New(cfg, store, natsClient, geminiClient).Handle),
		js.NewWorker(deps, js.SubjectPlan, "provenance-search-plan", searchplan.New(cfg, store, natsClient).Handle),
		js.NewWorker(deps, js.SubjectSearch, "provenance-search", search.New(store, natsClient, registry).Handle),
		js.NewWorker(deps, js.SubjectEnrich, "provenance-enrich", enrich.New(store, natsClient).Handle),
		js.NewWorker(deps, js.SubjectRank, "provenance-rank", rank.New(cfg, store, natsClient).Handle),
		js.NewWorker(deps, js.SubjectAIDetect, "provenance-ai-detect", aidetect.New(cfg, store, natsClient, geminiClient).Handle),
		js.NewWorker(deps, js.SubjectExperts, "provenance-experts", experts.New(store, natsClient, geminiClient).Handle),
		js.NewWorker(deps, js.SubjectFinalize, "provenance-finalize", finalize.New(cfg, store).Handle),
	}
	for _, worker := range workers {
		if err := worker.Start(ctx); err != nil {
			return err
		}
	}
	logger.Info("provenance workers started", "count", len(workers))
	return nil
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
