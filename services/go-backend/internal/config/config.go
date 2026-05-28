package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr                       string
	DatabaseURL                    string
	NATSURL                        string
	GeminiAPIKey                   string
	GeminiModel                    string
	GeminiFastModel                string
	GeminiRequestsPerMinute        int
	GeminiTokensPerMinute          int
	BrightDataAPIKey               string
	BrightDataUnlockerZone         string
	BrightDataXDatasetID           string
	BrightDataSearchDatasetID      string
	BrightDataBudgetUSD            float64
	BrightDataXRequestsPerMinute   int
	BrightDataWebRequestsPerMinute int
	RSSFeeds                       []string
	RSSPollInterval                time.Duration
	RSSMaxItemsPerPoll             int
	RetentionDays                  int
	MaxSourcesPerJob               int
	MaxXResultsPerJob              int
	MaxWebResultsPerJob            int
	TopCandidates                  int
	SourceConfidenceThreshold      float64
	SemanticSimilarityThreshold    float64
	BasicWebDomainRPS              float64
	DefaultTopNarratives           int
	InteractionsPerNarrative       int
	CrawlInterval                  time.Duration
}

func Load() Config {
	return Config{
		HTTPAddr:                       env("HTTP_ADDR", ":8080"),
		DatabaseURL:                    env("DATABASE_URL", "postgres://provenance:provenance@localhost:5432/provenance?sslmode=disable"),
		NATSURL:                        env("NATS_URL", "nats://localhost:4222"),
		GeminiAPIKey:                   os.Getenv("GEMINI_API_KEY"),
		GeminiModel:                    env("GEMINI_MODEL", "gemini-1.5-pro"),
		GeminiFastModel:                env("GEMINI_FAST_MODEL", "gemini-1.5-flash"),
		GeminiRequestsPerMinute:        envInt("GEMINI_REQUESTS_PER_MINUTE", 60),
		GeminiTokensPerMinute:          envInt("GEMINI_TOKENS_PER_MINUTE", 60000),
		BrightDataAPIKey:               os.Getenv("BRIGHTDATA_API_KEY"),
		BrightDataUnlockerZone:         os.Getenv("BRIGHTDATA_UNLOCKER_ZONE"),
		BrightDataXDatasetID:           os.Getenv("BRIGHTDATA_X_DATASET_ID"),
		BrightDataSearchDatasetID:      os.Getenv("BRIGHTDATA_SEARCH_DATASET_ID"),
		BrightDataBudgetUSD:            envFloat("BRIGHTDATA_BUDGET_USD", 0),
		BrightDataXRequestsPerMinute:   envInt("BRIGHTDATA_X_REQUESTS_PER_MINUTE", 30),
		BrightDataWebRequestsPerMinute: envInt("BRIGHTDATA_WEB_REQUESTS_PER_MINUTE", 30),
		RSSFeeds:                       envCSV("RSS_FEEDS"),
		RSSPollInterval:                time.Duration(envInt("RSS_POLL_INTERVAL_SECONDS", 300)) * time.Second,
		RSSMaxItemsPerPoll:             envInt("RSS_MAX_ITEMS_PER_POLL", 20),
		RetentionDays:                  envInt("RETENTION_DAYS", 7),
		MaxSourcesPerJob:               envInt("MAX_SOURCES_PER_JOB", 250),
		MaxXResultsPerJob:              envInt("MAX_X_RESULTS_PER_JOB", 175),
		MaxWebResultsPerJob:            envInt("MAX_WEB_RESULTS_PER_JOB", 60),
		TopCandidates:                  envInt("TOP_CANDIDATES", 15),
		SourceConfidenceThreshold:      envFloat("SOURCE_CONFIDENCE_THRESHOLD", 0.65),
		SemanticSimilarityThreshold:    envFloat("SEMANTIC_SIMILARITY_THRESHOLD", 0.55),
		BasicWebDomainRPS:              envFloat("BASIC_WEB_DOMAIN_RPS", 2),
		DefaultTopNarratives:           envInt("DEFAULT_TOP_NARRATIVES", 20),
		InteractionsPerNarrative:       envInt("INTERACTIONS_PER_NARRATIVE", 2000),
		CrawlInterval:                  time.Duration(envInt("CRAWL_INTERVAL_SECONDS", 900)) * time.Second,
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envFloat(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envCSV(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
