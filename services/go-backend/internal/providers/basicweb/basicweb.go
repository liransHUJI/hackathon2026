package basicweb

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/models"
	"github.com/hnweb/provenance/internal/providers"
	"github.com/hnweb/provenance/internal/ratelimit"
)

type Provider struct {
	client  *http.Client
	limiter *ratelimit.Limiter
}

func New(requestsPerSecond float64) *Provider {
	requestsPerMinute := int(requestsPerSecond * 60)
	if requestsPerMinute <= 0 {
		requestsPerMinute = 60
	}
	return &Provider{
		client:  &http.Client{Timeout: 15 * time.Second},
		limiter: ratelimit.NewPerMinute(requestsPerMinute),
	}
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

	links, err := p.searchFreeWeb(ctx, trimmed, query.MaxResults)
	if err != nil {
		return nil, err
	}
	results := make([]models.SourceResult, 0, len(links))
	for _, link := range links {
		result, err := p.Fetch(ctx, providers.SourceRef{URL: link})
		if err != nil {
			results = append(results, unavailableSearchResult(query, link, err))
			continue
		}
		result.QueryUsed = query.Query
		result.Provider = p.ID()
		results = append(results, *result)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("%w: free web search returned no results", providers.ErrUnavailable)
	}
	return results, nil
}

func (p *Provider) Fetch(ctx context.Context, ref providers.SourceRef) (*models.SourceResult, error) {
	if p.limiter != nil {
		if err := p.limiter.Acquire(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ProvenancePipeline/0.1; +https://localhost)")
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
	text = firstN(text, 8000)
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

func (p *Provider) searchFreeWeb(ctx context.Context, query string, maxResults int) ([]string, error) {
	links, err := p.searchDuckDuckGo(ctx, query, maxResults)
	if err == nil && len(links) > 0 {
		return links, nil
	}
	bingLinks, bingErr := p.searchBing(ctx, query, maxResults)
	if bingErr == nil && len(bingLinks) > 0 {
		return bingLinks, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, bingErr
}

func (p *Provider) searchDuckDuckGo(ctx context.Context, query string, maxResults int) ([]string, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: empty search query", providers.ErrUnavailable)
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 10 {
		maxResults = 10
	}
	if p.limiter != nil {
		if err := p.limiter.Acquire(ctx); err != nil {
			return nil, err
		}
	}
	searchURL := "https://duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ProvenancePipeline/0.1; +https://localhost)")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: free web search failed with HTTP %d", providers.ErrUnavailable, resp.StatusCode)
	}
	links := extractDuckDuckGoLinks(string(body), maxResults)
	if len(links) == 0 {
		return nil, fmt.Errorf("%w: free web search found no result links", providers.ErrUnavailable)
	}
	return links, nil
}

func (p *Provider) searchBing(ctx context.Context, query string, maxResults int) ([]string, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: empty search query", providers.ErrUnavailable)
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 10 {
		maxResults = 10
	}
	if p.limiter != nil {
		if err := p.limiter.Acquire(ctx); err != nil {
			return nil, err
		}
	}
	searchURL := "https://www.bing.com/search?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ProvenancePipeline/0.1; +https://localhost)")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 768*1024))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: free Bing search failed with HTTP %d", providers.ErrUnavailable, resp.StatusCode)
	}
	links := extractBingLinks(string(body), maxResults)
	if len(links) == 0 {
		return nil, fmt.Errorf("%w: free Bing search found no result links", providers.ErrUnavailable)
	}
	return links, nil
}

var resultLinkRE = regexp.MustCompile(`(?is)<a[^>]+class=["'][^"']*result__a[^"']*["'][^>]+href=["']([^"']+)["']|<a[^>]+href=["']([^"']+)["'][^>]+class=["'][^"']*result__a[^"']*["']`)
var bingLinkRE = regexp.MustCompile(`(?is)<li[^>]+class=["'][^"']*b_algo[^"']*["'][^>]*>.*?<a[^>]+href=["'](https?://[^"']+)["']`)

func extractDuckDuckGoLinks(page string, maxResults int) []string {
	matches := resultLinkRE.FindAllStringSubmatch(page, -1)
	seen := map[string]bool{}
	links := make([]string, 0, min(maxResults, len(matches)))
	for _, match := range matches {
		link := ""
		for _, group := range match[1:] {
			if group != "" {
				link = normalizeDuckDuckGoLink(group)
				break
			}
		}
		if link == "" || seen[link] {
			continue
		}
		seen[link] = true
		links = append(links, link)
		if len(links) >= maxResults {
			break
		}
	}
	return links
}

func extractBingLinks(page string, maxResults int) []string {
	matches := bingLinkRE.FindAllStringSubmatch(page, -1)
	seen := map[string]bool{}
	links := make([]string, 0, min(maxResults, len(matches)))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		link := html.UnescapeString(strings.TrimSpace(match[1]))
		if link == "" || seen[link] || strings.Contains(link, "bing.com") || strings.Contains(link, "microsoft.com") {
			continue
		}
		seen[link] = true
		links = append(links, link)
		if len(links) >= maxResults {
			break
		}
	}
	return links
}

func normalizeDuckDuckGoLink(raw string) string {
	value := html.UnescapeString(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.Host == "duckduckgo.com" && parsed.Path == "/l/" {
		redirect := parsed.Query().Get("uddg")
		if redirect != "" {
			if decoded, err := url.QueryUnescape(redirect); err == nil {
				value = decoded
			} else {
				value = redirect
			}
		}
	}
	parsed, err = url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

func unavailableSearchResult(query providers.SourceQuery, link string, err error) models.SourceResult {
	now := time.Now().UTC()
	title := "Unavailable free web result"
	errText := err.Error()
	status := 0
	return models.SourceResult{
		SourceID:           uuid.NewString(),
		GlobalDedupKey:     "unavailable:" + link,
		SourceType:         models.SourceTypeSearchResult,
		Provider:           "basic_web",
		URL:                &link,
		CanonicalURL:       &link,
		Title:              &title,
		Snippet:            "Free web search found this URL, but fetching it failed.",
		ScrapedAt:          now,
		IndexedAt:          &now,
		Engagement:         map[string]any{},
		SourceMetadata:     map[string]any{"fallback": "duckduckgo_html"},
		QueryUsed:          query.Query,
		HTTPStatus:         &status,
		AvailabilityStatus: models.AvailabilityUnknown,
		Error:              &errText,
	}
}

func (p *Provider) Collect(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	trimmed := strings.TrimSpace(target.Query)
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		results, err := p.Search(ctx, providers.SourceQuery{
			Query:      target.Query,
			Source:     p.ID(),
			MaxResults: target.MaxResults,
			ReportID:   target.CampaignID,
		})
		if err != nil {
			return nil, err
		}
		items := make([]models.SourceItem, 0, len(results))
		for _, result := range results {
			items = append(items, sourceResultToItem(result, target))
		}
		return items, nil
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

func sourceResultToItem(result models.SourceResult, target models.CollectionTarget) models.SourceItem {
	now := time.Now().UTC()
	text := result.Snippet
	if result.FullText != nil && strings.TrimSpace(*result.FullText) != "" {
		text = *result.FullText
	}
	title := ""
	if result.Title != nil {
		title = *result.Title
	}
	return models.SourceItem{
		SourceID:           result.SourceID,
		CampaignID:         target.CampaignID,
		GlobalDedupKey:     result.GlobalDedupKey,
		SourceType:         result.SourceType,
		Provider:           pID(result.Provider),
		URL:                result.URL,
		CanonicalURL:       result.CanonicalURL,
		Title:              title,
		Text:               text,
		Snippet:            result.Snippet,
		Language:           "unknown",
		PublishedAt:        result.PublishedAt,
		IndexedAt:          result.IndexedAt,
		CollectedAt:        now,
		Engagement:         result.Engagement,
		LinkedURLs:         urlsFromResult(result),
		AvailabilityStatus: result.AvailabilityStatus,
		CollectionQuery:    target.Query,
		Error:              result.Error,
	}
}

func pID(provider string) string {
	if strings.TrimSpace(provider) == "" {
		return "basic_web"
	}
	return provider
}

func urlsFromResult(result models.SourceResult) []string {
	out := []string{}
	if result.URL != nil && strings.TrimSpace(*result.URL) != "" {
		out = append(out, *result.URL)
	}
	if result.CanonicalURL != nil && strings.TrimSpace(*result.CanonicalURL) != "" && (result.URL == nil || *result.CanonicalURL != *result.URL) {
		out = append(out, *result.CanonicalURL)
	}
	return out
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
