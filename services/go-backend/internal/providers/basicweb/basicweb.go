package basicweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/models"
	"github.com/hnweb/provenance/internal/providers"
)

type Provider struct {
	client *http.Client
}

func New() *Provider {
	return &Provider{client: &http.Client{Timeout: 15 * time.Second}}
}

func (p *Provider) ID() string {
	return "basic_web"
}

func (p *Provider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{
		SupportedSourceTypes:        []models.SourceType{models.SourceTypeWebArticle, models.SourceTypeSearchResult},
		SupportsXSearch:             false,
		ReturnsUnavailableEvidence:  false,
		SupportsFullTextFetch:       true,
		ReliablePublishedTimestamps: false,
		CostModel:                   "free best-effort http fetch",
		RateLimitProfile:            "per-domain",
	}
}

func (p *Provider) Search(ctx context.Context, query providers.SourceQuery) ([]models.SourceResult, error) {
	trimmed := strings.TrimSpace(query.Query)
	if parsed, err := url.ParseRequestURI(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		result, err := p.Fetch(ctx, providers.SourceRef{URL: trimmed})
		if err != nil {
			return nil, err
		}
		result.QueryUsed = query.Query
		return []models.SourceResult{*result}, nil
	}

	return nil, fmt.Errorf("%w: basic_web only fetches explicit URLs; configure Bright Data for search", providers.ErrUnavailable)
}

func (p *Provider) Fetch(ctx context.Context, ref providers.SourceRef) (*models.SourceResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "provenance-pipeline-mvp/0.1")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	text := htmlToText(string(body))
	if strings.TrimSpace(text) == "" {
		text = fmt.Sprintf("Fetched %s with HTTP status %d but no readable text was extracted.", ref.URL, resp.StatusCode)
	}
	now := time.Now().UTC()
	title := firstSentence(text)
	canonical := ref.URL
	status := resp.StatusCode
	return &models.SourceResult{
		SourceID:           uuid.NewString(),
		GlobalDedupKey:     "url:" + canonical,
		SourceType:         models.SourceTypeWebArticle,
		Provider:           p.ID(),
		URL:                &canonical,
		CanonicalURL:       &canonical,
		Title:              &title,
		Snippet:            firstN(text, 300),
		FullText:           &text,
		ScrapedAt:          now,
		IndexedAt:          &now,
		Engagement:         map[string]any{},
		SourceMetadata:     map[string]any{"content_type": resp.Header.Get("content-type")},
		QueryUsed:          ref.URL,
		HTTPStatus:         &status,
		AvailabilityStatus: models.AvailabilityAvailable,
	}, nil
}

func (p *Provider) Collect(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	trimmed := strings.TrimSpace(target.Query)
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: basic_web requires an explicit URL collection target", providers.ErrUnavailable)
	}
	result, err := p.Fetch(ctx, providers.SourceRef{URL: trimmed})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	text := ""
	if result.FullText != nil {
		text = *result.FullText
	}
	title := ""
	if result.Title != nil {
		title = *result.Title
	}
	return []models.SourceItem{{
		SourceID:           result.SourceID,
		CampaignID:         target.CampaignID,
		GlobalDedupKey:     result.GlobalDedupKey,
		SourceType:         result.SourceType,
		Provider:           p.ID(),
		URL:                result.URL,
		CanonicalURL:       result.CanonicalURL,
		Title:              title,
		Text:               text,
		Snippet:            result.Snippet,
		Language:           "unknown",
		CollectedAt:        now,
		Engagement:         map[string]any{},
		LinkedURLs:         []string{trimmed},
		AvailabilityStatus: result.AvailabilityStatus,
		CollectionQuery:    target.Query,
	}}, nil
}

func (p *Provider) FetchAccount(ctx context.Context, accountRef string) (*models.AccountProfile, error) {
	_ = ctx
	_ = accountRef
	return nil, errors.New("basic_web does not provide account metadata")
}

func (p *Provider) FetchInteractions(ctx context.Context, source models.SourceItem, limit int) ([]models.InteractionEvent, error) {
	_ = ctx
	_ = source
	_ = limit
	return nil, fmt.Errorf("%w: basic_web does not provide interaction data", providers.ErrUnavailable)
}

func htmlToText(value string) string {
	replacer := strings.NewReplacer("<", " <", ">", "> ", "\n", " ", "\t", " ")
	value = replacer.Replace(value)
	var out strings.Builder
	inTag := false
	for _, r := range value {
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

func firstSentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Untitled source"
	}
	if idx := strings.Index(value, "."); idx > 0 && idx < 140 {
		return value[:idx+1]
	}
	return firstN(value, 120)
}

func firstN(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}
