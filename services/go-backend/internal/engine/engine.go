package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/models"
	"github.com/hnweb/provenance/internal/providers"
	"github.com/hnweb/provenance/internal/scoring"
)

type Engine struct {
	cfg      config.Config
	store    *db.Store
	registry *providers.Registry
}

func New(cfg config.Config, store *db.Store, registry *providers.Registry) *Engine {
	return &Engine{cfg: cfg, store: store, registry: registry}
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
	if err := e.store.SaveSourceItems(ctx, campaignID, sources); err != nil {
		return models.DiscoveryRunResponse{}, err
	}
	narratives := e.discoverNarratives(*campaign, sources)
	totalInteractions := 0
	for idx := range narratives {
		interactionTarget := campaign.CrawlBudget.InteractionsPerNarrative
		if interactionTarget <= 0 {
			interactionTarget = e.cfg.InteractionsPerNarrative
		}
		narrativeSources := sourcesForNarrative(narratives[idx], sources)
		interactions, interactionFailures := e.harvestInteractions(ctx, narrativeSources, interactionTarget)
		failures = append(failures, interactionFailures...)
		totalInteractions += len(interactions)
		classifications := e.classifyActors(*campaign, narratives[idx].NarrativeID, narrativeSources, interactions)
		if err := e.store.SaveInteractions(ctx, campaignID, narratives[idx].NarrativeID, interactions); err != nil {
			return models.DiscoveryRunResponse{}, err
		}
		if err := e.store.SaveActorClassifications(ctx, classifications); err != nil {
			return models.DiscoveryRunResponse{}, err
		}
		e.completeNarrative(campaign, &narratives[idx], narrativeSources, interactions, classifications)
		if err := e.store.SaveNarrative(ctx, narratives[idx]); err != nil {
			return models.DiscoveryRunResponse{}, err
		}
		for _, alert := range alertsForNarrative(narratives[idx]) {
			if err := e.store.SaveAlert(ctx, alert); err != nil {
				return models.DiscoveryRunResponse{}, err
			}
		}
	}
	snapshot := e.snapshot(campaignID, narratives, failures)
	if err := e.store.SaveDashboardSnapshot(ctx, snapshot); err != nil {
		return models.DiscoveryRunResponse{}, err
	}
	status := models.EngineStatusCompleted
	message := "discovery completed"
	if len(failures) > 0 {
		status = models.EngineStatusDegraded
		message = "discovery completed with provider degradation"
	}
	if len(narratives) == 0 {
		status = models.EngineStatusInsufficientData
		message = "no narratives could be discovered from live providers"
	}
	_ = e.store.MarkCampaignRunCompleted(ctx, campaignID, status)
	return models.DiscoveryRunResponse{
		CampaignID:       campaignID,
		Status:           status,
		NarrativesFound:  len(narratives),
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
	}
	queries = dedupeStrings(queries)
	max := campaign.CrawlBudget.MaxCollectionResults
	if max <= 0 {
		max = e.cfg.DefaultTopNarratives * 50
	}
	targets := []models.CollectionTarget{}
	for _, query := range queries {
		if strings.TrimSpace(query) == "" {
			continue
		}
		targets = append(targets, models.CollectionTarget{
			CampaignID: campaign.CampaignID,
			Query:      query,
			Source:     "x",
			MaxResults: max / maxInt(1, len(queries)),
			Languages:  campaign.Languages,
			Region:     campaign.Region,
		})
		targets = append(targets, models.CollectionTarget{
			CampaignID: campaign.CampaignID,
			Query:      query,
			Source:     "web",
			MaxResults: maxInt(10, max/maxInt(4, len(queries)*4)),
			Languages:  campaign.Languages,
			Region:     campaign.Region,
		})
	}
	return targets
}

func (e *Engine) discoverNarratives(campaign models.CampaignProfile, sources []models.SourceItem) []models.NarrativeCluster {
	type bucket struct {
		key       string
		text      string
		sourceIDs []string
		first     *time.Time
		last      *time.Time
		relevance float64
	}
	buckets := map[string]*bucket{}
	for _, source := range sources {
		text := firstNonEmpty(source.Text, source.Title, source.Snippet)
		if text == "" {
			continue
		}
		key := narrativeKey(text)
		if key == "" {
			continue
		}
		current, ok := buckets[key]
		if !ok {
			current = &bucket{key: key, text: summarizeNarrative(text), relevance: scoring.Relevance(campaign, text)}
			buckets[key] = current
		}
		current.sourceIDs = append(current.sourceIDs, source.SourceID)
		if source.PublishedAt != nil {
			if current.first == nil || source.PublishedAt.Before(*current.first) {
				t := *source.PublishedAt
				current.first = &t
			}
			if current.last == nil || source.PublishedAt.After(*current.last) {
				t := *source.PublishedAt
				current.last = &t
			}
		}
	}
	list := []*bucket{}
	for _, bucket := range buckets {
		list = append(list, bucket)
	}
	sort.Slice(list, func(i, j int) bool {
		return len(list[i].sourceIDs)*100+int(list[i].relevance*100) > len(list[j].sourceIDs)*100+int(list[j].relevance*100)
	})
	limit := campaign.CrawlBudget.TopNarratives
	if limit <= 0 {
		limit = e.cfg.DefaultTopNarratives
	}
	if len(list) > limit {
		list = list[:limit]
	}
	now := time.Now().UTC()
	narratives := make([]models.NarrativeCluster, 0, len(list))
	for _, bucket := range list {
		narratives = append(narratives, models.NarrativeCluster{
			NarrativeID:           uuid.NewString(),
			CampaignID:            campaign.CampaignID,
			Narrative:             bucket.text,
			Summary:               "People are discussing: " + bucket.text,
			CanonicalClaims:       []string{bucket.text},
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

func (e *Engine) harvestInteractions(ctx context.Context, sources []models.SourceItem, target int) ([]models.InteractionEvent, []models.ProviderFailure) {
	interactions := []models.InteractionEvent{}
	failures := []models.ProviderFailure{}
	for _, source := range sources {
		for _, provider := range e.registry.CampaignProviders() {
			if provider.ID() != source.Provider {
				continue
			}
			remaining := target - len(interactions)
			if remaining <= 0 {
				return dedupeInteractions(interactions), failures
			}
			events, err := provider.FetchInteractions(ctx, source, remaining)
			if err != nil {
				failures = append(failures, failure(provider.ID(), err))
				continue
			}
			interactions = append(interactions, events...)
		}
	}
	return dedupeInteractions(interactions), failures
}

func (e *Engine) classifyActors(campaign models.CampaignProfile, narrativeID string, sources []models.SourceItem, interactions []models.InteractionEvent) []models.ActorClassification {
	accounts := map[string]models.AccountProfile{}
	for _, source := range sources {
		if source.Author.AccountID != "" {
			accounts[source.Author.AccountID] = source.Author
		}
	}
	byAccount := map[string][]models.InteractionEvent{}
	for _, interaction := range interactions {
		byAccount[interaction.AccountID] = append(byAccount[interaction.AccountID], interaction)
	}
	classifications := []models.ActorClassification{}
	now := time.Now().UTC()
	for accountID, accountInteractions := range byAccount {
		account := accounts[accountID]
		if account.AccountID == "" {
			account = models.AccountProfile{AccountID: accountID, Platform: "x", Handle: accountID}
		}
		score, evidence := scoring.ActorBotScore(account, accountInteractions)
		class := models.ActorClassNonBot
		if score >= 0.65 {
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
	narrative.DecisionExplanation = decisionExplanation(*narrative)
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
			DashboardPriority:      scoring.DashboardPriority(narrative.PopularityScore, narrative.InauthenticPercentage, sourcePopularityScore, velocityScore(narrative.VelocityPerHour), narrative.RelevanceScore),
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
