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
)

type Provider struct {
	id           string
	apiKey       string
	unlockerZone string
	datasetID    string
	budgetUSD    float64
	spentUSD     float64
	client       *http.Client
	mu           sync.Mutex
}

func NewX(apiKey string, budgetUSD float64) *Provider {
	return NewXWithDataset(apiKey, "", budgetUSD)
}

func NewXWithDataset(apiKey, datasetID string, budgetUSD float64) *Provider {
	return &Provider{id: "brightdata_x", apiKey: apiKey, datasetID: datasetID, budgetUSD: budgetUSD, client: &http.Client{Timeout: 90 * time.Second}}
}

func NewWeb(apiKey string, budgetUSD float64) *Provider {
	return NewWebWithUnlocker(apiKey, "", budgetUSD)
}

func NewWebWithUnlocker(apiKey, unlockerZone string, budgetUSD float64) *Provider {
	return &Provider{id: "brightdata_web", apiKey: apiKey, unlockerZone: unlockerZone, budgetUSD: budgetUSD, client: &http.Client{Timeout: 60 * time.Second}}
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
	} else {
		items, err = p.collectWeb(ctx, target)
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
		return p.collectDataset(ctx, target)
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
	target := models.CollectionTarget{
		CampaignID: source.CampaignID,
		Query:      firstNonEmptyString(firstNonEmpty(source.URL, source.CanonicalURL), source.Author.Handle, source.Text),
		Source:     "x_interactions",
		MaxResults: limit,
	}
	items, err := p.collectDataset(ctx, target)
	if err != nil {
		return nil, err
	}
	events := make([]models.InteractionEvent, 0, len(items))
	for _, item := range items {
		accountID := item.Author.AccountID
		if accountID == "" {
			accountID = item.Author.Handle
		}
		events = append(events, models.InteractionEvent{
			InteractionID:      uuid.NewString(),
			SourceID:           source.SourceID,
			AccountID:          accountID,
			InteractionType:    inferInteractionType(item),
			OccurredAt:         firstTime(item.PublishedAt, item.IndexedAt, item.CollectedAt),
			EngagementSnapshot: item.Engagement,
			ImportanceScore:    item.Author.InfluenceScore,
			Metadata: map[string]any{
				"provider":        p.id,
				"child_source_id": item.SourceID,
				"url":             item.URL,
			},
		})
	}
	return events, nil
}

func (p *Provider) collectDataset(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error) {
	if err := p.ensureAvailable(0.01); err != nil {
		return nil, err
	}
	if p.datasetID == "" {
		return nil, fmt.Errorf("%w: %s requires BRIGHTDATA_X_DATASET_ID", providers.ErrUnavailable, p.id)
	}
	requestBody := []map[string]any{{
		"query":        target.Query,
		"keyword":      target.Query,
		"search":       target.Query,
		"limit":        target.MaxResults,
		"num_of_posts": target.MaxResults,
	}}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	triggerURL := "https://api.brightdata.com/datasets/v3/trigger?dataset_id=" + url.QueryEscape(p.datasetID)
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
	return p.pollSnapshot(ctx, snapshotID, target)
}

func (p *Provider) pollSnapshot(ctx context.Context, snapshotID string, target models.CollectionTarget) ([]models.SourceItem, error) {
	snapshotURL := "https://api.brightdata.com/datasets/v3/snapshot/" + url.PathEscape(snapshotID) + "?format=json"
	var lastBody string
	for attempt := 0; attempt < 18; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, snapshotURL, nil)
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
		lastBody = string(body)
		if resp.StatusCode == http.StatusOK && len(body) > 0 && !bytes.Contains(bytes.ToLower(body), []byte("running")) {
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
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(2+attempt) * time.Second):
		}
	}
	return nil, fmt.Errorf("%w: bright data snapshot %s did not complete: %s", providers.ErrUnavailable, snapshotID, truncate(lastBody, 300))
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

func (p *Provider) mapDatasetRows(rows []map[string]any, target models.CollectionTarget) []models.SourceItem {
	items := make([]models.SourceItem, 0, len(rows))
	now := time.Now().UTC()
	for _, row := range rows {
		text := stringField(row, "text", "content", "post_text", "tweet_text", "description", "caption")
		urlValue := stringField(row, "url", "post_url", "tweet_url", "link")
		handle := strings.TrimPrefix(stringField(row, "author_handle", "user_handle", "username", "screen_name", "handle"), "@")
		accountID := stringField(row, "author_id", "user_id", "account_id", "id")
		if accountID == "" && handle != "" {
			accountID = "x:" + strings.ToLower(handle)
		}
		published := parseAnyTime(stringField(row, "date", "created_at", "timestamp", "published_at"))
		sourceID := stringField(row, "post_id", "tweet_id", "id")
		if sourceID == "" {
			sourceID = uuid.NewString()
		}
		var urlPtr *string
		if urlValue != "" {
			urlPtr = &urlValue
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
				DisplayName:      stringField(row, "author_name", "name", "display_name"),
				Bio:              stringField(row, "bio", "description"),
				DeclaredLocation: stringField(row, "location", "user_location"),
				FollowersCount:   int64Field(row, "followers", "followers_count"),
				FollowingCount:   int64Field(row, "following", "following_count"),
				Verified:         boolField(row, "verified", "is_verified"),
			},
			PublishedAt: published,
			CollectedAt: now,
			Engagement: map[string]any{
				"likes":   int64Field(row, "likes", "like_count"),
				"reposts": int64Field(row, "retweets", "reposts", "retweet_count"),
				"replies": int64Field(row, "replies", "reply_count"),
				"quotes":  int64Field(row, "quotes", "quote_count"),
				"views":   int64Field(row, "views", "view_count"),
			},
			LinkedURLs:         splitURLField(row),
			Hashtags:           splitHashTags(text),
			AvailabilityStatus: models.AvailabilityAvailable,
			CollectionQuery:    target.Query,
		}
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
	lower := strings.ToLower(item.Text)
	switch {
	case strings.Contains(lower, " rt ") || strings.Contains(lower, "repost"):
		return "repost"
	case strings.Contains(lower, "quote"):
		return "quote"
	case strings.HasPrefix(strings.TrimSpace(lower), "@"):
		return "reply"
	default:
		return "post"
	}
}
