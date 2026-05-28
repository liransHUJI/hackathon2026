package enrich

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
	"github.com/hnweb/provenance/internal/scoring"
)

var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

type Processor struct {
	store *db.Store
	nats  *js.Client
}

func New(store *db.Store, nats *js.Client) *Processor {
	return &Processor{store: store, nats: nats}
}

func (p *Processor) Handle(ctx context.Context, body []byte) error {
	env, err := js.DecodeEnvelope[models.SourceResultSet](body)
	if err != nil {
		return err
	}
	set := p.Process(env.Payload)
	progress := pipeline.Progress(models.StageEnrich, len(set.Sources), 0)
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StageEnrich, progress); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StageRank, set)
	return p.nats.PublishJSON(ctx, js.SubjectRank, next)
}

func (p *Processor) Process(sourceSet models.SourceResultSet) models.EnrichedSourceSet {
	out := make([]models.EnrichedSource, 0, len(sourceSet.Results))
	for _, result := range sourceSet.Results {
		text := result.Snippet
		if result.FullText != nil && strings.TrimSpace(*result.FullText) != "" {
			text = *result.FullText
		}
		completeness := 0.45
		if len(strings.Fields(text)) > 40 {
			completeness = 0.8
		}
		timestamp := 0.4
		if result.PublishedAt != nil || result.IndexedAt != nil {
			timestamp = 0.75
		}
		providerReliability := 0.5
		if strings.HasPrefix(result.Provider, "brightdata") {
			providerReliability = 0.75
		}
		accountReliability := 0.45
		confidence := scoring.SourceConfidence(timestamp, completeness, providerReliability, accountReliability, 0.35)
		out = append(out, models.EnrichedSource{
			SourceResult:          result,
			NormalizedText:        strings.Join(strings.Fields(text), " "),
			TextHash:              pipeline.HashText(text),
			Language:              "en",
			Entities:              extractEntities(text),
			LinkedURLs:            urlPattern.FindAllString(text, 10),
			QuotedSources:         nil,
			AccountReliability:    accountReliability,
			TimestampConfidence:   timestamp,
			ContentCompleteness:   completeness,
			SourceConfidence:      confidence,
			EnrichmentExplanation: "MVP enrichment used local text normalization, timestamp availability, provider reliability, and content completeness.",
		})
	}
	return models.EnrichedSourceSet{
		SourceSet:        sourceSet,
		Sources:          out,
		ProviderFailures: sourceSet.ProviderFailures,
		CreatedAt:        time.Now().UTC(),
	}
}

func extractEntities(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, word := range strings.Fields(text) {
		cleaned := strings.Trim(word, ".,:;!?()[]{}\"'")
		if len(cleaned) < 3 {
			continue
		}
		if strings.HasPrefix(cleaned, "#") || strings.HasPrefix(cleaned, "@") || strings.ToUpper(cleaned[:1]) == cleaned[:1] {
			if !seen[cleaned] {
				seen[cleaned] = true
				out = append(out, cleaned)
			}
		}
		if len(out) >= 12 {
			break
		}
	}
	return out
}
