package nats

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
)

const (
	StreamName = "CAMPAIGN_INTEL"

	SubjectNormalize = "provenance.input.normalize"
	SubjectSemantic  = "provenance.semantic.generate"
	SubjectPlan      = "provenance.search.plan"
	SubjectSearch    = "provenance.search.execute"
	SubjectEnrich    = "provenance.source.enrich"
	SubjectRank      = "provenance.analyze.rank"
	SubjectAIDetect  = "provenance.ai.detect"
	SubjectExperts   = "provenance.experts.review"
	SubjectFinalize  = "provenance.report.finalize"
	SubjectCancel    = "provenance.job.cancel"
	SubjectDLQ       = "provenance.dlq"

	SubjectCampaignProfileUpdated = "campaign.profile.updated"
	SubjectCrawlPlanGenerate      = "crawl.plan.generate"
	SubjectCrawlExecute           = "crawl.execute"
	SubjectSourceStore            = "source.store"
	SubjectAccountEnrich          = "account.enrich"
	SubjectNarrativeCluster       = "narrative.cluster"
	SubjectNarrativeRelevance     = "narrative.relevance"
	SubjectSentimentGeoAnalyze    = "sentiment.geo.analyze"
	SubjectCoordinationAnalyze    = "coordination.analyze"
	SubjectAIMisinfoAnalyze       = "ai.misinfo.analyze"
	SubjectOrganicAnalyze         = "organic.prominence.analyze"
	SubjectDashboardSnapshot      = "dashboard.snapshot"
	SubjectAlertCreate            = "alert.create"
	SubjectCampaignDLQ            = "dlq"
)

var AllSubjects = []string{
	SubjectNormalize,
	SubjectSemantic,
	SubjectPlan,
	SubjectSearch,
	SubjectEnrich,
	SubjectRank,
	SubjectAIDetect,
	SubjectExperts,
	SubjectFinalize,
	SubjectCancel,
	SubjectDLQ,
	SubjectCampaignProfileUpdated,
	SubjectCrawlPlanGenerate,
	SubjectCrawlExecute,
	SubjectSourceStore,
	SubjectAccountEnrich,
	SubjectNarrativeCluster,
	SubjectNarrativeRelevance,
	SubjectSentimentGeoAnalyze,
	SubjectCoordinationAnalyze,
	SubjectAIMisinfoAnalyze,
	SubjectOrganicAnalyze,
	SubjectDashboardSnapshot,
	SubjectAlertCreate,
	SubjectCampaignDLQ,
}

type Client struct {
	conn   *natsgo.Conn
	js     natsgo.JetStreamContext
	logger *slog.Logger
}

func Connect(cfg config.Config, logger *slog.Logger) (*Client, error) {
	conn, err := natsgo.Connect(cfg.NATSURL, natsgo.Name("provenance-api"))
	if err != nil {
		return nil, err
	}
	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, err
	}
	client := &Client{conn: conn, js: js, logger: logger}
	if err := client.EnsureStream(); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) Close() {
	c.conn.Drain()
	c.conn.Close()
}

func (c *Client) Ping() error {
	if c.conn == nil || !c.conn.IsConnected() {
		return errors.New("nats is not connected")
	}
	return nil
}

func (c *Client) EnsureStream() error {
	_, err := c.js.StreamInfo(StreamName)
	if err == nil {
		return nil
	}
	_, err = c.js.AddStream(&natsgo.StreamConfig{
		Name:      StreamName,
		Subjects:  AllSubjects,
		Retention: natsgo.LimitsPolicy,
		MaxAge:    7 * 24 * time.Hour,
		Storage:   natsgo.FileStorage,
	})
	return err
}

func (c *Client) PublishJSON(ctx context.Context, subject string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = c.js.Publish(subject, body, natsgo.Context(ctx))
	return err
}

type WorkerDependencies struct {
	NATS   *Client
	Store  *db.Store
	Logger *slog.Logger
}
