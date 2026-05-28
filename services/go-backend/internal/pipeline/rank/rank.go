package rank

import (
	"context"
	"sort"
	"time"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
	"github.com/hnweb/provenance/internal/scoring"
)

type Processor struct {
	cfg   config.Config
	store *db.Store
	nats  *js.Client
}

func New(cfg config.Config, store *db.Store, nats *js.Client) *Processor {
	return &Processor{cfg: cfg, store: store, nats: nats}
}

func (p *Processor) Handle(ctx context.Context, body []byte) error {
	env, err := js.DecodeEnvelope[models.EnrichedSourceSet](body)
	if err != nil {
		return err
	}
	set := p.Process(env.Payload)
	if err := p.store.SaveRankedResults(ctx, env.ReportID, set.RankedResults); err != nil {
		return err
	}
	progress := pipeline.Progress(models.StageRank, len(env.Payload.Sources), len(set.RankedResults))
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StageRank, progress); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StageAIDetect, set)
	return p.nats.PublishJSON(ctx, js.SubjectAIDetect, next)
}

func (p *Processor) Process(set models.EnrichedSourceSet) models.AnalyzedSet {
	claim := set.SourceSet.Plan.SourcePermutationSet.CanonicalClaim
	var earliest *time.Time
	for _, source := range set.Sources {
		ts := source.SourceResult.PublishedAt
		if ts == nil {
			ts = source.SourceResult.IndexedAt
		}
		if ts != nil && (earliest == nil || ts.Before(*earliest)) {
			copy := *ts
			earliest = &copy
		}
	}

	ranked := make([]models.RankedResult, 0, len(set.Sources))
	for _, source := range set.Sources {
		ts := source.SourceResult.PublishedAt
		if ts == nil {
			ts = source.SourceResult.IndexedAt
		}
		similarity := scoring.LexicalSimilarity(claim, source.NormalizedText)
		chronological := scoring.ChronologicalScore(ts, earliest)
		aiSignal := 0.25
		if len(source.NormalizedText) > 0 && len(source.NormalizedText)%7 == 0 {
			aiSignal = 0.4
		}
		spread := 0.2
		if source.SourceResult.SourceType == models.SourceTypeXPost {
			spread = 0.45
		}
		composite := scoring.ProvenanceScore(similarity, chronological, source.SourceConfidence, aiSignal, spread)
		ranked = append(ranked, models.RankedResult{
			EnrichedSource:     source,
			SimilarityScore:    similarity,
			ChronologicalScore: chronological,
			SourceConfidence:   source.SourceConfidence,
			AIOriginSignal:     aiSignal,
			SpreadSignal:       spread,
			CompositeScore:     composite,
			IsCandidateOrigin:  source.SourceConfidence >= p.cfg.SourceConfidenceThreshold && similarity >= p.cfg.SemanticSimilarityThreshold,
			RankingExplanation: "Composite score combines local lexical similarity, timestamp position, source confidence, AI-origin signal, and spread signal.",
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].CompositeScore > ranked[j].CompositeScore
	})
	for i := range ranked {
		ranked[i].ChronologicalRank = i + 1
	}
	if len(ranked) > p.cfg.TopCandidates {
		ranked = ranked[:p.cfg.TopCandidates]
	}
	return models.AnalyzedSet{
		EnrichedSet:      set,
		RankedResults:    ranked,
		ProviderFailures: set.ProviderFailures,
		CreatedAt:        time.Now().UTC(),
	}
}
