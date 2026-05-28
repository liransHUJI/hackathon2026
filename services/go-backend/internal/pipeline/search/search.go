package search

import (
	"context"
	"errors"
	"time"

	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
	"github.com/hnweb/provenance/internal/providers"
)

type Processor struct {
	store    *db.Store
	nats     *js.Client
	registry *providers.Registry
}

func New(store *db.Store, nats *js.Client, registry *providers.Registry) *Processor {
	return &Processor{store: store, nats: nats, registry: registry}
}

func (p *Processor) Handle(ctx context.Context, body []byte) error {
	env, err := js.DecodeEnvelope[models.SourceSearchPlan](body)
	if err != nil {
		return err
	}
	resultSet := p.Process(ctx, env.Payload, env.JobID, env.ReportID)
	if err := p.store.SaveSourceResults(ctx, env.ReportID, resultSet.Results); err != nil {
		return err
	}
	progress := pipeline.Progress(models.StageSearch, len(resultSet.Results), 0)
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StageSearch, progress); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StageEnrich, resultSet)
	return p.nats.PublishJSON(ctx, js.SubjectEnrich, next)
}

func (p *Processor) Process(ctx context.Context, plan models.SourceSearchPlan, jobID, reportID string) models.SourceResultSet {
	failures := append([]models.ProviderFailure{}, plan.ProviderFailures...)
	results := make([]models.SourceResult, 0, plan.MaxTotalResults)
	seen := map[string]bool{}

	for _, target := range plan.SourceTargets {
		if len(results) >= plan.MaxTotalResults {
			break
		}
		provider, ok := p.registry.Get(target.Provider)
		if !ok {
			failures = append(failures, pipeline.ProviderFailure(target.Provider, models.StageSearch, providers.ErrUnavailable))
			continue
		}
		found, err := provider.Search(ctx, providers.SourceQuery{Query: target.Query, Source: target.Provider, MaxResults: target.MaxResults, JobID: jobID, ReportID: reportID})
		p.store.RecordProviderUsage(ctx, provider.ID(), jobID, reportID, "search", 0, err == nil, err)
		if err != nil {
			failures = append(failures, pipeline.ProviderFailure(provider.ID(), models.StageSearch, err))
			continue
		}
		for _, result := range found {
			if result.GlobalDedupKey == "" {
				result.GlobalDedupKey = pipeline.HashText(result.Provider + result.Snippet + result.QueryUsed)
			}
			if seen[result.GlobalDedupKey] {
				continue
			}
			seen[result.GlobalDedupKey] = true
			results = append(results, result)
			if len(results) >= plan.MaxTotalResults {
				break
			}
		}
	}
	if len(results) == 0 {
		failures = append(failures, pipeline.ProviderFailure("all", models.StageSearch, errors.New("no source provider returned results")))
	}
	return models.SourceResultSet{
		Plan:             plan,
		Results:          results,
		ProviderFailures: failures,
		CreatedAt:        time.Now().UTC(),
	}
}
