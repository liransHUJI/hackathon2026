package experts

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/llm/gemini"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
)

type Processor struct {
	store  *db.Store
	nats   *js.Client
	gemini *gemini.Client
}

func New(store *db.Store, nats *js.Client, client *gemini.Client) *Processor {
	return &Processor{store: store, nats: nats, gemini: client}
}

func (p *Processor) Handle(ctx context.Context, body []byte) error {
	env, err := js.DecodeEnvelope[models.AISignatureSet](body)
	if err != nil {
		return err
	}
	set := p.Process(ctx, env.Payload)
	if err := p.store.SaveExpertReview(ctx, env.ReportID, set.Review); err != nil {
		return err
	}
	progress := pipeline.Progress(models.StageExperts, len(env.Payload.AnalyzedSet.EnrichedSet.Sources), len(env.Payload.Results))
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StageExperts, progress); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StageFinalize, set)
	return p.nats.PublishJSON(ctx, js.SubjectFinalize, next)
}

func (p *Processor) Process(ctx context.Context, set models.AISignatureSet) models.ExpertReviewSet {
	claim := set.AnalyzedSet.EnrichedSet.SourceSet.Plan.SourcePermutationSet.CanonicalClaim
	summary, err := p.gemini.Committee(ctx, claim)
	failures := append([]models.ProviderFailure{}, set.ProviderFailures...)
	if err != nil {
		failures = append(failures, pipeline.ProviderFailure("gemini", models.StageExperts, err))
	}
	experts := map[string]models.ExpertOpinion{
		"provenance_expert":           opinion(0.45, "Earliest source evidence is ranked but remains limited by provider availability."),
		"disinformation_expert":       opinion(0.35, "No coordinated spread signal is available in the fallback slice."),
		"linguistic_forensics_expert": opinion(maxAI(set), "Local AI detectors found limited heuristic evidence."),
		"source_reliability_expert":   opinion(0.5, "Provider and timestamp quality vary across sources."),
		"skeptic_expert":              opinion(0.3, "The report avoids overclaiming when timestamps or external providers are weak."),
	}
	review := models.ExpertCommitteeReview{
		ReviewID:             uuid.NewString(),
		ReviewedCandidates:   candidateIDs(set),
		Experts:              experts,
		CommitteeScore:       0.42,
		ConfidenceAdjustment: -0.05,
		RiskAdjustment:       0.05,
		ConsensusLabel:       "provenance_uncertain",
		DissentingViews:      []string{"External provider stubs reduce confidence in earliest-source claims."},
		Summary:              summary,
		CreatedAt:            time.Now().UTC(),
	}
	return models.ExpertReviewSet{AISignatureSet: set, Review: review, ProviderFailures: failures, CreatedAt: time.Now().UTC()}
}

func opinion(score float64, reason string) models.ExpertOpinion {
	return models.ExpertOpinion{
		Score:            pipeline.Clamp01(score),
		Confidence:       0.55,
		KeyReasons:       []string{reason},
		Concerns:         []string{"MVP fallback evidence is useful for demo flow but not final attribution."},
		RecommendedLabel: "review",
	}
}

func maxAI(set models.AISignatureSet) float64 {
	maxScore := 0.0
	for _, result := range set.Results {
		if result.EnsembleScore > maxScore {
			maxScore = result.EnsembleScore
		}
	}
	return maxScore
}

func candidateIDs(set models.AISignatureSet) []string {
	out := make([]string, 0, len(set.Results))
	for _, result := range set.Results {
		out = append(out, result.RankedResult.EnrichedSource.SourceResult.SourceID)
	}
	return out
}
