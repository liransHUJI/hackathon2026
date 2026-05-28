package engine

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/models"
	"github.com/hnweb/provenance/internal/providers"
)

func providerSupports(provider providers.CampaignProvider, target string) bool {
	caps := provider.Capabilities()
	if target == "x" || target == "x_interactions" {
		return caps.SupportsXSearch
	}
	return caps.SupportsFullTextFetch
}

func failure(provider string, err error) models.ProviderFailure {
	msg := "provider unavailable"
	if err != nil {
		msg = err.Error()
	}
	return models.ProviderFailure{Provider: provider, Stage: models.StageSearch, Error: msg, At: time.Now().UTC()}
}

func dedupeSources(sources []models.SourceItem) []models.SourceItem {
	seen := map[string]bool{}
	out := []models.SourceItem{}
	for _, source := range sources {
		key := source.GlobalDedupKey
		if key == "" {
			key = source.SourceID
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, source)
	}
	return out
}

func dedupeInteractions(interactions []models.InteractionEvent) []models.InteractionEvent {
	seen := map[string]bool{}
	out := []models.InteractionEvent{}
	for _, interaction := range interactions {
		key := interaction.InteractionID
		if key == "" {
			key = fmt.Sprintf("%s:%s:%s", interaction.SourceID, interaction.AccountID, interaction.InteractionType)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, interaction)
	}
	return out
}

func dedupeStrings(values []string) []string {
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

func narrativeKey(text string) string {
	text = strings.ToLower(strings.Join(strings.Fields(text), " "))
	text = strings.Trim(text, ".,:;!?()[]{}\"'")
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}
	if len(words) > 14 {
		words = words[:14]
	}
	sum := sha256.Sum256([]byte(strings.Join(words, " ")))
	return fmt.Sprintf("%x", sum[:8])
}

func summarizeNarrative(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) > 160 {
		text = string([]rune(text)[:160])
	}
	return text
}

func sourcesForNarrative(narrative models.NarrativeCluster, sources []models.SourceItem) []models.SourceItem {
	ids := map[string]bool{}
	for _, id := range narrative.SourceIDs {
		ids[id] = true
	}
	out := []models.SourceItem{}
	for _, source := range sources {
		if ids[source.SourceID] {
			out = append(out, source)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func estimateReach(sources []models.SourceItem, interactions []models.InteractionEvent) int64 {
	reach := int64(len(interactions))
	seen := map[string]bool{}
	for _, source := range sources {
		if source.Author.AccountID != "" && !seen[source.Author.AccountID] {
			reach += source.Author.FollowersCount
			seen[source.Author.AccountID] = true
		}
	}
	return reach
}

func velocity(first, last *time.Time, interactions int) float64 {
	if interactions == 0 {
		return 0
	}
	if first == nil || last == nil || !last.After(*first) {
		return float64(interactions)
	}
	hours := last.Sub(*first).Hours()
	if hours < 1 {
		hours = 1
	}
	return float64(interactions) / hours
}

func trend(velocity float64) models.TrendDirection {
	switch {
	case velocity >= 100:
		return models.TrendRising
	case velocity <= 5:
		return models.TrendFalling
	default:
		return models.TrendFlat
	}
}

func velocityScore(velocity float64) float64 {
	switch {
	case velocity >= 1000:
		return 1
	case velocity >= 100:
		return 0.8
	case velocity >= 25:
		return 0.5
	case velocity > 0:
		return 0.2
	default:
		return 0
	}
}

func foreignInfluenceRisk(campaign *models.CampaignProfile, sources []models.SourceItem) float64 {
	if campaign.Region == "" || len(sources) == 0 {
		return 0
	}
	outside := 0
	for _, source := range sources {
		country := strings.ToLower(source.Author.InferredCountry + " " + source.Author.DeclaredLocation)
		if country != "" && !strings.Contains(country, strings.ToLower(campaign.Region)) {
			outside++
		}
	}
	return float64(outside) / float64(len(sources))
}

func aiGenerationRisk(sources []models.SourceItem) float64 {
	if len(sources) == 0 {
		return 0
	}
	flagged := 0
	for _, source := range sources {
		lower := strings.ToLower(source.Text)
		if strings.Contains(lower, "as an ai") || strings.Contains(lower, "it is important to note") || strings.Count(lower, ",") > 8 {
			flagged++
		}
	}
	return float64(flagged) / float64(len(sources))
}

func misinformationRisk(bot, ai, foreign float64) float64 {
	risk := 0.4*bot + 0.3*ai + 0.3*foreign
	if risk > 1 {
		return 1
	}
	return risk
}

func importantUserScore(sources []models.SourceItem) float64 {
	if len(sources) == 0 {
		return 0
	}
	best := 0.0
	for _, source := range sources {
		if source.Author.InfluenceScore > best {
			best = source.Author.InfluenceScore
		}
	}
	return best
}

func sourceMix(sources []models.SourceItem) map[string]int {
	out := map[string]int{}
	for _, source := range sources {
		out[string(source.SourceType)]++
	}
	return out
}

func geoDistribution(sources []models.SourceItem) map[string]float64 {
	counts := map[string]int{}
	for _, source := range sources {
		country := strings.TrimSpace(source.Author.InferredCountry)
		if country == "" {
			country = "unknown"
		}
		counts[country]++
	}
	out := map[string]float64{}
	for key, value := range counts {
		out[key] = float64(value) / float64(maxInt(1, len(sources)))
	}
	return out
}

func sentimentDistribution(sources []models.SourceItem) map[string]float64 {
	counts := map[string]int{"positive": 0, "neutral": 0, "negative": 0}
	for _, source := range sources {
		lower := strings.ToLower(source.Text)
		switch {
		case strings.Contains(lower, "fake") || strings.Contains(lower, "corrupt") || strings.Contains(lower, "scandal"):
			counts["negative"]++
		case strings.Contains(lower, "support") || strings.Contains(lower, "great") || strings.Contains(lower, "win"):
			counts["positive"]++
		default:
			counts["neutral"]++
		}
	}
	total := float64(maxInt(1, len(sources)))
	return map[string]float64{
		"positive": float64(counts["positive"]) / total,
		"neutral":  float64(counts["neutral"]) / total,
		"negative": float64(counts["negative"]) / total,
	}
}

func primarySource(campaignID, narrativeID string, sources []models.SourceItem) models.PrimarySourceAttribution {
	if len(sources) == 0 {
		return models.PrimarySourceAttribution{
			AttributionID: uuid.NewString(),
			CampaignID:    campaignID,
			NarrativeID:   narrativeID,
			SourceType:    models.SourceAuthenticityUnknown,
			Confidence:    0,
			Evidence:      []string{"no source data was available"},
			CreatedAt:     time.Now().UTC(),
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		left := sources[i].CollectedAt
		right := sources[j].CollectedAt
		if sources[i].PublishedAt != nil {
			left = *sources[i].PublishedAt
		}
		if sources[j].PublishedAt != nil {
			right = *sources[j].PublishedAt
		}
		return left.Before(right)
	})
	source := sources[0]
	sourceType := models.SourceAuthenticityHuman
	if source.Author.BotLikelihood >= 0.65 {
		sourceType = models.SourceAuthenticitySynthetic
	}
	first := source.PublishedAt
	return models.PrimarySourceAttribution{
		AttributionID: uuid.NewString(),
		CampaignID:    campaignID,
		NarrativeID:   narrativeID,
		SourceID:      source.SourceID,
		AccountID:     source.Author.AccountID,
		SourceType:    sourceType,
		Confidence:    0.75,
		Evidence:      []string{"earliest collected or published source in the narrative cluster", fmt.Sprintf("provider=%s", source.Provider)},
		FirstSeenAt:   first,
		CreatedAt:     time.Now().UTC(),
	}
}

func recommendedAction(narrative models.NarrativeCluster) string {
	switch {
	case narrative.InauthenticPercentage >= 60 && narrative.PopularityScore >= 0.5:
		return "brief leadership and prepare a corrective response"
	case narrative.InauthenticPercentage >= 40:
		return "monitor closely and validate source claims before responding"
	case narrative.PopularityScore >= 0.7:
		return "consider proactive engagement because the narrative is popular"
	default:
		return "monitor"
	}
}

func whyItMatters(narrative models.NarrativeCluster) string {
	return fmt.Sprintf("This narrative has %.0f%% authentic engagement, %.0f%% inauthentic engagement, estimated reach %d, and %.1f interactions per hour.", narrative.AuthenticPercentage, narrative.InauthenticPercentage, narrative.ReachEstimate, narrative.VelocityPerHour)
}

func decisionExplanation(narrative models.NarrativeCluster) string {
	return fmt.Sprintf("Ranked by popularity %.2f, relevance %.2f, bot/coordination risk %.2f, and source popularity.", narrative.PopularityScore, narrative.RelevanceScore, narrative.BotCoordinationRisk)
}

func confidenceFromEvidence(score float64, evidence int) float64 {
	conf := 0.55 + float64(evidence)*0.08
	if score > 0.8 || score < 0.2 {
		conf += 0.15
	}
	if conf > 1 {
		return 1
	}
	return conf
}

func alertsForNarrative(narrative models.NarrativeCluster) []models.Alert {
	if narrative.OverallRisk < 0.6 && narrative.PopularityScore < 0.75 {
		return nil
	}
	severity := strings.ToLower(narrative.RiskLabel)
	if narrative.PopularityScore >= 0.75 && narrative.OverallRisk < 0.6 {
		severity = "medium"
	}
	return []models.Alert{{
		AlertID:           uuid.NewString(),
		CampaignID:        narrative.CampaignID,
		NarrativeID:       narrative.NarrativeID,
		AlertType:         "narrative_integrity",
		Severity:          severity,
		Title:             "Narrative requires PR review",
		Summary:           narrative.Summary,
		WhyNow:            narrative.WhyItMatters,
		Evidence:          []string{narrative.DecisionExplanation},
		RecommendedAction: narrative.RecommendedPRAction,
		Confidence:        narrative.PopularityScore,
		CreatedAt:         time.Now().UTC(),
	}}
}

func narrativeStatus(narrative models.NarrativeCluster) models.EngineStatus {
	if narrative.InsufficientDataReason != nil {
		return models.EngineStatusInsufficientData
	}
	return models.EngineStatusCompleted
}

func executiveSummary(cards []models.NarrativeCard, failures []models.ProviderFailure) string {
	if len(cards) == 0 {
		if len(failures) > 0 {
			return "No narratives are available yet because live provider collection is degraded."
		}
		return "No narratives are available yet."
	}
	return fmt.Sprintf("%d narratives are available. The top narrative is %q with %.0f%% inauthentic engagement.", len(cards), cards[0].Narrative, cards[0].InauthenticPercentage)
}

func recommendedActions(cards []models.NarrativeCard) []string {
	out := []string{}
	for _, card := range cards {
		if card.RecommendedPRAction != "" {
			out = append(out, card.RecommendedPRAction+": "+card.Narrative)
		}
		if len(out) >= 5 {
			break
		}
	}
	return out
}
