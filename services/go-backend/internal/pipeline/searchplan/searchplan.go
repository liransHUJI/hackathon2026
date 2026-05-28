package searchplan

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
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
	env, err := js.DecodeEnvelope[models.PermutationSet](body)
	if err != nil {
		return err
	}
	plan := p.Process(env.Payload)
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StagePlan, pipeline.Progress(models.StagePlan, 0, 0)); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StageSearch, plan)
	return p.nats.PublishJSON(ctx, js.SubjectSearch, next)
}

func (p *Processor) Process(set models.PermutationSet) models.SourceSearchPlan {
	maxTotal := set.SourceItem.Options.MaxSources
	if maxTotal <= 0 {
		maxTotal = p.cfg.MaxSourcesPerJob
	}
	maxX := p.cfg.MaxXResultsPerJob
	maxWeb := p.cfg.MaxWebResultsPerJob
	if maxTotal < maxX+maxWeb {
		maxX = int(float64(maxTotal) * 0.70)
		maxWeb = int(float64(maxTotal) * 0.25)
	}

	targets := make([]models.SourceTarget, 0, len(set.Permutations)*3+len(set.URLTerms))
	for _, urlTerm := range set.URLTerms {
		if isXStatusURL(urlTerm) {
			targets = append(targets, models.SourceTarget{
				Provider:       "brightdata_x",
				SourceTypes:    []string{"x_post"},
				Query:          urlTerm,
				MaxResults:     1,
				Priority:       11,
				SearchStrategy: "direct_x_status_url",
			})
		}
		if p.cfg.BrightDataUnlockerZone != "" {
			targets = append(targets, models.SourceTarget{
				Provider:       "brightdata_web",
				SourceTypes:    []string{"web_article"},
				Query:          urlTerm,
				MaxResults:     1,
				Priority:       10,
				SearchStrategy: "direct_url_unlocker",
			})
		}
		targets = append(targets, models.SourceTarget{
			Provider:       "basic_web",
			SourceTypes:    []string{"web_article"},
			Query:          urlTerm,
			MaxResults:     1,
			Priority:       10,
			SearchStrategy: "direct_url_fallback",
		})
	}
	for idx, perm := range set.Permutations {
		if p.cfg.BrightDataSERPZone != "" {
			targets = append(targets, models.SourceTarget{
				Provider:       "brightdata_web",
				SourceTypes:    []string{"web_article", "search_result"},
				Query:          perm.Text,
				MaxResults:     max(1, maxWeb/max(1, len(set.Permutations))),
				Priority:       8 - min(idx, 7),
				SearchStrategy: perm.Strategy,
			})
		}
		targets = append(targets, models.SourceTarget{
			Provider:       "basic_web",
			SourceTypes:    []string{"web_article", "search_result"},
			Query:          perm.Text,
			MaxResults:     max(2, maxWeb/max(1, len(set.Permutations))),
			Priority:       3,
			SearchStrategy: "free_search_fallback",
		})
	}
	return models.SourceSearchPlan{
		SourcePermutationSet: set,
		SourceTargets:        targets,
		MaxTotalResults:      maxTotal,
		MaxXResults:          maxX,
		MaxWebResults:        maxWeb,
		RateLimitProfile:     "default",
		CreatedAt:            time.Now().UTC(),
		ProviderFailures:     set.ProviderFailures,
	}
}

func isXStatusURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host != "x.com" && host != "twitter.com" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return len(parts) >= 3 && parts[1] == "status" && parts[2] != ""
}
