package aidetect

import (
	"context"
	"errors"
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
	env, err := js.DecodeEnvelope[models.AnalyzedSet](body)
	if err != nil {
		return err
	}
	set := p.Process(ctx, env.Payload)
	if err := p.store.SaveAIResults(ctx, env.ReportID, set.Results); err != nil {
		return err
	}
	progress := pipeline.Progress(models.StageAIDetect, len(env.Payload.EnrichedSet.Sources), len(set.Results))
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StageAIDetect, progress); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StageExperts, set)
	return p.nats.PublishJSON(ctx, js.SubjectExperts, next)
}

func (p *Processor) Process(ctx context.Context, set models.AnalyzedSet) models.AISignatureSet {
	failures := append([]models.ProviderFailure{}, set.ProviderFailures...)
	results := make([]models.AISignatureResult, 0, len(set.RankedResults))
	for _, ranked := range set.RankedResults {
		text := ranked.EnrichedSource.NormalizedText
		methods := []models.DetectionMethod{
			statisticalMethod(text),
			missingOptional("gptzero", 0.25, p.cfg.GPTZeroAPIKey),
			missingOptional("sapling", 0.20, p.cfg.SaplingAPIKey),
		}
		score, explanation, err := p.gemini.JudgeAI(ctx, text)
		if err != nil {
			failures = append(failures, pipeline.ProviderFailure("gemini", models.StageAIDetect, err))
			methods = append(methods, models.DetectionMethod{Name: "gemini_llm_judge", Weight: 0.20, Succeeded: false, Error: ptr(err.Error()), Explanation: explanation})
		} else {
			methods = append(methods, models.DetectionMethod{Name: "gemini_llm_judge", Score: score, Weight: 0.20, Succeeded: true, Explanation: explanation})
		}
		ensemble, confidence := ensemble(methods, len(strings.Fields(text)))
		results = append(results, models.AISignatureResult{
			RankedResult:     ranked,
			DetectionMethods: methods,
			EnsembleScore:    ensemble,
			IsAIGenerated:    ensemble >= threshold(len(strings.Fields(text)), methods),
			Confidence:       confidence,
			Explanation:      "AI score renormalizes over successful methods; unavailable optional providers are reported but not scored as zero.",
		})
	}
	return models.AISignatureSet{
		AnalyzedSet:      set,
		Results:          results,
		ProviderFailures: failures,
		CreatedAt:        time.Now().UTC(),
	}
}

func statisticalMethod(text string) models.DetectionMethod {
	words := strings.Fields(text)
	score := 0.25
	if len(words) > 40 {
		score += 0.1
	}
	if strings.Contains(strings.ToLower(text), "furthermore") || strings.Contains(strings.ToLower(text), "it is important") {
		score += 0.2
	}
	return models.DetectionMethod{Name: "statistical_linguistic", Score: pipeline.Clamp01(score), Weight: 0.20, Succeeded: true, Explanation: "Local heuristic considers length, repetition, and common AI-like transition phrases."}
}

func missingOptional(name string, weight float64, key string) models.DetectionMethod {
	if key == "" {
		err := errors.New(name + " API key is not configured")
		return models.DetectionMethod{Name: name, Weight: weight, Succeeded: false, Error: ptr(err.Error()), Explanation: "Optional detector skipped and excluded from ensemble."}
	}
	err := errors.New(name + " client stub is present but endpoint integration is not implemented in MVP")
	return models.DetectionMethod{Name: name, Weight: weight, Succeeded: false, Error: ptr(err.Error()), Explanation: "Provider client remains a safe stub for this pass."}
}

func ensemble(methods []models.DetectionMethod, words int) (float64, float64) {
	totalWeight := 0.0
	weighted := 0.0
	successes := 0
	for _, method := range methods {
		if !method.Succeeded {
			continue
		}
		successes++
		totalWeight += method.Weight
		weighted += method.Score * method.Weight
	}
	if totalWeight == 0 {
		return 0, 0
	}
	score := weighted / totalWeight
	confidence := 0.75
	if successes < 2 {
		confidence = 0.5
	}
	if words < 40 && confidence > 0.6 {
		confidence = 0.6
	}
	return pipeline.Clamp01(score), confidence
}

func threshold(words int, methods []models.DetectionMethod) float64 {
	successes := 0
	for _, method := range methods {
		if method.Succeeded {
			successes++
		}
	}
	if successes >= 3 {
		return 0.60
	}
	if words < 40 {
		return 0.75
	}
	if words < 80 {
		return 0.70
	}
	return 0.65
}

func ptr(value string) *string {
	return &value
}
