package finalize

import (
	"context"
	"fmt"
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
}

func New(cfg config.Config, store *db.Store) *Processor {
	return &Processor{cfg: cfg, store: store}
}

func (p *Processor) Handle(ctx context.Context, body []byte) error {
	env, err := js.DecodeEnvelope[models.ExpertReviewSet](body)
	if err != nil {
		return err
	}
	report := p.Process(env.JobID, env.ReportID, env.Payload)
	if err := p.store.SaveReport(ctx, report); err != nil {
		return err
	}
	if err := p.store.MarkJobStage(ctx, env.JobID, report.Status, models.StageFinalize, pipeline.Progress(models.StageFinalize, len(report.Timeline), len(report.AISignatureResults))); err != nil {
		return err
	}
	return p.store.CompleteJob(ctx, env.JobID, report.Status)
}

func (p *Processor) Process(jobID, reportID string, set models.ExpertReviewSet) models.ProvenanceReport {
	analyzed := set.AISignatureSet.AnalyzedSet
	claim := analyzed.EnrichedSet.SourceSet.Plan.SourcePermutationSet.CanonicalClaim
	item := analyzed.EnrichedSet.SourceSet.Plan.SourcePermutationSet.SourceItem
	timeline := buildTimeline(analyzed.RankedResults)
	candidates := make([]models.RankedResult, 0)
	for _, result := range analyzed.RankedResults {
		if result.IsCandidateOrigin {
			candidates = append(candidates, result)
		}
	}
	if len(candidates) == 0 && len(analyzed.RankedResults) > 0 {
		candidates = append(candidates, analyzed.RankedResults[0])
	}
	var earliestHigh *models.RankedResult
	var earliestIndexed *models.RankedResult
	for i := range analyzed.RankedResults {
		result := &analyzed.RankedResults[i]
		if earliestIndexed == nil {
			earliestIndexed = result
		}
		if result.SourceConfidence >= p.cfg.SourceConfidenceThreshold && result.SimilarityScore >= p.cfg.SemanticSimilarityThreshold {
			earliestHigh = result
			break
		}
	}
	maxAI := 0.0
	for _, result := range set.AISignatureSet.Results {
		if result.EnsembleScore > maxAI {
			maxAI = result.EnsembleScore
		}
	}
	provConfidence := 0.35
	if len(candidates) > 0 {
		provConfidence = candidates[0].CompositeScore
	}
	risk := scoring.Risk(maxAI, provConfidence, 0.25, 0.35, set.Review.RiskAdjustment)
	label := scoring.RiskLabel(risk)
	status := models.JobStatusCompleted
	if len(set.ProviderFailures) > 0 {
		status = models.JobStatusPartial
	}
	if len(analyzed.RankedResults) == 0 {
		label = "LOW"
		status = models.JobStatusPartial
	}
	now := time.Now().UTC()
	created := item.IngestedAt
	return models.ProvenanceReport{
		ReportID:                     reportID,
		JobID:                        jobID,
		SourceItem:                   item,
		Status:                       status,
		CanonicalClaim:               claim,
		Timeline:                     timeline,
		CandidateOrigins:             candidates,
		AISignatureResults:           set.AISignatureSet.Results,
		ExpertCommitteeReview:        set.Review,
		EarliestHighConfidenceSource: earliestHigh,
		EarliestIndexedSource:        earliestIndexed,
		DisinformationRisk:           risk,
		RiskLabel:                    label,
		Confidence:                   pipeline.Clamp01(provConfidence + set.Review.ConfidenceAdjustment),
		SeverityExplanation:          severity(label, len(set.ProviderFailures), earliestHigh != nil),
		SourceDecisionExplanations:   decisions(candidates),
		ProviderFailures:             set.ProviderFailures,
		PipelineVersion:              pipeline.Version,
		GeneratedAt:                  now,
		ExpiresAt:                    now.AddDate(0, 0, p.cfg.RetentionDays),
		TotalDurationSeconds:         now.Sub(created).Seconds(),
		Summary:                      fmt.Sprintf("Analyzed %d sources for %q. Risk is %s with confidence %.2f.", len(timeline), claim, label, pipeline.Clamp01(provConfidence+set.Review.ConfidenceAdjustment)),
	}
}

func buildTimeline(results []models.RankedResult) []models.TimelineEntry {
	out := make([]models.TimelineEntry, 0, len(results))
	for _, result := range results {
		source := result.EnrichedSource.SourceResult
		out = append(out, models.TimelineEntry{
			SourceID:       source.SourceID,
			SourceType:     source.SourceType,
			Provider:       source.Provider,
			Title:          source.Title,
			URL:            source.URL,
			PublishedAt:    source.PublishedAt,
			IndexedAt:      source.IndexedAt,
			CompositeScore: result.CompositeScore,
			Explanation:    result.RankingExplanation,
		})
	}
	return out
}

func severity(label string, failures int, hasOrigin bool) string {
	if !hasOrigin {
		return "No high-confidence origin was identified; provenance remains uncertain."
	}
	if failures > 0 {
		return "The report is partial because one or more configured providers were unavailable or skipped."
	}
	return "Candidate origins were ranked using semantic relevance, timestamp evidence, source confidence, and AI-origin signals."
}

func decisions(candidates []models.RankedResult) []models.DecisionExplanation {
	out := make([]models.DecisionExplanation, 0, len(candidates))
	for _, candidate := range candidates {
		source := candidate.EnrichedSource.SourceResult
		out = append(out, models.DecisionExplanation{
			SourceID:            source.SourceID,
			IncludedBecause:     "It ranked highly for semantic relevance, timing, and source confidence.",
			TimestampEvidence:   "Published or indexed timestamp was used when available; otherwise confidence is reduced.",
			SemanticEvidence:    "Local lexical overlap with the canonical claim contributed to ranking.",
			Weaknesses:          "Fallback providers and missing external detectors reduce attribution confidence.",
			Role:                "candidate_origin_or_amplifier",
			SeriousnessEvidence: "AI-origin and spread signals were considered in the final risk score.",
		})
	}
	return out
}
