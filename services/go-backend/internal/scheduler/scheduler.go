package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/engine"
)

type Scheduler struct {
	cfg    config.Config
	engine *engine.Engine
	logger *slog.Logger
}

func New(cfg config.Config, engine *engine.Engine, logger *slog.Logger) *Scheduler {
	return &Scheduler{cfg: cfg, engine: engine, logger: logger}
}

func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.CrawlInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			campaigns, err := s.engineCampaigns(ctx)
			if err != nil {
				s.logger.Warn("list campaigns for scheduler failed", "error", err)
				continue
			}
			for _, campaignID := range campaigns {
				go func(id string) {
					if _, err := s.engine.RunDiscovery(ctx, id); err != nil {
						s.logger.Warn("scheduled discovery failed", "campaign_id", id, "error", err)
					}
				}(campaignID)
			}
		}
	}
}

func (s *Scheduler) engineCampaigns(ctx context.Context) ([]string, error) {
	campaigns, err := s.engine.ListCampaigns(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(campaigns))
	for _, campaign := range campaigns {
		// Only auto-crawl campaigns the user has left in the "active" monitoring state. Campaigns
		// that are stopped, already running, or have completed a run are not re-triggered here
		// (a run in flight is guarded separately, and completed/stopped campaigns are re-run only
		// on explicit request). This prevents the scheduler from stacking endless overlapping
		// crawls on the same campaign.
		if campaign.Status == "active" {
			ids = append(ids, campaign.CampaignID)
		}
	}
	return ids, nil
}
