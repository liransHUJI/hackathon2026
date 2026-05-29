package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/llm/gemini"
	"github.com/hnweb/provenance/internal/models"
	"github.com/hnweb/provenance/internal/providers"
	"github.com/hnweb/provenance/internal/scoring"
)

type Engine struct {
	cfg      config.Config
	store    *db.Store
	registry *providers.Registry
	gemini   *gemini.Client
	// inflight guards against overlapping discovery runs for the same campaign. The scheduler
	// ticks on a fixed interval and a single live crawl can outlast that interval, so without
	// this guard multiple RunDiscovery goroutines would stack up on one campaign and corrupt
	// each other's snapshot/status (and waste duplicate Bright Data spend).
	inflight sync.Map
}

func New(cfg config.Config, store *db.Store, registry *providers.Registry, geminiClient *gemini.Client) *Engine {
	return &Engine{cfg: cfg, store: store, registry: registry, gemini: geminiClient}
}

func (e *Engine) CreateCampaign(ctx context.Context, req models.CampaignRequest) (*models.CampaignProfile, error) {
	if strings.TrimSpace(req.ClientName) == "" {
		return nil, errors.New("client_name is required")
	}
	now := time.Now().UTC()
	if len(req.MonitoredTopics) == 0 {
		req.MonitoredTopics = []string{req.ClientName}
	}
	if len(req.Languages) == 0 {
		req.Languages = []string{"en"}
	}
	if req.CrawlBudget.TopNarratives <= 0 {
		req.CrawlBudget.TopNarratives = e.cfg.DefaultTopNarratives
	}
	if req.CrawlBudget.InteractionsPerNarrative <= 0 {
		req.CrawlBudget.InteractionsPerNarrative = e.cfg.InteractionsPerNarrative
	}
	if req.CrawlBudget.MaxCollectionResults <= 0 {
		req.CrawlBudget.MaxCollectionResults = req.CrawlBudget.TopNarratives * 50
	}
	for idx := range req.InterestGroups {
		if req.InterestGroups[idx].GroupID == "" {
			req.InterestGroups[idx].GroupID = uuid.NewString()
		}
	}
	campaign := models.CampaignProfile{
		CampaignID:        uuid.NewString(),
		ClientName:        req.ClientName,
		ClientAliases:     req.ClientAliases,
		Industry:          req.Industry,
		Region:            req.Region,
		MonitoredTopics:   req.MonitoredTopics,
		Opponents:         req.Opponents,
		InterestGroups:    req.InterestGroups,
		ImportantAccounts: req.ImportantAccounts,
		ClientAccounts:    req.ClientAccounts,
		TrustedSources:    req.TrustedSources,
		HostileSources:    req.HostileSources,
		Languages:         req.Languages,
		CrawlBudget:       req.CrawlBudget,
		Status:            models.EngineStatusActive,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := e.store.UpsertCampaign(ctx, campaign); err != nil {
		return nil, err
	}
	return &campaign, nil
}

func (e *Engine) ListCampaigns(ctx context.Context) ([]models.CampaignProfile, error) {
	return e.store.ListCampaigns(ctx)
}

func (e *Engine) UpdateCampaign(ctx context.Context, campaignID string, req models.CampaignRequest) (*models.CampaignProfile, error) {
	current, err := e.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	updated, err := e.CreateCampaignObject(campaignID, req, current.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := e.store.UpsertCampaign(ctx, *updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func (e *Engine) CreateCampaignObject(campaignID string, req models.CampaignRequest, createdAt time.Time) (*models.CampaignProfile, error) {
	if strings.TrimSpace(req.ClientName) == "" {
		return nil, errors.New("client_name is required")
	}
	now := time.Now().UTC()
	if req.CrawlBudget.TopNarratives <= 0 {
		req.CrawlBudget.TopNarratives = e.cfg.DefaultTopNarratives
	}
	if req.CrawlBudget.InteractionsPerNarrative <= 0 {
		req.CrawlBudget.InteractionsPerNarrative = e.cfg.InteractionsPerNarrative
	}
	if req.CrawlBudget.MaxCollectionResults <= 0 {
		req.CrawlBudget.MaxCollectionResults = req.CrawlBudget.TopNarratives * 50
	}
	for idx := range req.InterestGroups {
		if req.InterestGroups[idx].GroupID == "" {
			req.InterestGroups[idx].GroupID = uuid.NewString()
		}
	}
	return &models.CampaignProfile{
		CampaignID:        campaignID,
		ClientName:        req.ClientName,
		ClientAliases:     req.ClientAliases,
		Industry:          req.Industry,
		Region:            req.Region,
		MonitoredTopics:   req.MonitoredTopics,
		Opponents:         req.Opponents,
		InterestGroups:    req.InterestGroups,
		ImportantAccounts: req.ImportantAccounts,
		ClientAccounts:    req.ClientAccounts,
		TrustedSources:    req.TrustedSources,
		HostileSources:    req.HostileSources,
		Languages:         req.Languages,
		CrawlBudget:       req.CrawlBudget,
		Status:            models.EngineStatusActive,
		CreatedAt:         createdAt,
		UpdatedAt:         now,
	}, nil
}

func (e *Engine) RunDiscovery(ctx context.Context, campaignID string) (models.DiscoveryRunResponse, error) {
	// Skip if a crawl for this campaign is already running (e.g. the scheduler ticked while a
	// long live crawl is still in flight). Overlapping runs previously corrupted the dashboard
	// snapshot and left the campaign status stuck on "running".
	if _, loaded := e.inflight.LoadOrStore(campaignID, true); loaded {
		return models.DiscoveryRunResponse{
			CampaignID: campaignID,
			Status:     models.EngineStatusRunning,
			Message:    "discovery already in progress for this campaign",
		}, nil
	}
	defer e.inflight.Delete(campaignID)

	campaign, err := e.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return models.DiscoveryRunResponse{}, err
	}
	if err := e.store.MarkCampaignRunStarted(ctx, campaignID); err != nil {
		return models.DiscoveryRunResponse{}, err
	}
	failures := []models.ProviderFailure{}
	sources := []models.SourceItem{}
	for _, target := range e.collectionTargets(*campaign) {
		for _, provider := range e.registry.CampaignProviders() {
			if !providerSupports(provider, target.Source) {
				continue
			}
			collected, err := provider.Collect(ctx, target)
			if err != nil {
				failures = append(failures, failure(provider.ID(), err))
				continue
			}
			sources = append(sources, collected...)
		}
	}
	sources = dedupeSources(sources)
	return e.processSources(ctx, campaign, sources, failures, true)
}

// processSources runs the shared discovery pipeline (clustering, committee assessment, interaction
// harvesting, actor classification, snapshot) over an already-collected source set. useProvider
// controls whether additional interactions are fetched from live providers; seeded/offline runs
// pass false so only in-cluster siblings are classified.
func (e *Engine) processSources(ctx context.Context, campaign *models.CampaignProfile, sources []models.SourceItem, failures []models.ProviderFailure, useProvider bool) (models.DiscoveryRunResponse, error) {
	campaignID := campaign.CampaignID
	if err := e.store.SaveSourceItems(ctx, campaignID, sources); err != nil {
		return models.DiscoveryRunResponse{}, err
	}
	candidates := e.discoverNarratives(*campaign, sources)
	// LLM committee (single batched call) judges relevance, client-origin and impact BEFORE we
	// spend interaction budget. Irrelevant or client-originated narratives are filtered out here.
	verdicts := e.assessNarratives(ctx, *campaign, candidates, sources)
	narratives := make([]models.NarrativeCluster, 0, len(candidates))
	for idx := range candidates {
		verdict := verdicts[candidates[idx].NarrativeID]
		if !verdict.Relevant || verdict.ClientOriginated {
			continue
		}
		v := verdict
		candidates[idx].CommitteeVerdict = &v
		if v.RelevanceScore > candidates[idx].RelevanceScore {
			candidates[idx].RelevanceScore = v.RelevanceScore
		}
		narratives = append(narratives, candidates[idx])
	}
	// Campaign-wide coordination: detect copy-paste amplification across the entire collected
	// corpus (all source posts) so accounts spreading the same slogan across many narratives are
	// flagged even when any single narrative only sees them once. Folded into per-narrative
	// classification below.
	baseCoord := corpusCoordination(sources, nil)
	// Persisted narratives are tracked separately so the final snapshot reflects only what actually
	// reached the store, even if the run is cut short by the discovery deadline.
	persisted := make([]models.NarrativeCluster, 0, len(narratives))
	totalInteractions := 0
	for idx := range narratives {
		interactionTarget := campaign.CrawlBudget.InteractionsPerNarrative
		if interactionTarget <= 0 {
			interactionTarget = e.cfg.InteractionsPerNarrative
		}
		narrativeSources := sourcesForNarrative(narratives[idx], sources)
		interactions, interactionFailures := e.harvestInteractions(ctx, narrativeSources, interactionTarget, useProvider)
		failures = append(failures, interactionFailures...)
		classifications := e.classifyActors(*campaign, narratives[idx].NarrativeID, narrativeSources, interactions, baseCoord)
		e.completeNarrative(campaign, &narratives[idx], narrativeSources, interactions, classifications)
		// The narrative row must exist before interactions/classifications reference it via FK.
		// Save failures (e.g. a cancelled context near the deadline) are non-fatal: we skip this
		// narrative and still finalize the snapshot/status for everything harvested so far.
		if err := e.store.SaveNarrative(ctx, narratives[idx]); err != nil {
			failures = append(failures, failure("persistence", err))
			break
		}
		if err := e.store.SaveInteractions(ctx, campaignID, narratives[idx].NarrativeID, interactions); err != nil {
			failures = append(failures, failure("persistence", err))
		}
		if err := e.store.SaveActorClassifications(ctx, classifications); err != nil {
			failures = append(failures, failure("persistence", err))
		}
		for _, alert := range alertsForNarrative(narratives[idx]) {
			if err := e.store.SaveAlert(ctx, alert); err != nil {
				failures = append(failures, failure("persistence", err))
			}
		}
		totalInteractions += len(interactions)
		persisted = append(persisted, narratives[idx])
	}
	// Finalize on a detached context so the dashboard snapshot and campaign status are always
	// written, even when the discovery context has already hit its deadline mid-crawl.
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelFinish()
	snapshot := e.snapshot(campaignID, persisted, failures)
	if err := e.store.SaveDashboardSnapshot(finishCtx, snapshot); err != nil {
		failures = append(failures, failure("persistence", err))
	}
	status := models.EngineStatusCompleted
	message := "discovery completed"
	if len(failures) > 0 {
		status = models.EngineStatusDegraded
		message = "discovery completed with provider degradation"
	}
	if len(persisted) == 0 {
		status = models.EngineStatusInsufficientData
		message = "no narratives could be discovered from live providers"
	}
	_ = e.store.MarkCampaignRunCompleted(finishCtx, campaignID, status)
	return models.DiscoveryRunResponse{
		CampaignID:       campaignID,
		Status:           status,
		NarrativesFound:  len(persisted),
		SourcesCollected: len(sources),
		Interactions:     totalInteractions,
		ProviderFailures: failures,
		Message:          message,
	}, nil
}

func (e *Engine) collectionTargets(campaign models.CampaignProfile) []models.CollectionTarget {
	queries := []string{campaign.ClientName}
	queries = append(queries, campaign.ClientAliases...)
	queries = append(queries, campaign.MonitoredTopics...)
	queries = append(queries, campaign.Opponents...)
	for _, group := range campaign.InterestGroups {
		queries = append(queries, group.Keywords...)
		queries = append(queries, group.Hashtags...)
		queries = append(queries, group.Issues...)
	}
	// Always steer collection toward inauthentic-traction discovery by expanding each campaign
	// with bot/AI-coordination intent variants. This keeps harvesting real while biasing retrieval
	// toward narratives where our core differentiation (authentic vs inauthentic traction) matters.
	queries = append(queries, botIntentQueryVariants(campaign, queries)...)
	queries = dedupeStrings(queries)
	max := campaign.CrawlBudget.MaxCollectionResults
	if max <= 0 {
		max = e.cfg.DefaultTopNarratives * 50
	}
	weightedTotal := 0
	for _, query := range queries {
		if strings.TrimSpace(query) == "" {
			continue
		}
		weightedTotal += queryWeight(query)
	}
	if weightedTotal <= 0 {
		weightedTotal = maxInt(1, len(queries))
	}
	targets := []models.CollectionTarget{}
	for _, query := range queries {
		if strings.TrimSpace(query) == "" {
			continue
		}
		// Twitter/X only. Per product requirement, general web sources are NOT used to build
		// narratives; web is reserved solely for confirming whether a narrative originates from
		// the client/affiliates (handled in the committee step).
		targets = append(targets, models.CollectionTarget{
			CampaignID: campaign.CampaignID,
			Query:      query,
			Source:     "x",
			// Bot/AI-intent queries get a larger share of retrieval budget.
			MaxResults: maxInt(10, (max*queryWeight(query))/weightedTotal),
			Languages:  campaign.Languages,
			Region:     campaign.Region,
		})
	}
	return targets
}

func botIntentQueryVariants(campaign models.CampaignProfile, seedQueries []string) []string {
	templates := []string{
		`%s bot network`,
		`%s coordinated inauthentic behavior`,
		`%s astroturf campaign`,
		`%s ai-generated propaganda`,
		`%s deepfake amplification`,
		`%s disinformation amplification`,
		`%s troll farm`,
	}
	variants := []string{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			variants = append(variants, v)
		}
	}
	for _, base := range seedQueries {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		// Hashtag/slogan seeds are already a concrete, targeted coordinated-event query. Expanding
		// them into "#tag bot network" style meta-queries surfaces journalists/researchers writing
		// ABOUT the campaign instead of the amplification network itself, so we collect them as-is.
		if hasBotIntent(base) || isHashtagQuery(base) {
			add(base)
			continue
		}
		for _, tmpl := range templates {
			add(fmt.Sprintf(tmpl, base))
		}
	}
	for _, topic := range campaign.MonitoredTopics {
		topic = strings.TrimSpace(topic)
		if topic == "" || isHashtagQuery(topic) {
			continue
		}
		add(topic + " synthetic engagement")
		add(topic + " coordinated amplification")
	}
	return variants
}

func isHashtagQuery(query string) bool {
	return strings.HasPrefix(strings.TrimSpace(query), "#")
}

func queryWeight(query string) int {
	// Bot-intent and concrete hashtag/slogan queries target the coordinated content directly, so
	// they receive the larger share of the collection budget.
	if hasBotIntent(query) || isHashtagQuery(query) {
		return 4
	}
	return 1
}

func hasBotIntent(query string) bool {
	lower := strings.ToLower(query)
	indicators := []string{
		"bot", "botnet", "coordinated inauthentic", "inauthentic", "astroturf",
		"troll farm", "synthetic engagement", "ai-generated", "ai generated",
		"deepfake", "disinformation", "influence operation", "manipulation campaign",
	}
	for _, marker := range indicators {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func campaignTargetsBotAmplification(campaign models.CampaignProfile) bool {
	for _, v := range campaign.MonitoredTopics {
		if hasBotIntent(v) {
			return true
		}
	}
	for _, g := range campaign.InterestGroups {
		for _, v := range g.Keywords {
			if hasBotIntent(v) {
				return true
			}
		}
		for _, v := range g.Issues {
			if hasBotIntent(v) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) discoverNarratives(campaign models.CampaignProfile, sources []models.SourceItem) []models.NarrativeCluster {
	clientFilter := newClientAccountFilter(campaign)
	buckets := []*narrativeBucket{}
	for idx := range sources {
		source := sources[idx]
		text := firstNonEmpty(source.Text, source.Title, source.Snippet)
		if text == "" {
			continue
		}
		// Drop the client's own / affiliate posts: the manager wants EXTERNAL chatter, not their
		// own messaging amplified back to them.
		if clientFilter.authoredByClient(source) {
			continue
		}
		match := matchBucket(buckets, source, text)
		if match == nil {
			match = &narrativeBucket{repText: text, repSource: source, query: source.CollectionQuery, hashtags: map[string]bool{}}
			buckets = append(buckets, match)
		}
		match.sources = append(match.sources, source)
		match.sourceIDs = append(match.sourceIDs, source.SourceID)
		for _, tag := range source.Hashtags {
			match.hashtags[strings.ToLower(tag)] = true
		}
		if relevance := scoring.Relevance(campaign, text); relevance > match.relevance {
			match.relevance = relevance
		}
		// Promote the most influential post as the representative for titling.
		if source.SourceType == models.SourceTypeXPost && source.Author.FollowersCount > match.repSource.Author.FollowersCount {
			match.repText = text
			match.repSource = source
		}
		if source.PublishedAt != nil {
			if match.first == nil || source.PublishedAt.Before(*match.first) {
				t := *source.PublishedAt
				match.first = &t
			}
			if match.last == nil || source.PublishedAt.After(*match.last) {
				t := *source.PublishedAt
				match.last = &t
			}
		}
	}
	sort.Slice(buckets, func(i, j int) bool {
		return len(buckets[i].sourceIDs)*100+int(buckets[i].relevance*100) > len(buckets[j].sourceIDs)*100+int(buckets[j].relevance*100)
	})
	limit := campaign.CrawlBudget.TopNarratives
	if limit <= 0 {
		limit = e.cfg.DefaultTopNarratives
	}
	if len(buckets) > limit {
		buckets = buckets[:limit]
	}
	now := time.Now().UTC()
	narratives := make([]models.NarrativeCluster, 0, len(buckets))
	for _, bucket := range buckets {
		title, summary := narrativeTitleAndSummary(bucket)
		narratives = append(narratives, models.NarrativeCluster{
			NarrativeID:           uuid.NewString(),
			CampaignID:            campaign.CampaignID,
			Narrative:             title,
			Summary:               summary,
			CanonicalClaims:       []string{summarizeNarrative(bucket.repText)},
			SourceIDs:             dedupeStrings(bucket.sourceIDs),
			FirstSeenAt:           bucket.first,
			LastSeenAt:            bucket.last,
			GeoDistribution:       map[string]float64{},
			SentimentDistribution: map[string]float64{},
			SourceMix:             map[string]int{},
			InteractionTarget:     campaign.CrawlBudget.InteractionsPerNarrative,
			RelevanceScore:        bucket.relevance,
			CreatedAt:             now,
			UpdatedAt:             now,
		})
	}
	return narratives
}

func (e *Engine) harvestInteractions(ctx context.Context, sources []models.SourceItem, target int, useProvider bool) ([]models.InteractionEvent, []models.ProviderFailure) {
	interactions := []models.InteractionEvent{}
	failures := []models.ProviderFailure{}
	if len(sources) == 0 {
		return interactions, failures
	}
	origin := chooseOrigin(sources)

	// 1. Sibling X posts in the cluster are themselves interactions (reply/quote/repost/subtweet)
	//    relative to the origin post. This is free and needs no extra provider calls.
	for _, source := range sources {
		if source.SourceType != models.SourceTypeXPost {
			continue
		}
		itype, include := scoring.ClassifyXInteraction(origin, source)
		if !include {
			continue
		}
		accountID := source.Author.AccountID
		if accountID == "" {
			accountID = source.Author.Handle
		}
		if accountID == "" {
			continue
		}
		interactions = append(interactions, models.InteractionEvent{
			InteractionID:      uuid.NewString(),
			SourceID:           origin.SourceID,
			AccountID:          accountID,
			InteractionType:    itype,
			OccurredAt:         interactionTime(source),
			EngagementSnapshot: source.Engagement,
			ImportanceScore:    source.Author.InfluenceScore,
			Metadata: map[string]any{
				"child_source_id": source.SourceID,
				"handle":          source.Author.Handle,
				"text":            truncateText(source.Text, 280),
				"author":          source.Author,
			},
		})
	}

	// 2. Fetch additional conversation (replies/quotes/reposts) for the origin from the live
	//    provider, bounded by the remaining interaction budget.
	if remaining := target - len(interactions); useProvider && remaining > 0 && origin.Provider != "" {
		for _, provider := range e.registry.CampaignProviders() {
			if provider.ID() != origin.Provider {
				continue
			}
			events, err := provider.FetchInteractions(ctx, origin, remaining)
			if err != nil {
				failures = append(failures, failure(provider.ID(), err))
				continue
			}
			interactions = append(interactions, events...)
		}
	}

	interactions = dedupeInteractions(interactions)
	if len(interactions) > target && target > 0 {
		interactions = interactions[:target]
	}
	return interactions, failures
}

// chooseOrigin selects the most likely origin post of a narrative: the most-followed X account,
// falling back to the earliest source, then the first.
func chooseOrigin(sources []models.SourceItem) models.SourceItem {
	var best *models.SourceItem
	for idx := range sources {
		if sources[idx].SourceType != models.SourceTypeXPost {
			continue
		}
		if best == nil || sources[idx].Author.FollowersCount > best.Author.FollowersCount {
			best = &sources[idx]
		}
	}
	if best != nil {
		return *best
	}
	return sources[0]
}

func interactionTime(source models.SourceItem) time.Time {
	if source.PublishedAt != nil {
		return *source.PublishedAt
	}
	if source.IndexedAt != nil {
		return *source.IndexedAt
	}
	if !source.CollectedAt.IsZero() {
		return source.CollectedAt
	}
	return time.Now().UTC()
}

func truncateText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

func (e *Engine) classifyActors(campaign models.CampaignProfile, narrativeID string, sources []models.SourceItem, interactions []models.InteractionEvent, baseCoord map[string]float64) []models.ActorClassification {
	// accountKey mirrors how harvestInteractions keys accounts (account id, falling back to handle)
	// so source authors and interaction authors line up under one identity.
	accountKey := func(p models.AccountProfile) string {
		if p.AccountID != "" {
			return p.AccountID
		}
		return p.Handle
	}
	accounts := map[string]models.AccountProfile{}
	for _, source := range sources {
		if key := accountKey(source.Author); key != "" {
			accounts[key] = source.Author
		}
	}
	for _, interaction := range interactions {
		if interaction.Metadata == nil {
			continue
		}
		if profile, ok := interaction.Metadata["author"].(models.AccountProfile); ok {
			if key := accountKey(profile); key != "" {
				if _, exists := accounts[key]; !exists {
					accounts[key] = profile
				}
			}
		}
	}
	byAccount := map[string][]models.InteractionEvent{}
	for _, interaction := range interactions {
		byAccount[interaction.AccountID] = append(byAccount[interaction.AccountID], interaction)
	}
	// Make each source author's own post text visible to the scorer (as a non-amplifying "post"
	// pseudo-event) so hashtag-stuffing source posters are evaluated too, not just repliers.
	for _, source := range sources {
		key := accountKey(source.Author)
		if key == "" || strings.TrimSpace(source.Text) == "" {
			continue
		}
		byAccount[key] = append(byAccount[key], models.InteractionEvent{
			AccountID:       key,
			InteractionType: "post",
			Metadata:        map[string]any{"text": source.Text},
		})
	}
	// Detect coordinated inauthentic behavior. The per-narrative pass catches copy that repeats
	// within this cluster; the campaign-wide baseCoord catches accounts spreading the same slogan
	// across many clusters. Both are folded into each account's profile so the bot scorer can flag
	// amplification networks. All signal is from real post text.
	coordination := computeCoordination(interactions)
	botThreshold := 0.65
	// For campaigns explicitly focused on inauthentic amplification, bias toward recall: we still
	// classify from real account behavior, but use a lower cut-off so suspicious actors are not
	// hidden behind an overly conservative threshold.
	if campaignTargetsBotAmplification(campaign) {
		botThreshold = 0.5
	}
	classifications := []models.ActorClassification{}
	now := time.Now().UTC()
	// Classify the union of source authors and interaction authors. Source posters are the ones
	// who actually spread the coordinated copy, so they must be scored too — not just repliers.
	for accountID, account := range accounts {
		if account.AccountID == "" {
			account = models.AccountProfile{AccountID: accountID, Platform: "x", Handle: accountID}
		}
		coord := coordination[accountID]
		if bc := baseCoord[accountID]; bc > coord {
			coord = bc
		}
		if coord > account.CoordinationScore {
			account.CoordinationScore = coord
		}
		score, evidence := scoring.ActorBotScore(account, byAccount[accountID])
		class := models.ActorClassNonBot
		if score >= botThreshold || account.CoordinationScore >= 0.6 {
			class = models.ActorClassBot
		}
		classifications = append(classifications, models.ActorClassification{
			ClassificationID: uuid.NewString(),
			CampaignID:       campaign.CampaignID,
			NarrativeID:      narrativeID,
			AccountID:        accountID,
			Class:            class,
			BotScore:         score,
			Confidence:       confidenceFromEvidence(score, len(evidence)),
			Evidence:         evidence,
			CreatedAt:        now,
		})
	}
	return classifications
}

func (e *Engine) completeNarrative(campaign *models.CampaignProfile, narrative *models.NarrativeCluster, sources []models.SourceItem, interactions []models.InteractionEvent, classifications []models.ActorClassification) {
	auth, inauth, unknown := scoring.AuthenticityPercentages(classifications)
	narrative.AuthenticPercentage = auth
	narrative.InauthenticPercentage = inauth
	narrative.UnknownPercentage = unknown
	narrative.TotalInteractions = len(interactions)
	reach := estimateReach(sources, interactions)
	narrative.ReachEstimate = reach
	narrative.VelocityPerHour = velocity(narrative.FirstSeenAt, narrative.LastSeenAt, len(interactions))
	narrative.Trend = trend(narrative.VelocityPerHour)
	narrative.PopularityScore = scoring.Popularity(len(interactions), reach, narrative.VelocityPerHour)
	narrative.BotCoordinationRisk = inauth / 100
	narrative.ForeignInfluenceRisk = foreignInfluenceRisk(campaign, sources)
	narrative.AIGenerationRisk = aiGenerationRisk(sources)
	narrative.MisinformationRisk = misinformationRisk(narrative.BotCoordinationRisk, narrative.AIGenerationRisk, narrative.ForeignInfluenceRisk)
	narrative.OrganicProminenceScore = 1 - narrative.BotCoordinationRisk
	narrative.OverallRisk = scoring.OverallNarrativeRisk(narrative.MisinformationRisk, narrative.BotCoordinationRisk, narrative.ForeignInfluenceRisk, narrative.AIGenerationRisk, velocityScore(narrative.VelocityPerHour), importantUserScore(sources))
	narrative.RiskLabel = scoring.RiskLabel(narrative.OverallRisk)
	narrative.SourceMix = sourceMix(sources)
	narrative.GeoDistribution = geoDistribution(sources)
	narrative.SentimentDistribution = sentimentDistribution(sources)
	narrative.TopSources = scoring.SourcePopularity(sources, interactions)
	attribution := primarySource(campaign.CampaignID, narrative.NarrativeID, sources)
	narrative.PrimarySourceAttribution = &attribution
	if len(interactions) < narrative.InteractionTarget {
		reason := fmt.Sprintf("provider returned %d interactions, below target %d", len(interactions), narrative.InteractionTarget)
		narrative.InsufficientDataReason = &reason
	}
	narrative.RecommendedPRAction = recommendedAction(*narrative)
	narrative.WhyItMatters = whyItMatters(*narrative)
	narrative.CapitalLossEstimate = scoring.CapitalLossEstimate(*narrative)
	narrative.DecisionExplanation = decisionExplanation(*narrative)
	narrative.SpreadTimeline = spreadTimeline(sources, interactions, classifications)
	narrative.InteractionBreakdown = interactionBreakdown(interactions)
	// Prefer the LLM committee's judgments where available; otherwise enrich the deterministic
	// fallback verdict with metric-derived experts and a capital-loss range so the dashboard stays
	// demo-worthy even when the live committee is unavailable.
	if v := narrative.CommitteeVerdict; v != nil {
		if v.Source != "gemini" {
			enrichHeuristicVerdict(narrative)
		}
		if strings.TrimSpace(v.ImpactSummary) != "" {
			narrative.WhyItMatters = v.ImpactSummary
		}
		if strings.TrimSpace(v.RecommendedAction) != "" {
			narrative.RecommendedPRAction = v.RecommendedAction
		}
		if v.RelevanceScore > narrative.RelevanceScore {
			narrative.RelevanceScore = v.RelevanceScore
		}
		if v.CapitalLoss.Applies && v.CapitalLoss.ExpectedUSD > 0 {
			narrative.CapitalLossEstimate = v.CapitalLoss
		}
	}
	narrative.UpdatedAt = time.Now().UTC()
}

func (e *Engine) snapshot(campaignID string, narratives []models.NarrativeCluster, failures []models.ProviderFailure) models.DashboardSnapshot {
	cards := make([]models.NarrativeCard, 0, len(narratives))
	important := map[string]models.AccountProfile{}
	for idx, narrative := range narratives {
		primaryType := models.SourceAuthenticityUnknown
		if narrative.PrimarySourceAttribution != nil {
			primaryType = narrative.PrimarySourceAttribution.SourceType
		}
		sourcePopularityScore := 0.0
		if len(narrative.TopSources) > 0 {
			sourcePopularityScore = narrative.TopSources[0].PopularityScore
		}
		card := models.NarrativeCard{
			NarrativeID:            narrative.NarrativeID,
			Narrative:              narrative.Narrative,
			Summary:                narrative.Summary,
			PopularityScore:        narrative.PopularityScore,
			PopularityRank:         idx + 1,
			TotalInteractions:      narrative.TotalInteractions,
			ReachEstimate:          narrative.ReachEstimate,
			VelocityPerHour:        narrative.VelocityPerHour,
			Trend:                  narrative.Trend,
			AuthenticPercentage:    narrative.AuthenticPercentage,
			InauthenticPercentage:  narrative.InauthenticPercentage,
			UnknownPercentage:      narrative.UnknownPercentage,
			PrimarySource:          narrative.PrimarySourceAttribution,
			PrimarySourceType:      primaryType,
			TopSources:             narrative.TopSources,
			SourcePopularity:       narrative.TopSources,
			RecommendedPRAction:    narrative.RecommendedPRAction,
			WhyItMatters:           narrative.WhyItMatters,
			CapitalLossEstimate:    narrative.CapitalLossEstimate,
			DashboardPriority:      scoring.DashboardPriority(narrative.PopularityScore, narrative.InauthenticPercentage, sourcePopularityScore, velocityScore(narrative.VelocityPerHour), narrative.RelevanceScore),
			RelevanceScore:         narrative.RelevanceScore,
			ImpactSummary:          impactSummary(narrative),
			OverallRisk:            narrative.OverallRisk,
			RiskLabel:              narrative.RiskLabel,
			BotCoordinationRisk:    narrative.BotCoordinationRisk,
			AIGenerationRisk:       narrative.AIGenerationRisk,
			CommitteeVerdict:       narrative.CommitteeVerdict,
			SpreadTimeline:         narrative.SpreadTimeline,
			InteractionBreakdown:   narrative.InteractionBreakdown,
			Status:                 narrativeStatus(narrative),
			InsufficientDataReason: narrative.InsufficientDataReason,
		}
		cards = append(cards, card)
		for _, source := range narrative.TopSources {
			if source.Handle != "" {
				important[source.AccountID] = models.AccountProfile{AccountID: source.AccountID, Handle: source.Handle, DisplayName: source.DisplayName, InfluenceScore: source.PopularityScore}
			}
		}
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].DashboardPriority > cards[j].DashboardPriority })
	for idx := range cards {
		cards[idx].PopularityRank = idx + 1
	}
	users := make([]models.AccountProfile, 0, len(important))
	for _, user := range important {
		users = append(users, user)
	}
	status := models.EngineStatusCompleted
	if len(failures) > 0 {
		status = models.EngineStatusDegraded
	}
	if len(cards) == 0 {
		status = models.EngineStatusInsufficientData
	}
	return models.DashboardSnapshot{
		SnapshotID:         uuid.NewString(),
		CampaignID:         campaignID,
		Status:             status,
		GeneratedAt:        time.Now().UTC(),
		ExecutiveSummary:   executiveSummary(cards, failures),
		Narratives:         cards,
		GeoSentiment:       map[string]any{},
		ImportantUsers:     users,
		SourceCounts:       map[string]int{"narratives": len(cards)},
		ProviderFailures:   failures,
		RecommendedActions: recommendedActions(cards),
	}
}
