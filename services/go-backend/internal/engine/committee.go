package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/hnweb/provenance/internal/llm/gemini"
	"github.com/hnweb/provenance/internal/models"
)

// computeCoordination scores accounts for coordinated inauthentic behavior within a single
// narrative. The core signal is near-duplicate messaging: when 3+ distinct accounts post the same
// normalized text, that text is treated as a coordinated template and every account using it is
// flagged. A lighter signal is applied when only 2 accounts share a template.
func computeCoordination(interactions []models.InteractionEvent) map[string]float64 {
	textAccounts := map[string]map[string]bool{}
	accountTexts := map[string][]string{}
	for _, it := range interactions {
		text := ""
		if it.Metadata != nil {
			if s, ok := it.Metadata["text"].(string); ok {
				text = s
			}
		}
		norm := normalizeForCoord(text)
		if norm == "" {
			continue
		}
		if textAccounts[norm] == nil {
			textAccounts[norm] = map[string]bool{}
		}
		textAccounts[norm][it.AccountID] = true
		accountTexts[it.AccountID] = append(accountTexts[it.AccountID], norm)
	}
	scores := map[string]float64{}
	for acc, norms := range accountTexts {
		best := 0.0
		for _, n := range norms {
			distinct := len(textAccounts[n])
			switch {
			case distinct >= 3:
				if best < 0.85 {
					best = 0.85
				}
			case distinct == 2:
				if best < 0.45 {
					best = 0.45
				}
			}
		}
		if best > 0 {
			scores[acc] = best
		}
	}
	return scores
}

// normalizeForCoord reduces a post to a comparable template: lowercased, with URLs and @mentions
// removed (these vary per post) and punctuation/emoji stripped, so copy-paste amplification with
// minor cosmetic differences still collides.
func normalizeForCoord(s string) string {
	fields := strings.Fields(strings.ToLower(s))
	keep := make([]string, 0, len(fields))
	for _, f := range fields {
		if strings.HasPrefix(f, "http") || strings.HasPrefix(f, "@") {
			continue
		}
		f = strings.TrimFunc(f, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '#'
		})
		if f != "" {
			keep = append(keep, f)
		}
	}
	out := strings.Join(keep, " ")
	if len(out) > 220 {
		out = out[:220]
	}
	return out
}

// clientAccountFilter recognizes posts authored by the client or their official affiliates so the
// engine can exclude the client's own voice from narrative discovery.
type clientAccountFilter struct {
	handles map[string]bool
	names   []string
}

func newClientAccountFilter(campaign models.CampaignProfile) clientAccountFilter {
	f := clientAccountFilter{handles: map[string]bool{}}
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(v, "@")))
		if v != "" {
			f.handles[v] = true
		}
	}
	for _, a := range campaign.ClientAccounts {
		add(a)
	}
	// The client's own primary handle(s) often appear in aliases (e.g. "realDonaldTrump").
	for _, a := range campaign.ClientAliases {
		if strings.HasPrefix(strings.TrimSpace(a), "@") || !strings.Contains(strings.TrimSpace(a), " ") {
			add(a)
		}
	}
	f.names = append(f.names, strings.ToLower(strings.TrimSpace(campaign.ClientName)))
	return f
}

func (f clientAccountFilter) authoredByClient(source models.SourceItem) bool {
	handle := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(source.Author.Handle, "@")))
	if handle != "" && f.handles[handle] {
		return true
	}
	acct := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(source.Author.AccountID, "@")))
	if acct != "" && f.handles[acct] {
		return true
	}
	return false
}

// assessNarratives runs the LLM committee over all candidate narratives in a single batched call.
// On any failure (no key, rate limit, parse error) it returns relevance-preserving heuristic
// verdicts so a degraded LLM never silently hides real narratives from the manager.
func (e *Engine) assessNarratives(ctx context.Context, campaign models.CampaignProfile, narratives []models.NarrativeCluster, sources []models.SourceItem) map[string]models.CommitteeVerdict {
	out := make(map[string]models.CommitteeVerdict, len(narratives))
	if e.gemini != nil && e.gemini.Configured() && len(narratives) > 0 {
		input := gemini.CommitteeInput{
			ClientName:     campaign.ClientName,
			ClientAliases:  campaign.ClientAliases,
			ClientAccounts: campaign.ClientAccounts,
			Industry:       campaign.Industry,
			Region:         campaign.Region,
		}
		for _, n := range narratives {
			nsrc := sourcesForNarrative(n, sources)
			input.Candidates = append(input.Candidates, gemini.CandidateNarrative{
				ID:            n.NarrativeID,
				Title:         n.Narrative,
				Summary:       n.Summary,
				SamplePosts:   samplePosts(nsrc, 3),
				Hashtags:      gatherHashtags(nsrc, 6),
				TopHandles:    topHandles(nsrc, 5),
				SourceCount:   len(nsrc),
				ReachEstimate: estimateReach(nsrc, nil),
			})
		}
		if verdicts, err := e.gemini.CommitteeAssess(ctx, input); err == nil {
			for id, v := range verdicts {
				out[id] = v
			}
		}
	}
	for _, n := range narratives {
		if _, ok := out[n.NarrativeID]; !ok {
			out[n.NarrativeID] = heuristicVerdict(n)
		}
	}
	return out
}

// heuristicVerdict keeps a narrative visible when the LLM is unavailable. Capital loss is left to
// the deterministic scorer in completeNarrative (verdict.Source != "gemini").
func heuristicVerdict(n models.NarrativeCluster) models.CommitteeVerdict {
	return models.CommitteeVerdict{
		Relevant:         true,
		RelevanceScore:   n.RelevanceScore,
		InterestScore:    n.RelevanceScore,
		ImpactSummary:    "",
		ClientOriginated: false,
		ConsensusLabel:   "heuristic_review",
		Experts: []models.ExpertAssessment{{
			Expert:     "Heuristic Committee",
			Opinion:    "LLM committee unavailable; narrative retained and scored by deterministic signals.",
			Severity:   n.RelevanceScore,
			Confidence: 0.4,
		}},
		Source: "heuristic",
	}
}

// enrichHeuristicVerdict upgrades a fallback (non-LLM) verdict using the narrative's computed
// metrics so the dashboard still shows varied relevance, expert opinions and a capital-loss range
// when the live committee is unavailable. The live Gemini verdict, when present, takes precedence.
func enrichHeuristicVerdict(n *models.NarrativeCluster) {
	v := n.CommitteeVerdict
	if v == nil || v.Source == "gemini" {
		return
	}
	negative := n.SentimentDistribution["negative"]
	rel := 0.35*n.RelevanceScore + 0.25*n.PopularityScore + 0.25*n.OverallRisk + 0.15*(n.InauthenticPercentage/100)
	if rel > 1 {
		rel = 1
	}
	v.RelevanceScore = rel
	v.InterestScore = rel

	reach := float64(n.ReachEstimate)
	if reach < 1000 {
		reach = 1000
	}
	base := reach * (0.012 + 0.05*n.PopularityScore)
	mult := 1 + n.OverallRisk + n.AIGenerationRisk + n.BotCoordinationRisk + negative
	expected := int64(base * mult)
	if expected < 25000 {
		expected = 25000
	}
	v.CapitalLoss = models.CapitalLossEstimate{
		Applies:     true,
		MinUSD:      roundUSD(int64(float64(expected) * 0.4)),
		ExpectedUSD: roundUSD(expected),
		MaxUSD:      roundUSD(int64(float64(expected) * 2.6)),
		Confidence:  0.3,
		Source:      "heuristic-committee",
		Explanation: fmt.Sprintf("Directional estimate from estimated reach (%d), %.0f%% bot/AI-driven amplification, and %s overall risk.", n.ReachEstimate, n.InauthenticPercentage, strings.ToLower(firstNonEmpty(n.RiskLabel, "moderate"))),
		Disclaimer:  "Heuristic estimate (live LLM committee unavailable); not financial advice.",
	}

	v.ConsensusLabel = strings.ToLower(firstNonEmpty(n.RiskLabel, "moderate")) + "-risk narrative"
	v.AudienceEffect = fmt.Sprintf("Reaches an estimated %d accounts; ~%.0f%% of engagement is bot/AI-driven amplification.", n.ReachEstimate, n.InauthenticPercentage)
	v.ImpactSummary = whyItMatters(*n)
	v.RecommendedAction = recommendedAction(*n)
	v.Experts = []models.ExpertAssessment{
		{Expert: "Disinformation Analyst", Severity: clampUnit(n.BotCoordinationRisk), Confidence: 0.5,
			Opinion: fmt.Sprintf("%.0f%% of engagement traces to bot/AI-driven accounts; coordination risk is %s.", n.InauthenticPercentage, band(n.BotCoordinationRisk))},
		{Expert: "Reputation & PR Strategist", Severity: clampUnit(n.OverallRisk), Confidence: 0.5,
			Opinion: fmt.Sprintf("Overall reputational risk is %s; %s", band(n.OverallRisk), recommendedAction(*n))},
		{Expert: "Political Media Analyst", Severity: clampUnit(n.PopularityScore), Confidence: 0.5,
			Opinion: fmt.Sprintf("Estimated reach of %d with a %s trend suggests %s pickup potential.", n.ReachEstimate, n.Trend, band(n.PopularityScore))},
		{Expert: "Financial Impact Economist", Severity: clampUnit(n.OverallRisk*0.6 + n.PopularityScore*0.4), Confidence: 0.3,
			Opinion: fmt.Sprintf("If unchecked, exposure to donor/sponsor/electoral capital is in the ~$%dK range.", roundUSD(expected)/1000)},
		{Expert: "Skeptic", Severity: clampUnit(1 - n.OverallRisk), Confidence: 0.4,
			Opinion: "Signal is partly heuristic; confirm with a quick manual scan before escalating."},
	}
}

func band(v float64) string {
	switch {
	case v >= 0.66:
		return "high"
	case v >= 0.33:
		return "moderate"
	default:
		return "low"
	}
}

func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func roundUSD(value int64) int64 {
	const nearest = 1000
	if value <= 0 {
		return 0
	}
	r := value % nearest
	if r >= nearest/2 {
		return value + nearest - r
	}
	return value - r
}

func impactSummary(n models.NarrativeCluster) string {
	if n.CommitteeVerdict != nil && strings.TrimSpace(n.CommitteeVerdict.ImpactSummary) != "" {
		return n.CommitteeVerdict.ImpactSummary
	}
	return n.WhyItMatters
}

func samplePosts(sources []models.SourceItem, limit int) []string {
	out := make([]string, 0, limit)
	for _, s := range sources {
		text := firstNonEmpty(s.Text, s.Title, s.Snippet)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, truncateText(text, 240))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func topHandles(sources []models.SourceItem, limit int) []string {
	type h struct {
		handle    string
		followers int64
	}
	seen := map[string]bool{}
	items := make([]h, 0, len(sources))
	for _, s := range sources {
		handle := strings.TrimSpace(s.Author.Handle)
		if handle == "" || seen[handle] {
			continue
		}
		seen[handle] = true
		items = append(items, h{handle: handle, followers: s.Author.FollowersCount})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].followers > items[j].followers })
	out := make([]string, 0, limit)
	for _, it := range items {
		out = append(out, it.handle)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func gatherHashtags(sources []models.SourceItem, limit int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range sources {
		for _, tag := range s.Hashtags {
			key := strings.ToLower(strings.TrimSpace(tag))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, tag)
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func interactionBreakdown(interactions []models.InteractionEvent) map[string]int {
	if len(interactions) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, it := range interactions {
		t := strings.TrimSpace(it.InteractionType)
		if t == "" {
			t = "post"
		}
		out[t]++
	}
	return out
}

// spreadTimeline buckets narrative activity over time, splitting each event into organic
// (authentic), bot/AI (inauthentic) or unknown using the per-account classifications.
func spreadTimeline(sources []models.SourceItem, interactions []models.InteractionEvent, classifications []models.ActorClassification) []models.TimelineBucket {
	botByAccount := map[string]bool{}
	classified := map[string]bool{}
	for _, c := range classifications {
		classified[c.AccountID] = true
		if c.Class == models.ActorClassBot {
			botByAccount[c.AccountID] = true
		}
	}

	type ev struct {
		t       time.Time
		account string
		reach   int64
	}
	events := make([]ev, 0, len(sources)+len(interactions))
	for _, s := range sources {
		if s.SourceType != models.SourceTypeXPost {
			continue
		}
		events = append(events, ev{t: interactionTime(s), account: s.Author.AccountID, reach: s.Author.FollowersCount})
	}
	for _, it := range interactions {
		reach := int64(0)
		if it.Metadata != nil {
			if profile, ok := it.Metadata["author"].(models.AccountProfile); ok {
				reach = profile.FollowersCount
			}
		}
		t := it.OccurredAt
		if t.IsZero() {
			t = time.Now().UTC()
		}
		events = append(events, ev{t: t, account: it.AccountID, reach: reach})
	}
	if len(events) == 0 {
		return nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].t.Before(events[j].t) })

	minT := events[0].t
	maxT := events[len(events)-1].t
	span := maxT.Sub(minT)
	const bucketCount = 16
	bucketDur := span / bucketCount
	if bucketDur <= 0 {
		bucketDur = time.Minute
	}

	buckets := make([]models.TimelineBucket, 0, bucketCount)
	index := map[int]int{}
	for _, e := range events {
		bi := int(e.t.Sub(minT) / bucketDur)
		if bi >= bucketCount {
			bi = bucketCount - 1
		}
		if bi < 0 {
			bi = 0
		}
		pos, ok := index[bi]
		if !ok {
			buckets = append(buckets, models.TimelineBucket{T: minT.Add(time.Duration(bi) * bucketDur).UTC()})
			pos = len(buckets) - 1
			index[bi] = pos
		}
		buckets[pos].Total++
		buckets[pos].Reach += e.reach
		switch {
		case botByAccount[e.account]:
			buckets[pos].Inauthentic++
		case classified[e.account]:
			buckets[pos].Authentic++
		default:
			buckets[pos].Unknown++
		}
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].T.Before(buckets[j].T) })
	return buckets
}
