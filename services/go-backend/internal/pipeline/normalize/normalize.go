package normalize

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

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
	env, err := js.DecodeEnvelope[models.SubmitReportRequest](body)
	if err != nil {
		return err
	}
	item, err := p.Process(ctx, env.Payload, env.ReportID)
	if err != nil {
		return err
	}
	if err := p.store.SaveNewsItem(ctx, item); err != nil {
		return err
	}
	if err := p.store.MarkJobStage(ctx, env.JobID, models.JobStatusRunning, models.StageNormalize, pipeline.Progress(models.StageNormalize, 0, 0)); err != nil {
		return err
	}
	next := pipeline.NewEnvelope(env.JobID, env.ReportID, env.TraceID, models.StageSemantic, item)
	return p.nats.PublishJSON(ctx, js.SubjectSemantic, next)
}

func (p *Processor) Process(ctx context.Context, req models.SubmitReportRequest, reportID string) (models.NewsItem, error) {
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return models.NewsItem{}, errors.New("input is required")
	}
	options := defaultOptions(req.Options, p.cfg)
	inputType := req.InputType
	isURL := false
	if inputType == "" || inputType == models.InputTypeAuto {
		if parsed, err := url.ParseRequestURI(input); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			inputType = models.InputTypeURL
			isURL = true
		} else {
			inputType = models.InputTypeText
		}
	} else if parsed, err := url.ParseRequestURI(input); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		isURL = true
	}

	now := time.Now().UTC()
	var canonicalURL *string
	var body *string
	var headline *string
	failures := []models.ProviderFailure{}
	if inputType == models.InputTypeURL || (inputType == models.InputTypeRSS && isURL) {
		canonicalURL = &input
		text, title, err := fetchURLText(ctx, input)
		if err != nil {
			failures = append(failures, pipeline.ProviderFailure("basic_web", models.StageNormalize, err))
			fallback := "URL input could not be fetched; continuing with URL metadata only."
			body = &fallback
			headline = &input
		} else {
			body = &text
			headline = &title
		}
	} else {
		body = &input
		title := firstN(input, 120)
		headline = &title
	}
	meaningful := input
	if body != nil && strings.TrimSpace(*body) != "" {
		meaningful = *body
	}
	return models.NewsItem{
		ItemID:        uuid.NewString(),
		ReportID:      reportID,
		InputType:     inputType,
		OriginalInput: input,
		CanonicalURL:  canonicalURL,
		Headline:      headline,
		Body:          body,
		Summary:       headline,
		Language:      "en",
		SourceType:    "manual",
		IngestedAt:    now,
		ContentHash:   pipeline.HashText(meaningful),
		RawMetadata:   map[string]any{"normalizer": "go-mvp"},
		Options:       options,
		ProviderNotes: failures,
	}, nil
}

func defaultOptions(options *models.ReportOptions, cfg config.Config) models.ReportOptions {
	out := models.ReportOptions{MaxSources: cfg.MaxSourcesPerJob, XSearchDepth: "standard", WebSearchDepth: "limited"}
	if options == nil {
		return out
	}
	out.IncludeRawSources = options.IncludeRawSources
	if options.MaxSources > 0 {
		out.MaxSources = options.MaxSources
	}
	if options.XSearchDepth != "" {
		out.XSearchDepth = options.XSearchDepth
	}
	if options.WebSearchDepth != "" {
		out.WebSearchDepth = options.WebSearchDepth
	}
	return out
}

func fetchURLText(ctx context.Context, rawURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "provenance-pipeline-mvp/0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	text := stripTags(string(body))
	if strings.TrimSpace(text) == "" {
		return "", "", errors.New("no readable text extracted from URL")
	}
	return text, firstN(text, 120), nil
}

func stripTags(value string) string {
	var out strings.Builder
	inTag := false
	for _, r := range strings.ReplaceAll(value, "\n", " ") {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				out.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func firstN(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
