package aidetect

import (
	"context"
	"math"
	"regexp"
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
			stylometricMethod(text),
			templateRepetitionMethod(text),
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
			Explanation:      "AI score combines free local detectors and the optional Gemini judge; unavailable providers are excluded from the ensemble.",
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
	words := normalizedWords(text)
	score := 0.25
	if len(words) > 40 {
		score += 0.1
	}
	if strings.Contains(strings.ToLower(text), "furthermore") || strings.Contains(strings.ToLower(text), "it is important") {
		score += 0.2
	}
	return models.DetectionMethod{Name: "statistical_linguistic", Score: pipeline.Clamp01(score), Weight: 0.35, Succeeded: true, Explanation: "Local heuristic considers length, repetition, and common AI-like transition phrases."}
}

func stylometricMethod(text string) models.DetectionMethod {
	words := normalizedWords(text)
	if len(words) < 20 {
		return models.DetectionMethod{Name: "stylometric", Score: 0.5, Weight: 0.25, Succeeded: true, Explanation: "Text is short, so the local stylometry score is neutral."}
	}
	sentenceLens := sentenceLengths(text)
	lexicalDiversity := float64(uniqueCount(words)) / float64(len(words))
	lexicalUniformity := pipeline.Clamp01((0.72 - lexicalDiversity) / 0.42)
	sentenceRegularity := pipeline.Clamp01(1 - coefficientOfVariation(sentenceLens)/0.65)
	wordUniformity := pipeline.Clamp01(1 - coefficientOfVariation(wordLengths(words))/0.55)
	openingUniformity := repeatedOpeningRatio(sentences(text), 2)
	score := 0.30*lexicalUniformity + 0.30*sentenceRegularity + 0.20*wordUniformity + 0.20*openingUniformity
	return models.DetectionMethod{Name: "stylometric", Score: pipeline.Clamp01(score), Weight: 0.25, Succeeded: true, Explanation: "Local stylometry checks lexical diversity, sentence regularity, word-length variance, and repeated openings."}
}

func templateRepetitionMethod(text string) models.DetectionMethod {
	words := normalizedWords(text)
	if len(words) < 20 {
		return models.DetectionMethod{Name: "template_repetition", Score: 0.5, Weight: 0.20, Succeeded: true, Explanation: "Text is short, so the local repetition score is neutral."}
	}
	lower := strings.ToLower(text)
	phraseHits := 0
	for _, phrase := range []string{
		"it is important to note",
		"it is worth noting",
		"in today's rapidly evolving",
		"the broader implications",
		"it remains to be seen",
		"this underscores",
	} {
		phraseHits += strings.Count(lower, phrase)
	}
	phraseDensity := float64(phraseHits) / (float64(len(words)) / 100)
	score := 0.35*pipeline.Clamp01(repeatedNgramRatio(words, 3)/0.08) +
		0.25*pipeline.Clamp01(repeatedNgramRatio(words, 4)/0.05) +
		0.25*pipeline.Clamp01(phraseDensity/4.0) +
		0.15*repeatedOpeningRatio(sentences(text), 3)
	return models.DetectionMethod{Name: "template_repetition", Score: pipeline.Clamp01(score), Weight: 0.20, Succeeded: true, Explanation: "Local detector checks repeated n-grams, boilerplate phrases, and repeated sentence openings."}
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

var (
	sentenceSplitRE = regexp.MustCompile(`[.!?]+`)
	wordTrimRE      = regexp.MustCompile(`[^\pL\pN']+`)
)

func sentences(text string) []string {
	raw := sentenceSplitRE.Split(text, -1)
	out := make([]string, 0, len(raw))
	for _, sentence := range raw {
		if trimmed := strings.TrimSpace(sentence); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizedWords(text string) []string {
	raw := strings.Fields(text)
	out := make([]string, 0, len(raw))
	for _, word := range raw {
		normalized := strings.ToLower(wordTrimRE.ReplaceAllString(word, ""))
		if normalized != "" {
			out = append(out, normalized)
		}
	}
	return out
}

func sentenceLengths(text string) []float64 {
	sentenceList := sentences(text)
	lengths := make([]float64, 0, len(sentenceList))
	for _, sentence := range sentenceList {
		if words := normalizedWords(sentence); len(words) > 0 {
			lengths = append(lengths, float64(len(words)))
		}
	}
	return lengths
}

func wordLengths(words []string) []float64 {
	lengths := make([]float64, 0, len(words))
	for _, word := range words {
		lengths = append(lengths, float64(len(word)))
	}
	return lengths
}

func coefficientOfVariation(values []float64) float64 {
	if len(values) < 2 {
		return 0.5
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	mean := total / float64(len(values))
	if mean <= 0 {
		return 0.5
	}
	variance := 0.0
	for _, value := range values {
		diff := value - mean
		variance += diff * diff
	}
	return math.Sqrt(variance/float64(len(values)-1)) / mean
}

func uniqueCount(words []string) int {
	seen := make(map[string]struct{}, len(words))
	for _, word := range words {
		seen[word] = struct{}{}
	}
	return len(seen)
}

func repeatedNgramRatio(words []string, size int) float64 {
	if len(words) < size*2 {
		return 0
	}
	counts := make(map[string]int)
	for i := 0; i <= len(words)-size; i++ {
		counts[strings.Join(words[i:i+size], " ")]++
	}
	repeated := 0
	for _, count := range counts {
		if count > 1 {
			repeated += count - 1
		}
	}
	return float64(repeated) / float64(len(words)-size+1)
}

func repeatedOpeningRatio(sentenceList []string, size int) float64 {
	if len(sentenceList) < 2 {
		return 0.5
	}
	openings := make(map[string]int)
	total := 0
	for _, sentence := range sentenceList {
		words := normalizedWords(sentence)
		if len(words) < size {
			continue
		}
		openings[strings.Join(words[:size], " ")]++
		total++
	}
	if total == 0 {
		return 0.5
	}
	duplicates := 0
	for _, count := range openings {
		if count > 1 {
			duplicates += count - 1
		}
	}
	return pipeline.Clamp01(float64(duplicates) / float64(total))
}

func ptr(value string) *string {
	return &value
}
