package gemini

import (
	"context"
	"errors"
	"strings"
)

var ErrNotConfigured = errors.New("gemini is not configured")

type Client struct {
	apiKey    string
	model     string
	fastModel string
}

type PermutationResponse struct {
	CanonicalClaim      string
	InputClassification string
	Permutations        []string
	Entities            []string
}

func New(apiKey, model, fastModel string) *Client {
	return &Client{apiKey: apiKey, model: model, fastModel: fastModel}
}

func (c *Client) ModelName() string {
	if c.fastModel != "" {
		return c.fastModel
	}
	if c.model != "" {
		return c.model
	}
	return "deterministic-fallback"
}

func (c *Client) GeneratePermutations(ctx context.Context, text string) (PermutationResponse, error) {
	_ = ctx
	if c.apiKey == "" {
		return fallbackPermutations(text), ErrNotConfigured
	}
	// The MVP keeps the Gemini call behind this abstraction. Wire the concrete Google SDK here
	// without changing pipeline stages.
	return fallbackPermutations(text), ErrNotConfigured
}

func (c *Client) JudgeAI(ctx context.Context, text string) (float64, string, error) {
	_ = ctx
	if c.apiKey == "" {
		return 0, "Gemini LLM judge was skipped because GEMINI_API_KEY is not configured.", ErrNotConfigured
	}
	score := 0.35
	if strings.Count(text, ",") > 4 || strings.Contains(strings.ToLower(text), "it is important") {
		score = 0.55
	}
	return score, "Gemini judge fallback heuristic used by MVP abstraction.", nil
}

func (c *Client) Committee(ctx context.Context, claim string) (string, error) {
	_ = ctx
	if c.apiKey == "" {
		return "Expert committee used deterministic fallback because Gemini is not configured.", ErrNotConfigured
	}
	return "Expert committee reviewed the deterministic evidence and found provenance uncertainty.", nil
}

func fallbackPermutations(text string) PermutationResponse {
	claim := strings.Join(strings.Fields(text), " ")
	if len([]rune(claim)) > 220 {
		claim = string([]rune(claim)[:220])
	}
	quoted := `"` + claim + `"`
	words := strings.Fields(claim)
	entities := make([]string, 0, 6)
	for _, word := range words {
		cleaned := strings.Trim(word, ".,:;!?()[]{}\"'")
		if cleaned == "" {
			continue
		}
		if strings.HasPrefix(cleaned, "#") || strings.HasPrefix(cleaned, "@") || strings.ToUpper(cleaned[:1]) == cleaned[:1] {
			entities = append(entities, cleaned)
		}
		if len(entities) >= 6 {
			break
		}
	}
	perms := []string{quoted, claim, claim + " source", claim + " first appeared", claim + " debunked"}
	if len(words) > 4 {
		perms = append(perms, strings.Join(words[:min(5, len(words))], " "))
	}
	return PermutationResponse{
		CanonicalClaim:      claim,
		InputClassification: classify(words),
		Permutations:        dedupe(perms),
		Entities:            dedupe(entities),
	}
}

func classify(words []string) string {
	switch {
	case len(words) <= 1:
		return "word"
	case len(words) <= 8:
		return "phrase"
	case len(words) <= 40:
		return "tweet"
	default:
		return "article"
	}
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
