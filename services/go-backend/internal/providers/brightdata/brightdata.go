package brightdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/models"
	"github.com/hnweb/provenance/internal/providers"
	"github.com/hnweb/provenance/internal/scoring"
)

type Provider struct {
	id           string
	apiKey       string
	unlockerZone string
	serpZone     string
	datasetID    string
	budgetUSD    float64
	spentUSD     float64
	client       *http.Client
	mu           sync.Mutex
}

func NewX(apiKey string, budgetUSD float64) *Provider {
	return NewXWithDataset(apiKey, "", "", budgetUSD)
}

func NewXWithDataset(apiKey, datasetID, serpZone string, budgetUSD float64) *Provider {
	return &Provider{id: "brightdata_x", apiKey: apiKey, datasetID: datasetID, serpZone: serpZone, budgetUSD: budgetUSD, client: &http.Client{Timeout: 180 * time.Second}}
}

func NewWeb(apiKey string, budgetUSD float64) *Provider {
	return NewWebWithUnlocker(apiKey, "", "", budgetUSD)
}

func NewWebWithUnlocker(apiKey, unlockerZone, serpZone string, budgetUSD float64) *Provider {
	return &Provider{id: "brightdata_web", apiKey: apiKey, unlockerZone: unlockerZone, serpZone: serpZone, budgetUSD: budgetUSD, client: &http.Client{Timeout: 60 * time.Second}}
}

func (p *Provider) ID() string {
	return p.id
}

func (p *Provider) Capabilities() providers.ProviderCapabilities {
	sourceTypes := []models.SourceType{models.SourceTypeWebArticle, models.SourceTypeSearchResult}
	supportsX := false
	if p.id == "brightdata_x" {
		sourceTypes = []models.SourceType{models.SourceTypeXPost}
		supportsX = true
	}
	return providers.ProviderCapabilities{
		SupportedSourceTypes:        sourceTypes,
		SupportsXSearch:             supportsX,
		ReturnsUnavailableEvidence:  true,
		SupportsFullTextFetch:       true,
		ReliablePublishedTimestamps: true,
		CostModel:                   "estimated per search/fetch call",
		RateLimitProfile:            "brightdata",
	}
}

func (p *Provider) Search(ctx context.Context, query providers.SourceQuery) ([]models.SourceResult, error) {
	target := models.CollectionTarget{
		CampaignID: query.ReportID,
		Query:      query.Query,
		Source:     query.Source,
		MaxResults: query.MaxResults,
	}
	var (
		items []models.SourceItem
		err   error
	)
	if p.id == "brightdata_x" {
		items, err = p.collectDataset(ctx, target)
	} else if isExplicitURL(query.Query) {
		items, err = p.collectWeb(ctx, target)
	} else {
		items, err = p.collectSERP(ctx, target)
	}
	if err != nil {
		return nil, err
	}
	results := make([]models.SourceResult, 0, len(items))
	for _, item := range items {
		result := sourceItemToResult(item)
		result.QueryUsed = query.Query
		results = append(results, result)
	}
	return results, nil
}

func (p *Provider) Fetch(ctx context.Context, ref providers.SourceRef) (*models.SourceResult, error) {
	if strings.TrimSpace(ref.URL) == "" {
		return nil, errors.New("bright data fetch requires a URL")
	}
	items, err := p.collectWeb(ctx, models.CollectionTarget{
		Query:      ref.URL,
		Source:     p.id,
		MaxResults: 1,
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: bright data fetch returned no result", providers.ErrUnavailable)
	}
	result := sourceItemToResult(items[0])
	result.QueryUsed = ref.URL
	return &result, nil
}

func (p *Provider) Collect(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	if p.id == "brightdata_x" {
		items, err := p.collectDataset(ctx, target)
		if err == nil && len(items) > 0 {
			return items, nil
		}
		// Fallback: when the X dataset is unavailable or empty, surface tweet links via SERP.
		if _, isURL := normalizeXStatusURL(target.Query); !isURL && strings.TrimSpace(p.serpZone) != "" {
			if fallback, fbErr := p.collectXViaSERP(ctx, target); fbErr == nil && len(fallback) > 0 {
				return fallback, nil
			}
		}
		return items, err
	}
	return p.collectWeb(ctx, target)
}

func (p *Provider) FetchAccount(ctx context.Context, accountRef string) (*models.AccountProfile, error) {
	_ = ctx
	if strings.TrimSpace(accountRef) == "" {
		return nil, fmt.Errorf("%w: empty account ref", providers.ErrUnavailable)
	}
	return nil, fmt.Errorf("%w: %s account enrichment requires a configured social dataset endpoint", providers.ErrUnavailable, p.id)
}

func (p *Provider) FetchInteractions(ctx context.Context, source models.SourceItem, limit int) ([]models.InteractionEvent, error) {
	if p.id != "brightdata_x" {
		return nil, fmt.Errorf("%w: interactions are only available from social providers", providers.ErrUnavailable)
	}
	query := conversationQuery(source)
	if query == "" {
		return nil, fmt.Errorf("%w: source had no usable conversation signal", providers.ErrUnavailable)
	}
	target := models.CollectionTarget{
		CampaignID: source.CampaignID,
		Query:      query,
		Source:     "x_interactions",
		MaxResults: limit,
	}
	items, err := p.collectDataset(ctx, target)
	if err != nil {
		if strings.TrimSpace(p.serpZone) != "" {
			if fallback, fbErr := p.collectXViaSERP(ctx, target); fbErr == nil {
				items = fallback
			} else {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	events := make([]models.InteractionEvent, 0, len(items))
	for _, item := range items {
		itype, include := scoring.ClassifyXInteraction(source, item)
		if !include {
			continue
		}
		accountID := item.Author.AccountID
		if accountID == "" {
			accountID = item.Author.Handle
		}
		if accountID == "" {
			continue
		}
		events = append(events, models.InteractionEvent{
			InteractionID:      uuid.NewString(),
			SourceID:           source.SourceID,
			AccountID:          accountID,
			InteractionType:    itype,
			OccurredAt:         firstTime(item.PublishedAt, item.IndexedAt, item.CollectedAt),
			EngagementSnapshot: item.Engagement,
			ImportanceScore:    item.Author.InfluenceScore,
			Metadata: map[string]any{
				"provider":        p.id,
				"child_source_id": item.SourceID,
				"url":             derefStr(item.URL),
				"handle":          item.Author.Handle,
				"text":            truncate(item.Text, 280),
				"author":          item.Author,
			},
		})
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: no interactions discovered for the source post", providers.ErrUnavailable)
	}
	return events, nil
}

// conversationQuery builds a keyword to discover the conversation around a post: the author
// handle captures replies/quotes/reposts, biased toward the post's top hashtag when present.
func conversationQuery(source models.SourceItem) string {
	handle := strings.TrimPrefix(strings.TrimSpace(source.Author.Handle), "@")
	if handle != "" {
		if len(source.Hashtags) > 0 {
			return "@" + handle + " " + source.Hashtags[0]
		}
		return "@" + handle
	}
	if len(source.Hashtags) > 0 {
		return source.Hashtags[0]
	}
	fields := strings.Fields(source.Text)
	if len(fields) > 6 {
		fields = fields[:6]
	}
	return strings.Join(fields, " ")
}

func derefStr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (p *Provider) collectDataset(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	if p.datasetID == "" {
		return nil, fmt.Errorf("%w: %s requires BRIGHTDATA_X_DATASET_ID", providers.ErrUnavailable, p.id)
	}
	if err := p.ensureAvailable(0.02); err != nil {
		return nil, err
	}
	triggerURL := "https://api.brightdata.com/datasets/v3/trigger?dataset_id=" + url.QueryEscape(p.datasetID)
	var inputs []map[string]any
	if postURL, ok := normalizeXStatusURL(target.Query); ok {
		// Collect a specific X/Twitter status by URL.
		triggerURL += "&format=json"
		inputs = []map[string]any{{"url": postURL}}
	} else {
		// Discover posts by keyword/hashtag for campaign monitoring.
		keyword := strings.TrimSpace(target.Query)
		if keyword == "" {
			return nil, fmt.Errorf("%w: %s keyword discovery requires a non-empty query", providers.ErrUnavailable, p.id)
		}
		num := target.MaxResults
		if num <= 0 {
			num = 20
		}
		if num > 100 {
			num = 100
		}
		triggerURL += "&type=discover_new&discover_by=keyword&format=json"
		inputs = []map[string]any{{"keyword": keyword, "num_of_posts": num}}
	}
	body, err := json.Marshal(inputs)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, triggerURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	req.Header.Set("content-type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: bright data dataset trigger failed with %d: %s", providers.ErrUnavailable, resp.StatusCode, truncate(string(respBody), 300))
	}
	var trigger struct {
		SnapshotID string `json:"snapshot_id"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &trigger); err != nil {
		return nil, err
	}
	snapshotID := trigger.SnapshotID
	if snapshotID == "" {
		snapshotID = trigger.ID
	}
	if snapshotID == "" {
		return nil, fmt.Errorf("%w: bright data trigger did not return snapshot_id", providers.ErrUnavailable)
	}
	return p.pollDataset(ctx, snapshotID, target)
}

func (p *Provider) pollDataset(ctx context.Context, snapshotID string, target models.CollectionTarget) ([]models.SourceItem, error) {
	statusURL := "https://api.brightdata.com/datasets/v3/progress/" + url.PathEscape(snapshotID)
	var lastStatus string
	for attempt := range 40 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("authorization", "Bearer "+p.apiKey)
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			lastStatus = string(body)
		} else {
			var progress struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(body, &progress); err == nil {
				lastStatus = progress.Status
				switch progress.Status {
				case "ready":
					return p.downloadSnapshot(ctx, snapshotID, target)
				case "failed":
					return nil, fmt.Errorf("%w: bright data dataset snapshot %s failed", providers.ErrUnavailable, snapshotID)
				}
			} else {
				lastStatus = string(body)
			}
		}
		delay := min(time.Duration(3+attempt)*time.Second, 8*time.Second)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, fmt.Errorf("%w: bright data dataset snapshot %s was not ready: %s", providers.ErrUnavailable, snapshotID, truncate(lastStatus, 300))
}

func (p *Provider) downloadSnapshot(ctx context.Context, snapshotID string, target models.CollectionTarget) ([]models.SourceItem, error) {
	snapshotURL := "https://api.brightdata.com/datasets/v3/snapshot/" + url.PathEscape(snapshotID) + "?format=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, snapshotURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: bright data snapshot download failed with %d: %s", providers.ErrUnavailable, resp.StatusCode, truncate(string(body), 300))
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err == nil {
		return p.mapDatasetRows(rows, target), nil
	}
	var wrapped struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Data != nil {
		return p.mapDatasetRows(wrapped.Data, target), nil
	}
	return nil, fmt.Errorf("%w: bright data snapshot returned unrecognized JSON: %s", providers.ErrUnavailable, truncate(string(body), 300))
}

func (p *Provider) collectWeb(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	if err := p.ensureAvailable(0.002); err != nil {
		return nil, err
	}
	targetURL := target.Query
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "https://www.google.com/search?q=" + url.QueryEscape(target.Query)
	}
	requestBody := map[string]any{
		"url":         targetURL,
		"format":      "raw",
		"data_format": "markdown",
	}
	if strings.TrimSpace(p.unlockerZone) != "" {
		requestBody["zone"] = p.unlockerZone
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.brightdata.com/request", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	req.Header.Set("content-type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: bright data web unlocker failed with %d: %s", providers.ErrUnavailable, resp.StatusCode, truncate(string(respBody), 300))
	}
	text := strings.TrimSpace(string(respBody))
	text = truncate(text, 8000)
	now := time.Now().UTC()
	item := models.SourceItem{
		SourceID:           uuid.NewString(),
		CampaignID:         target.CampaignID,
		GlobalDedupKey:     "brightdata_web:" + stableKey(targetURL+text),
		SourceType:         models.SourceTypeWebArticle,
		Provider:           p.id,
		URL:                &targetURL,
		CanonicalURL:       &targetURL,
		Title:              firstLine(text),
		Text:               text,
		Snippet:            truncate(text, 280),
		Language:           "unknown",
		CollectedAt:        now,
		Engagement:         map[string]any{},
		LinkedURLs:         []string{targetURL},
		AvailabilityStatus: models.AvailabilityAvailable,
		CollectionQuery:    target.Query,
	}
	return []models.SourceItem{item}, nil
}

func (p *Provider) collectSERP(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	if strings.TrimSpace(p.serpZone) == "" {
		return nil, fmt.Errorf("%w: %s requires BRIGHTDATA_SERP_ZONE for search result extraction", providers.ErrUnavailable, p.id)
	}
	if err := p.ensureAvailable(0.001); err != nil {
		return nil, err
	}
	queryURL := "https://www.google.com/search?q=" + url.QueryEscape(target.Query) + "&brd_json=1&gl=us&hl=en"
	requestBody := map[string]any{
		"zone":   p.serpZone,
		"url":    queryURL,
		"format": "raw",
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.brightdata.com/request", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	req.Header.Set("content-type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: bright data SERP failed with %d: %s", providers.ErrUnavailable, resp.StatusCode, truncate(string(respBody), 300))
	}
	return mapSERPResponse(respBody, target, p.id)
}

func mapSERPResponse(body []byte, target models.CollectionTarget, providerID string) ([]models.SourceItem, error) {
	var parsed struct {
		Organic []struct {
			Rank        int    `json:"rank"`
			GlobalRank  int    `json:"global_rank"`
			Title       string `json:"title"`
			Link        string `json:"link"`
			Description string `json:"description"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: bright data SERP returned non-JSON response: %w", providers.ErrUnavailable, err)
	}
	if len(parsed.Organic) == 0 {
		return nil, fmt.Errorf("%w: bright data SERP returned no organic results", providers.ErrUnavailable)
	}
	limit := target.MaxResults
	if limit <= 0 || limit > len(parsed.Organic) {
		limit = len(parsed.Organic)
	}
	now := time.Now().UTC()
	items := make([]models.SourceItem, 0, limit)
	for _, result := range parsed.Organic[:limit] {
		link := strings.TrimSpace(result.Link)
		if link == "" {
			continue
		}
		urlCopy := link
		text := strings.Join(strings.Fields(result.Title+" "+result.Description), " ")
		items = append(items, models.SourceItem{
			SourceID:       uuid.NewString(),
			CampaignID:     target.CampaignID,
			GlobalDedupKey: providerID + ":serp:" + stableKey(link),
			SourceType:     models.SourceTypeSearchResult,
			Provider:       providerID,
			URL:            &urlCopy,
			CanonicalURL:   &urlCopy,
			Title:          result.Title,
			Text:           text,
			Snippet:        result.Description,
			Language:       "unknown",
			CollectedAt:    now,
			Engagement: map[string]any{
				"rank":        result.Rank,
				"global_rank": result.GlobalRank,
			},
			LinkedURLs:         []string{link},
			AvailabilityStatus: models.AvailabilityAvailable,
			CollectionQuery:    target.Query,
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: bright data SERP results had no links", providers.ErrUnavailable)
	}
	return items, nil
}

func (p *Provider) mapDatasetRows(rows []map[string]any, target models.CollectionTarget) []models.SourceItem {
	items := make([]models.SourceItem, 0, len(rows))
	now := time.Now().UTC()
	for _, row := range rows {
		text := stringField(row, "text", "content", "post_text", "tweet_text", "description", "caption")
		urlValue := stringField(row, "url", "post_url", "tweet_url", "link")
		handle := strings.TrimPrefix(stringField(row, "user_posted", "author_handle", "user_handle", "username", "screen_name", "handle"), "@")
		accountID := stringField(row, "user_id", "author_id", "account_id")
		if accountID == "" && handle != "" {
			accountID = "x:" + strings.ToLower(handle)
		}
		published := parseAnyTime(stringField(row, "date_posted", "date", "created_at", "timestamp", "published_at"))
		sourceID := stringField(row, "post_id", "tweet_id", "id")
		if sourceID == "" {
			sourceID = uuid.NewString()
		}
		var urlPtr *string
		if urlValue != "" {
			urlPtr = &urlValue
		}
		var profileURL *string
		if handle != "" {
			pu := "https://x.com/" + handle
			profileURL = &pu
		}
		hashtags := splitHashTags(text)
		if extra := stringSliceField(row, "hashtags"); len(extra) > 0 {
			hashtags = dedupeTags(append(hashtags, extra...))
		}
		item := models.SourceItem{
			SourceID:       sourceID,
			CampaignID:     target.CampaignID,
			GlobalDedupKey: p.id + ":" + stableKey(firstNonEmptyString(urlValue, sourceID, text)),
			SourceType:     models.SourceTypeXPost,
			Provider:       p.id,
			URL:            urlPtr,
			CanonicalURL:   urlPtr,
			Title:          truncate(text, 90),
			Text:           text,
			Snippet:        truncate(text, 280),
			Language:       stringField(row, "language", "lang"),
			Author: models.AccountProfile{
				AccountID:        accountID,
				Platform:         "x",
				Handle:           handle,
				DisplayName:      stringField(row, "name", "author_name", "display_name"),
				ProfileURL:       profileURL,
				Bio:              stringField(row, "biography", "bio", "user_biography"),
				DeclaredLocation: stringField(row, "location", "user_location"),
				FollowersCount:   int64Field(row, "followers", "followers_count"),
				FollowingCount:   int64Field(row, "following", "following_count"),
				Verified:         boolField(row, "is_verified", "verified"),
			},
			PublishedAt: published,
			CollectedAt: now,
			Engagement: map[string]any{
				"likes":   int64Field(row, "likes", "like_count"),
				"reposts": int64Field(row, "reposts", "retweets", "retweet_count"),
				"replies": int64Field(row, "replies", "reply_count"),
				"quotes":  int64Field(row, "quotes", "quote_count"),
				"views":   int64Field(row, "views", "view_count"),
			},
			LinkedURLs:         splitURLField(row),
			Hashtags:           hashtags,
			AvailabilityStatus: models.AvailabilityAvailable,
			CollectionQuery:    target.Query,
		}
		annotateInteractionMeta(row, &item)
		item.Author.InfluenceScore = influence(item.Author.FollowersCount, item.Author.Verified)
		item.Author.BotLikelihood = botLikelihood(item.Author, item.Text)
		item.Author.ReliabilityScore = 1 - item.Author.BotLikelihood
		item.Author.AccountType = accountType(item.Author)
		items = append(items, item)
	}
	return items
}

func sourceItemToResult(item models.SourceItem) models.SourceResult {
	title := item.Title
	if strings.TrimSpace(title) == "" {
		title = firstLine(item.Text)
	}
	fullText := item.Text
	if strings.TrimSpace(fullText) == "" {
		fullText = item.Snippet
	}
	fullText = truncate(fullText, 8000)
	now := time.Now().UTC()
	scrapedAt := item.CollectedAt
	if scrapedAt.IsZero() {
		scrapedAt = now
	}
	return models.SourceResult{
		SourceID:       firstNonEmptyString(item.SourceID, uuid.NewString()),
		GlobalDedupKey: firstNonEmptyString(item.GlobalDedupKey, item.Provider+":"+stableKey(firstNonEmptyString(firstNonEmpty(item.URL, item.CanonicalURL), item.Text))),
		SourceType:     item.SourceType,
		Provider:       item.Provider,
		URL:            item.URL,
		CanonicalURL:   item.CanonicalURL,
		Title:          &title,
		Snippet:        firstNonEmptyString(item.Snippet, truncate(fullText, 280)),
		FullText:       &fullText,
		AuthorName:     stringPtrIfNotEmpty(item.Author.DisplayName),
		AuthorHandle:   stringPtrIfNotEmpty(item.Author.Handle),
		AuthorURL:      item.Author.ProfileURL,
		PublishedAt:    item.PublishedAt,
		IndexedAt:      item.IndexedAt,
		ScrapedAt:      scrapedAt,
		Engagement:     item.Engagement,
		SourceMetadata: map[string]any{
			"language":         item.Language,
			"collection_query": item.CollectionQuery,
			"linked_urls":      item.LinkedURLs,
			"hashtags":         item.Hashtags,
		},
		RawPayloadRef:      item.RawPayloadRef,
		QueryUsed:          item.CollectionQuery,
		AvailabilityStatus: item.AvailabilityStatus,
		Error:              item.Error,
	}
}

func (p *Provider) ensureAvailable(estimatedCost float64) error {
	if p.apiKey == "" {
		return fmt.Errorf("%w: %s requires BRIGHTDATA_API_KEY", providers.ErrUnavailable, p.id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.budgetUSD > 0 && p.spentUSD+estimatedCost > p.budgetUSD {
		return fmt.Errorf("%w: %s budget guard refused estimated cost %.4f", providers.ErrUnavailable, p.id, estimatedCost)
	}
	p.spentUSD += estimatedCost
	return nil
}

func stringField(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key]; ok && value != nil {
			switch typed := value.(type) {
			case string:
				return strings.TrimSpace(typed)
			case fmt.Stringer:
				return strings.TrimSpace(typed.String())
			default:
				text := strings.TrimSpace(fmt.Sprint(typed))
				if text != "<nil>" {
					return text
				}
			}
		}
	}
	return ""
}

func int64Field(row map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := row[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case int:
			return int64(typed)
		case int64:
			return typed
		case float64:
			return int64(typed)
		case json.Number:
			parsed, _ := typed.Int64()
			return parsed
		case string:
			var parsed int64
			_, _ = fmt.Sscan(strings.ReplaceAll(typed, ",", ""), &parsed)
			return parsed
		}
	}
	return 0
}

func boolField(row map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := row[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			return strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
		}
	}
	return false
}

func parseAnyTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	formats := []string{time.RFC3339, time.RFC1123, "2006-01-02 15:04:05", "2006-01-02"}
	for _, format := range formats {
		if parsed, err := time.Parse(format, value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func firstNonEmpty(values ...*string) string {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			return strings.TrimSpace(*value)
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return uuid.NewString()
}

func stringPtrIfNotEmpty(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func isExplicitURL(value string) bool {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func normalizeXStatusURL(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host != "x.com" && host != "twitter.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 3 || parts[0] == "" || parts[1] != "status" || parts[2] == "" {
		return "", false
	}
	parsed.Scheme = "https"
	parsed.Host = "x.com"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func firstTime(values ...any) time.Time {
	for _, value := range values {
		switch typed := value.(type) {
		case *time.Time:
			if typed != nil {
				return *typed
			}
		case time.Time:
			if !typed.IsZero() {
				return typed
			}
		}
	}
	return time.Now().UTC()
}

func stableKey(value string) string {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	if len(value) > 96 {
		value = value[:96]
	}
	return strings.NewReplacer(" ", "-", "/", "-", ":", "-", "?", "-").Replace(value)
}

func truncate(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Collected web result"
	}
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		value = value[:idx]
	}
	return truncate(value, 120)
}

func splitURLField(row map[string]any) []string {
	raw := stringField(row, "urls", "linked_urls", "expanded_url")
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n'
	})
	out := []string{}
	for _, part := range parts {
		if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") {
			out = append(out, part)
		}
	}
	return out
}

func splitHashTags(text string) []string {
	out := []string{}
	for _, field := range strings.Fields(text) {
		field = strings.Trim(field, ".,:;!?()[]{}\"'")
		if strings.HasPrefix(field, "#") {
			out = append(out, field)
		}
	}
	return out
}

// stringSliceField reads an array (or comma/space separated string) field from a dataset row.
func stringSliceField(row map[string]any, keys ...string) []string {
	out := []string{}
	for _, key := range keys {
		value, ok := row[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case []any:
			for _, entry := range typed {
				if s := strings.TrimSpace(fmt.Sprint(entry)); s != "" && s != "<nil>" {
					out = append(out, s)
				}
			}
		case []string:
			for _, entry := range typed {
				if s := strings.TrimSpace(entry); s != "" {
					out = append(out, s)
				}
			}
		case string:
			for _, part := range strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == '\n' }) {
				if s := strings.TrimSpace(part); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func dedupeTags(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

// annotateInteractionMeta records reply/quote/repost relationship hints on the source item
// so downstream classification can label interactions as reply/quote/repost/subtweet.
func annotateInteractionMeta(row map[string]any, item *models.SourceItem) {
	if item.Engagement == nil {
		item.Engagement = map[string]any{}
	}
	if v := stringField(row, "quoted_post", "quoted_post_id", "quote_id", "quoted_url", "quoted_post_url"); v != "" {
		item.Engagement["is_quote"] = true
		item.Engagement["quoted_ref"] = v
	} else if boolField(row, "is_quote", "is_quoted") {
		item.Engagement["is_quote"] = true
	}
	if v := stringField(row, "replied_to", "parent_post_id", "in_reply_to", "reply_to", "reply_to_post_id", "parent_post_url"); v != "" {
		item.Engagement["is_reply"] = true
		item.Engagement["parent_ref"] = v
	} else if boolField(row, "is_reply", "is_comment") {
		item.Engagement["is_reply"] = true
	}
	if boolField(row, "is_retweet", "is_repost", "reposted") {
		item.Engagement["is_repost"] = true
	} else if v := stringField(row, "retweeted_post", "reposted_post", "retweeted_post_url"); v != "" {
		item.Engagement["is_repost"] = true
		item.Engagement["repost_ref"] = v
	}
	if tags := stringSliceField(row, "tagged_users", "mentions", "mentioned_users"); len(tags) > 0 {
		item.MentionedEntities = dedupeTags(append(item.MentionedEntities, tags...))
	}
}

// handleFromTweetURL extracts the author handle from an x.com/twitter.com status URL.
func handleFromTweetURL(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if host != "x.com" && host != "twitter.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		return "", false
	}
	switch strings.ToLower(parts[0]) {
	case "i", "search", "hashtag", "home", "explore":
		return "", false
	}
	return parts[0], true
}

// collectXViaSERP discovers tweet links through the SERP zone when the X dataset is unavailable.
func (p *Provider) collectXViaSERP(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	serpTarget := target
	serpTarget.Query = "(site:x.com OR site:twitter.com) " + target.Query
	items, err := p.collectSERP(ctx, serpTarget)
	if err != nil {
		return nil, err
	}
	out := make([]models.SourceItem, 0, len(items))
	for _, item := range items {
		link := ""
		if item.URL != nil {
			link = *item.URL
		}
		handle, ok := handleFromTweetURL(link)
		if !ok {
			continue
		}
		profileURL := "https://x.com/" + handle
		item.SourceType = models.SourceTypeXPost
		item.Provider = p.id
		item.CollectionQuery = target.Query
		item.CampaignID = target.CampaignID
		item.Hashtags = splitHashTags(item.Text)
		item.Author = models.AccountProfile{
			AccountID:  "x:" + strings.ToLower(handle),
			Platform:   "x",
			Handle:     handle,
			ProfileURL: &profileURL,
		}
		item.Author.InfluenceScore = influence(item.Author.FollowersCount, item.Author.Verified)
		item.Author.AccountType = accountType(item.Author)
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: SERP fallback found no x.com posts", providers.ErrUnavailable)
	}
	return out, nil
}

func influence(followers int64, verified bool) float64 {
	score := 0.1
	switch {
	case followers > 1_000_000:
		score = 1
	case followers > 100_000:
		score = 0.85
	case followers > 10_000:
		score = 0.65
	case followers > 1_000:
		score = 0.4
	}
	if verified && score < 0.8 {
		score += 0.15
	}
	if score > 1 {
		return 1
	}
	return score
}

func botLikelihood(account models.AccountProfile, text string) float64 {
	score := 0.05
	if account.FollowersCount < 50 && account.FollowingCount > 500 {
		score += 0.35
	}
	lower := strings.ToLower(text)
	if strings.Count(lower, "http") > 2 {
		score += 0.15
	}
	if strings.Contains(lower, "!!!") || strings.Contains(lower, "must share") {
		score += 0.15
	}
	if account.DisplayName == "" || account.Bio == "" {
		score += 0.1
	}
	if score > 1 {
		return 1
	}
	return score
}

func accountType(account models.AccountProfile) string {
	if account.BotLikelihood >= 0.7 {
		return "bot_like"
	}
	if account.Verified || account.FollowersCount > 100_000 {
		return "public_figure"
	}
	return "person"
}

func inferInteractionType(item models.SourceItem) string {
	if flagged(item.Engagement, "is_repost") {
		return "repost"
	}
	if flagged(item.Engagement, "is_quote") {
		return "quote"
	}
	if flagged(item.Engagement, "is_reply") {
		return "reply"
	}
	lower := strings.ToLower(strings.TrimSpace(item.Text))
	switch {
	case strings.HasPrefix(lower, "rt @") || strings.Contains(lower, " rt @"):
		return "repost"
	case strings.HasPrefix(lower, "@"):
		return "reply"
	default:
		return "post"
	}
}

func flagged(meta map[string]any, key string) bool {
	if meta == nil {
		return false
	}
	b, _ := meta[key].(bool)
	return b
}
