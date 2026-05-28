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

const (
	minTextChars = 240
	aiThreshold  = 0.65
)

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
			IsAIGenerated:    ensemble >= aiThreshold,
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
	if len([]rune(strings.TrimSpace(text))) < minTextChars || len(words) < 35 {
		return models.DetectionMethod{Name: "statistical", Score: 0.5, Weight: 0.35, Succeeded: true, Explanation: "Text is short, so the statistical detector returns a neutral score."}
	}
	sentenceLens := sentenceLengths(text)
	paragraphLens := paragraphLengths(text)
	uniformSentences := pipeline.Clamp01(1 - coefficientOfVariation(sentenceLens)/0.85)
	burst := burstiness(sentenceLens)
	lowBurstiness := pipeline.Clamp01((0.25 - burst) / 0.50)
	transitionDensity := phraseDensity(text, transitionPhrases, len(words))
	hedgingDensity := phraseDensity(text, hedgingPhrases, len(words))
	paragraphHomogeneity := pipeline.Clamp01(1 - coefficientOfVariation(paragraphLens)/0.90)
	score := 0.28*uniformSentences +
		0.22*lowBurstiness +
		0.20*pipeline.Clamp01(transitionDensity/3.0) +
		0.15*pipeline.Clamp01(hedgingDensity/2.5) +
		0.15*paragraphHomogeneity
	return models.DetectionMethod{Name: "statistical", Score: pipeline.Clamp01(score), Weight: 0.35, Succeeded: true, Explanation: "Local statistical detector checks sentence uniformity, burstiness, transition density, hedging, and paragraph homogeneity."}
}

func stylometricMethod(text string) models.DetectionMethod {
	words := normalizedWords(text)
	if len([]rune(strings.TrimSpace(text))) < minTextChars || len(words) < 35 {
		return models.DetectionMethod{Name: "stylometric", Score: 0.5, Weight: 0.25, Succeeded: true, Explanation: "Text is short, so the local stylometry score is neutral."}
	}
	sentenceLens := sentenceLengths(text)
	lexicalDiversity := float64(uniqueCount(words)) / float64(len(words))
	lexicalUniformity := pipeline.Clamp01((0.72 - lexicalDiversity) / 0.42)
	sentenceRegularity := pipeline.Clamp01(1 - coefficientOfVariation(sentenceLens)/0.65)
	wordUniformity := pipeline.Clamp01(1 - coefficientOfVariation(wordLengths(words))/0.55)
	openingUniformity := repeatedOpeningRatio(sentences(text), 2)
	punctuationFlatness := punctuationFlatness(text, len(words))
	score := 0.30*lexicalUniformity +
		0.15*wordUniformity +
		0.30*sentenceRegularity +
		0.15*openingUniformity +
		0.10*punctuationFlatness
	return models.DetectionMethod{Name: "stylometric", Score: pipeline.Clamp01(score), Weight: 0.25, Succeeded: true, Explanation: "Local stylometry checks lexical diversity, word and sentence regularity, repeated openings, and punctuation flatness."}
}

func templateRepetitionMethod(text string) models.DetectionMethod {
	words := normalizedWords(text)
	if len([]rune(strings.TrimSpace(text))) < minTextChars || len(words) < 35 {
		return models.DetectionMethod{Name: "template_repetition", Score: 0.5, Weight: 0.20, Succeeded: true, Explanation: "Text is short, so the local repetition score is neutral."}
	}
	boilerplateDensity := phraseDensity(text, templatePhrases, len(words))
	paragraphTemplate := pipeline.Clamp01(1 - coefficientOfVariation(paragraphLengths(text))/0.90)
	score := 0.30*pipeline.Clamp01(repeatedNgramRatio(words, 3)/0.08) +
		0.20*pipeline.Clamp01(repeatedNgramRatio(words, 4)/0.05) +
		0.25*pipeline.Clamp01(boilerplateDensity/4.0) +
		0.15*repeatedOpeningRatio(sentences(text), 3) +
		0.10*paragraphTemplate
	return models.DetectionMethod{Name: "template_repetition", Score: pipeline.Clamp01(score), Weight: 0.20, Succeeded: true, Explanation: "Local detector checks repeated n-grams, boilerplate phrases, repeated openings, and paragraph templates."}
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
	confidence := math.Min(0.45+0.13*float64(successes), 0.97)
	if successes < 2 && confidence > 0.5 {
		confidence = 0.5
	}
	if words < 40 && confidence > 0.6 {
		confidence = 0.6
	}
	return pipeline.Clamp01(score), confidence
}

var (
	sentenceSplitRE = regexp.MustCompile(`[.!?]+`)
	wordTrimRE      = regexp.MustCompile(`[^\pL\pN']+`)
)

var transitionPhrases = []string{
	"furthermore",
	"moreover",
	"however",
	"therefore",
	"in conclusion",
	"as a result",
	"on the other hand",
	"it is important to note",
	"it is worth noting",
	"this highlights",
	"this underscores",
}

var hedgingPhrases = []string{
	"may",
	"might",
	"could",
	"appears to",
	"seems to",
	"suggests that",
	"likely",
	"potentially",
	"arguably",
}

var templatePhrases = []string{
	"it is important to note",
	"it is worth noting",
	"in today's rapidly evolving",
	"the broader implications",
	"it remains to be seen",
	"this underscores",
	"this highlights",
	"as the situation continues to unfold",
	"only time will tell",
	"stakeholders must consider",
}

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

func paragraphLengths(text string) []float64 {
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	lengths := make([]float64, 0, len(parts))
	for _, part := range parts {
		if words := normalizedWords(part); len(words) > 0 {
			lengths = append(lengths, float64(len(words)))
		}
	}
	if len(lengths) == 0 {
		if words := normalizedWords(text); len(words) > 0 {
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

func burstiness(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	mean := total / float64(len(values))
	if mean <= 0 {
		return 0
	}
	variance := 0.0
	for _, value := range values {
		diff := value - mean
		variance += diff * diff
	}
	stddev := math.Sqrt(variance / float64(len(values)-1))
	return (stddev - mean) / (stddev + mean)
}

func phraseDensity(text string, phrases []string, wordCount int) float64 {
	if wordCount == 0 {
		return 0
	}
	lower := strings.ToLower(text)
	hits := 0
	for _, phrase := range phrases {
		hits += strings.Count(lower, phrase)
	}
	return float64(hits) / (float64(wordCount) / 100)
}

func punctuationFlatness(text string, wordCount int) float64 {
	if wordCount == 0 {
		return 0.5
	}
	commas := strings.Count(text, ",")
	semicolons := strings.Count(text, ";")
	colons := strings.Count(text, ":")
	exclamations := strings.Count(text, "!")
	questions := strings.Count(text, "?")
	totalPunctuation := commas + semicolons + colons + exclamations + questions
	perHundredWords := float64(totalPunctuation) / (float64(wordCount) / 100)
	return pipeline.Clamp01(1 - math.Abs(perHundredWords-4.0)/8.0)
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
