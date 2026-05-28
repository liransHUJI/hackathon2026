package rss

import (
	"context"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
)

type Poller struct {
	cfg    config.Config
	store  *db.Store
	nats   *js.Client
	logger *slog.Logger
	client *http.Client
	seen   map[string]bool
}

func NewPoller(cfg config.Config, store *db.Store, nats *js.Client, logger *slog.Logger) *Poller {
	return &Poller{cfg: cfg, store: store, nats: nats, logger: logger, client: &http.Client{Timeout: 15 * time.Second}, seen: map[string]bool{}}
}

func (p *Poller) Start(ctx context.Context) {
	if len(p.cfg.RSSFeeds) == 0 {
		return
	}
	ticker := time.NewTicker(p.cfg.RSSPollInterval)
	defer ticker.Stop()
	p.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	for _, feedURL := range p.cfg.RSSFeeds {
		items, err := p.fetch(ctx, feedURL)
		if err != nil {
			p.logger.Warn("rss poll failed", "feed", feedURL, "error", err)
			continue
		}
		limit := p.cfg.RSSMaxItemsPerPoll
		if limit <= 0 || limit > len(items) {
			limit = len(items)
		}
		for _, item := range items[:limit] {
			key := item.GUID
			if key == "" {
				key = item.Link + item.Title
			}
			key = fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
			if p.seen[key] {
				continue
			}
			p.seen[key] = true
			req := models.SubmitReportRequest{InputType: models.InputTypeRSS, Input: item.bestInput(), Priority: "low"}
			jobID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("job:"+key)).String()
			reportID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("report:"+key)).String()
			now := time.Now().UTC()
			_ = p.store.CreateJob(ctx, models.JobRecord{
				ID:           jobID,
				ReportID:     reportID,
				Status:       models.JobStatusQueued,
				CurrentStage: models.StageNormalize,
				Progress:     pipeline.Progress(models.StageNormalize, 0, 0),
				CreatedAt:    now,
				UpdatedAt:    now,
				ExpiresAt:    now.AddDate(0, 0, p.cfg.RetentionDays),
			})
			env := pipeline.NewEnvelope(jobID, reportID, "", models.StageNormalize, req)
			_ = p.nats.PublishJSON(ctx, js.SubjectNormalize, env)
		}
	}
}

func (p *Poller) fetch(ctx context.Context, feedURL string) ([]feedItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	return feed.Channel.Items, nil
}

type rssFeed struct {
	Channel struct {
		Items []feedItem `xml:"item"`
	} `xml:"channel"`
}

type feedItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
}

func (i feedItem) bestInput() string {
	if strings.TrimSpace(i.Link) != "" {
		return strings.TrimSpace(i.Link)
	}
	if strings.TrimSpace(i.Title) != "" {
		return strings.TrimSpace(i.Title)
	}
	return strings.TrimSpace(i.Description)
}
