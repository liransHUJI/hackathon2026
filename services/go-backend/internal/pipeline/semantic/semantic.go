package semantic

import (
	"context"
	"strings"
	"time"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/llm/gemini"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
)

type Processor struct {
	cfg    config.Config
	store  *db.Store
	nats   *js.Client
	gemini *gemini.Client
}

func New(cfg config.Config, store *db.Store, nats *js.Client, client *gemini.Client) *Processor {
	return &Processor{cfg: cfg, store: store, nats: nats, gemini: client}
}

func (p *Processor) Handle(ctx context.Context, body []byte) error {
	env, err := js.DecodeEnvelope[models.NewsItem](body)
	if err != nil {
		return err
	}
	set := p.Process(ctx, env.Payload)
	if err := p.store.SavePermutationSet(ctx, env.ReportID, set); err != nil {
		return err
	}
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StageSemantic, pipeline.Progress(models.StageSemantic, 0, 0)); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StagePlan, set)
	return p.nats.PublishJSON(ctx, js.SubjectPlan, next)
}

func (p *Processor) Process(ctx context.Context, item models.NewsItem) models.PermutationSet {
	text := item.OriginalInput
	if item.Body != nil && strings.TrimSpace(*item.Body) != "" {
		text = *item.Body
	}
	resp, err := p.gemini.GeneratePermutations(ctx, text)
	failures := append([]models.ProviderFailure{}, item.ProviderNotes...)
	if err != nil {
		failures = append(failures, pipeline.ProviderFailure("gemini", models.StageSemantic, err))
	}
	perms := make([]models.Permutation, 0, len(resp.Permutations))
	for _, value := range resp.Permutations {
		perms = append(perms, models.Permutation{
			Text:               value,
			Strategy:           "deterministic_or_llm",
			Intent:             "find early matching source",
			Confidence:         0.7,
			RecommendedSources: []string{"brightdata_x", "brightdata_web", "basic_web"},
		})
	}
	if len(perms) == 0 {
		perms = append(perms, models.Permutation{Text: item.OriginalInput, Strategy: "exact", Intent: "fallback exact query", Confidence: 0.6, RecommendedSources: []string{"basic_web"}})
	}
	return models.PermutationSet{
		SourceItem:          item,
		CanonicalClaim:      resp.CanonicalClaim,
		InputClassification: resp.InputClassification,
		Permutations:        perms,
		EntityTerms:         resp.Entities,
		URLTerms:            urlTerms(item),
		ModelUsed:           p.gemini.ModelName(),
		GeneratedAt:         time.Now().UTC(),
		TotalCount:          len(perms),
		ProviderFailures:    failures,
	}
}

func urlTerms(item models.NewsItem) []string {
	if item.CanonicalURL == nil {
		return nil
	}
	return []string{*item.CanonicalURL}
}
