package scoring

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hnweb/provenance/internal/models"
	"github.com/hnweb/provenance/internal/pipeline"
)

func SourceConfidence(timestampConfidence, completeness, providerReliability, authorReliability, corroboration float64) float64 {
	return pipeline.Clamp01(0.30*timestampConfidence + 0.20*completeness + 0.20*providerReliability + 0.15*authorReliability + 0.15*corroboration)
}

func ProvenanceScore(similarity, chronological, sourceConfidence, aiOrigin, spread float64) float64 {
	return pipeline.Clamp01(0.35*similarity + 0.25*chronological + 0.20*sourceConfidence + 0.10*aiOrigin + 0.10*spread)
}

func Risk(aiScore, provenanceConfidence, spread, reliabilityConcern, expertAdjustment float64) float64 {
	return pipeline.Clamp01(0.30*aiScore + 0.25*provenanceConfidence + 0.20*spread + 0.15*reliabilityConcern + 0.10*expertAdjustment)
}

func RiskLabel(risk float64) string {
	switch {
	case risk >= 0.8:
		return "CRITICAL"
	case risk >= 0.6:
		return "HIGH"
	case risk >= 0.35:
		return "MEDIUM"
	default:
		return "LOW"
	}
}

func LexicalSimilarity(a, b string) float64 {
	aw := wordSet(a)
	bw := wordSet(b)
	if len(aw) == 0 || len(bw) == 0 {
		return 0
	}
	intersection := 0
	for word := range aw {
		if bw[word] {
			intersection++
		}
	}
	union := len(aw) + len(bw) - intersection
	return float64(intersection) / float64(union)
}

func ChronologicalScore(ts *time.Time, earliest *time.Time) float64 {
	if ts == nil {
		return 0.25
	}
	if earliest == nil || ts.Equal(*earliest) {
		return 1
	}
	days := math.Max(ts.Sub(*earliest).Hours()/24, 0)
	return pipeline.Clamp01(1 - days/30)
}

func wordSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.Fields(strings.ToLower(value)) {
		word = strings.Trim(word, ".,:;!?()[]{}\"'")
		if len(word) > 2 {
			out[word] = true
		}
	}
	return out
}

func Relevance(campaign models.CampaignProfile, text string) float64 {
	lower := strings.ToLower(text)
	score := 0.0
	for _, value := range append([]string{campaign.ClientName}, campaign.ClientAliases...) {
		if value != "" && strings.Contains(lower, strings.ToLower(value)) {
			score += 0.30
			break
		}
	}
	for _, topic := range campaign.MonitoredTopics {
		if topic != "" && strings.Contains(lower, strings.ToLower(topic)) {
			score += 0.20
			break
		}
	}
	for _, group := range campaign.InterestGroups {
		if InterestGroupMatch(group, text) > 0 {
			score += 0.20
			break
		}
	}
	if campaign.Region != "" && strings.Contains(lower, strings.ToLower(campaign.Region)) {
		score += 0.15
	}
	for _, account := range campaign.ImportantAccounts {
		if account != "" && strings.Contains(lower, strings.ToLower(account)) {
			score += 0.10
			break
		}
	}
	return pipeline.Clamp01(score + 0.05)
}

func InterestGroupMatch(group models.InterestGroup, text string) float64 {
	lower := strings.ToLower(text)
	matches := 0
	total := 0
	for _, list := range [][]string{group.Keywords, group.Hashtags, group.Accounts, group.Regions, group.Issues} {
		for _, value := range list {
			total++
			if value != "" && strings.Contains(lower, strings.ToLower(value)) {
				matches++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return pipeline.Clamp01(float64(matches) / math.Min(float64(total), 5))
}

// ClassifyXInteraction labels how a candidate post relates to an origin post and reports
// whether the candidate is a distinct post that should count as an interaction. Types are
// reply (comment), quote, repost, subtweet (topic match without an explicit reference), or post.
func ClassifyXInteraction(origin, candidate models.SourceItem) (string, bool) {
	if sameXPost(origin, candidate) {
		return "", false
	}
	originHandle := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(origin.Author.Handle), "@"))
	originURL := derefURL(origin.URL, origin.CanonicalURL)
	text := strings.TrimSpace(strings.ToLower(candidate.Text))

	if flaggedMeta(candidate.Engagement, "is_repost") {
		return "repost", true
	}
	if flaggedMeta(candidate.Engagement, "is_quote") {
		return "quote", true
	}
	if flaggedMeta(candidate.Engagement, "is_reply") {
		return "reply", true
	}
	if originHandle != "" {
		mention := "@" + originHandle
		if strings.HasPrefix(text, "rt "+mention) {
			return "repost", true
		}
		if strings.HasPrefix(text, mention) || mentionsHandle(candidate, originHandle) && strings.Contains(text, mention) {
			return "reply", true
		}
		if strings.Contains(text, mention) {
			return "quote", true
		}
	}
	if originURL != "" {
		for _, link := range candidate.LinkedURLs {
			if strings.Contains(strings.ToLower(link), strings.ToLower(originURL)) {
				return "quote", true
			}
		}
	}
	// No explicit reference: a subtweet shares the narrative topic without naming the origin.
	if sharesTopic(origin, candidate) {
		return "subtweet", true
	}
	return "post", true
}

func sameXPost(a, b models.SourceItem) bool {
	if a.SourceID != "" && a.SourceID == b.SourceID {
		return true
	}
	au := derefURL(a.URL, a.CanonicalURL)
	bu := derefURL(b.URL, b.CanonicalURL)
	return au != "" && strings.EqualFold(au, bu)
}

func derefURL(values ...*string) string {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			return strings.TrimSpace(*value)
		}
	}
	return ""
}

func flaggedMeta(meta map[string]any, key string) bool {
	if meta == nil {
		return false
	}
	b, _ := meta[key].(bool)
	return b
}

func mentionsHandle(candidate models.SourceItem, handle string) bool {
	for _, entity := range candidate.MentionedEntities {
		if strings.EqualFold(strings.TrimPrefix(entity, "@"), handle) {
			return true
		}
	}
	return false
}

func sharesTopic(a, b models.SourceItem) bool {
	for _, ha := range a.Hashtags {
		for _, hb := range b.Hashtags {
			if strings.EqualFold(ha, hb) {
				return true
			}
		}
	}
	return LexicalSimilarity(a.Text, b.Text) >= 0.18
}

func ActorBotScore(account models.AccountProfile, interactions []models.InteractionEvent) (float64, []string) {
	score := account.BotLikelihood
	evidence := []string{}
	if account.FollowersCount < 50 && account.FollowingCount > 500 {
		score += 0.25
		evidence = append(evidence, "low-follower/high-following ratio")
	}
	if len(interactions) > 25 {
		score += 0.20
		evidence = append(evidence, "high interaction volume in one narrative")
	}
	if account.Bio == "" {
		score += 0.10
		evidence = append(evidence, "missing profile bio")
	}
	if account.CoordinationScore > 0.5 {
		score += 0.20
		evidence = append(evidence, "coordination score is elevated")
	}
	if len(evidence) == 0 {
		evidence = append(evidence, "no strong bot indicators found")
	}
	return pipeline.Clamp01(score), evidence
}

func AuthenticityPercentages(classifications []models.ActorClassification) (authentic, inauthentic, unknown float64) {
	if len(classifications) == 0 {
		return 0, 0, 100
	}
	var bots, humans, unknowns int
	for _, classification := range classifications {
		switch classification.Class {
		case models.ActorClassBot:
			bots++
		case models.ActorClassNonBot:
			humans++
		default:
			unknowns++
		}
	}
	total := float64(len(classifications))
	return float64(humans) / total * 100, float64(bots) / total * 100, float64(unknowns) / total * 100
}

func Popularity(interactions int, reach int64, velocity float64) float64 {
	interactionScore := math.Log10(float64(interactions)+1) / 5
	reachScore := math.Log10(float64(reach)+1) / 7
	velocityScore := math.Log10(velocity+1) / 3
	return pipeline.Clamp01(0.45*interactionScore + 0.35*reachScore + 0.20*velocityScore)
}

func DashboardPriority(popularity, inauthenticPct, sourcePopularity, velocity, relevance float64) float64 {
	return pipeline.Clamp01(0.35*popularity + 0.25*(inauthenticPct/100) + 0.20*sourcePopularity + 0.10*velocity + 0.10*relevance)
}

func OverallNarrativeRisk(misinfo, botCoordination, foreignInfluence, aiGeneration, velocity, importantUsers float64) float64 {
	return pipeline.Clamp01(0.25*misinfo + 0.25*botCoordination + 0.20*foreignInfluence + 0.15*aiGeneration + 0.10*velocity + 0.05*importantUsers)
}

func SourcePopularity(sources []models.SourceItem, interactions []models.InteractionEvent) []models.SourcePopularity {
	counts := map[string]int{}
	for _, interaction := range interactions {
		counts[interaction.SourceID]++
	}
	out := make([]models.SourcePopularity, 0, len(sources))
	for _, source := range sources {
		reach := source.Author.FollowersCount + int64(counts[source.SourceID])
		popularity := Popularity(counts[source.SourceID], reach, float64(counts[source.SourceID]))
		authenticity := models.SourceAuthenticityHuman
		if source.Author.BotLikelihood >= 0.65 {
			authenticity = models.SourceAuthenticitySynthetic
		}
		out = append(out, models.SourcePopularity{
			SourceID:          source.SourceID,
			AccountID:         source.Author.AccountID,
			Handle:            source.Author.Handle,
			DisplayName:       source.Author.DisplayName,
			SourceType:        source.SourceType,
			PopularityScore:   popularity,
			ReachEstimate:     reach,
			InteractionCount:  counts[source.SourceID],
			AmplificationRole: amplificationRole(counts[source.SourceID]),
			ReliabilityScore:  source.Author.ReliabilityScore,
			Authenticity:      authenticity,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PopularityScore > out[j].PopularityScore })
	if len(out) > 10 {
		return out[:10]
	}
	return out
}

func CapitalLossEstimate(narrative models.NarrativeCluster) models.CapitalLossEstimate {
	negativeShare := narrative.SentimentDistribution["negative"]
	applies := negativeShare >= 0.35 || narrative.OverallRisk >= 0.35 || narrative.InauthenticPercentage >= 25
	if !applies {
		return models.CapitalLossEstimate{
			Applies:     false,
			Confidence:  0.2,
			Source:      "llm_or_heuristic_estimation",
			Explanation: "Trend is not currently negative or risky enough to estimate meaningful capital loss.",
			Disclaimer:  "This is not a financial model and should not be treated as accurate.",
		}
	}
	base := float64(narrative.ReachEstimate) * (0.02 + 0.08*narrative.PopularityScore)
	riskMultiplier := 1 + narrative.OverallRisk + (narrative.InauthenticPercentage / 100)
	negativeMultiplier := 1 + negativeShare
	expected := int64(math.Round(base * riskMultiplier * negativeMultiplier))
	if expected < 1000 {
		expected = 1000
	}
	minimum := int64(float64(expected) * 0.35)
	maximum := int64(float64(expected) * 2.75)
	return models.CapitalLossEstimate{
		Applies:     true,
		MinUSD:      roundToNearest(minimum, 1000),
		MaxUSD:      roundToNearest(maximum, 1000),
		ExpectedUSD: roundToNearest(expected, 1000),
		Confidence:  0.35,
		Source:      "trust_me_bro_llm_style_estimation",
		Explanation: "Rough PR impact estimate based on reach, popularity, negative sentiment share, overall risk, and inauthentic engagement percentage.",
		Disclaimer:  "Directional estimate only. It is intentionally not accurate and is not financial advice.",
	}
}

func roundToNearest(value int64, nearest int64) int64 {
	if nearest <= 0 {
		return value
	}
	remainder := value % nearest
	if remainder >= nearest/2 {
		return value + nearest - remainder
	}
	return value - remainder
}

func amplificationRole(count int) string {
	switch {
	case count > 1000:
		return "major_amplifier"
	case count > 100:
		return "amplifier"
	case count > 0:
		return "participant"
	default:
		return "origin_or_mention"
	}
}
