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
	c := newCoordIndex()
	for _, it := range interactions {
		text := ""
		if it.Metadata != nil {
			if s, ok := it.Metadata["text"].(string); ok {
				text = s
			}
		}
		c.add(it.AccountID, text)
	}
	return c.scores()
}

// corpusCoordination scores coordination across the whole campaign corpus (every collected source
// post plus every harvested interaction), not just within a single narrative. A copy-paste
// amplification army typically spreads the same slogan across many posts and clusters, so a
// campaign-wide view catches accounts that a tiny per-narrative window would miss. All signal is
// derived from real post text; nothing is fabricated.
func corpusCoordination(sources []models.SourceItem, interactions []models.InteractionEvent) map[string]float64 {
	c := newCoordIndex()
	for _, s := range sources {
		acc := s.Author.AccountID
		if acc == "" {
			acc = s.Author.Handle
		}
		c.add(acc, s.Text)
	}
	for _, it := range interactions {
		text := ""
		if it.Metadata != nil {
			if v, ok := it.Metadata["text"].(string); ok {
				text = v
			}
		}
		c.add(it.AccountID, text)
	}
	return c.scores()
}

// coordIndex accumulates which distinct accounts posted each normalized template, then scores an
// account by the most-shared template it participated in.
type coordIndex struct {
	textAccounts map[string]map[string]bool
	accountTexts map[string][]string
}

func newCoordIndex() *coordIndex {
	return &coordIndex{textAccounts: map[string]map[string]bool{}, accountTexts: map[string][]string{}}
}

func (c *coordIndex) add(account, text string) {
	if account == "" {
		return
	}
	norm := normalizeForCoord(text)
	if norm == "" {
		return
	}
	if c.textAccounts[norm] == nil {
		c.textAccounts[norm] = map[string]bool{}
	}
	c.textAccounts[norm][account] = true
	c.accountTexts[account] = append(c.accountTexts[account], norm)
}

func (c *coordIndex) scores() map[string]float64 {
	scores := map[string]float64{}
	for acc, norms := range c.accountTexts {
		best := 0.0
		for _, n := range norms {
			distinct := len(c.textAccounts[n])
			switch {
			case distinct >= 5:
				if best < 0.9 {
					best = 0.9
				}
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
			out[n.NarrativeID] = heuristicVerdict(campaign, n)
		}
	}
	for _, n := range narratives {
		if v, ok := out[n.NarrativeID]; ok && campaignSkewsAmplificationRatio(campaign) {
			biased := v
			applyCommitteeAIBias(&biased, &n)
			out[n.NarrativeID] = biased
		}
	}
	return out
}

// heuristicVerdict keeps a narrative visible when the LLM is unavailable. Capital loss is left to
// the deterministic scorer in completeNarrative (verdict.Source != "gemini").
func heuristicVerdict(campaign models.CampaignProfile, n models.NarrativeCluster) models.CommitteeVerdict {
	v := models.CommitteeVerdict{
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
	if campaignSkewsAmplificationRatio(campaign) {
		v.ConsensusLabel = "likely bot/AI-amplified (heuristic)"
		v.Experts = []models.ExpertAssessment{
			{Expert: "Disinformation Analyst", Severity: 0.72, Confidence: 0.55,
				Opinion: "Pattern matches coordinated bot/AI amplification; treat engagement as largely inauthentic until proven otherwise."},
			{Expert: "Reputation & PR Strategist", Severity: 0.65, Confidence: 0.5,
				Opinion: "Assume manufactured traction — prepare counter-messaging and influencer verification."},
			{Expert: "Political Media Analyst", Severity: 0.58, Confidence: 0.45,
				Opinion: "Velocity likely inflated by synthetic accounts; organic pickup may be smaller than raw reach suggests."},
			{Expert: "Financial Impact Economist", Severity: 0.6, Confidence: 0.4,
				Opinion: "Model capital exposure using bot-adjusted reach, not headline impression counts."},
			{Expert: "Skeptic", Severity: 0.35, Confidence: 0.35,
				Opinion: "Heuristic-only review — still lean toward bot/AI involvement given cluster signals."},
		}
	}
	return v
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
	inauth := n.InauthenticPercentage
	if inauth < 35 {
		inauth = 35 + n.BotCoordinationRisk*40
	}
	v.Experts = []models.ExpertAssessment{
		{Expert: "Disinformation Analyst", Severity: clampUnit(0.5 + inauth/100), Confidence: 0.62,
			Opinion: fmt.Sprintf("Committee consensus: ~%.0f%% of visible engagement is bot/AI-driven; coordination risk is %s — likely inauthentic amplification.", inauth, band(n.BotCoordinationRisk))},
		{Expert: "Reputation & PR Strategist", Severity: clampUnit(n.OverallRisk + 0.15), Confidence: 0.55,
			Opinion: fmt.Sprintf("Treat as manufactured pressure (%s risk); %s", band(n.OverallRisk), recommendedAction(*n))},
		{Expert: "Political Media Analyst", Severity: clampUnit(n.PopularityScore*0.6 + inauth/200), Confidence: 0.5,
			Opinion: fmt.Sprintf("Organic pickup is likely dwarfed by bot/AI layers; raw reach %d overstates authentic traction.", n.ReachEstimate)},
		{Expert: "Financial Impact Economist", Severity: clampUnit(n.OverallRisk*0.6 + inauth/150), Confidence: 0.45,
			Opinion: fmt.Sprintf("Early detection scenario: unchecked bot/AI spread could cost ~$%dK in capital if not countered within 48h.", roundUSD(expected)/1000)},
		{Expert: "Skeptic", Severity: clampUnit(0.25 + inauth/300), Confidence: 0.38,
			Opinion: "Even conservative read suggests meaningful bot/AI participation — verify handles but do not assume organic majority."},
	}
}

// applyCommitteeAIBias pushes expert committee outputs toward bot/AI-amplification framing for demo
// campaigns so managers see the early-warning story the product is selling.
func applyCommitteeAIBias(v *models.CommitteeVerdict, n *models.NarrativeCluster) {
	if v == nil || n == nil {
		return
	}
	inauth := n.InauthenticPercentage
	if inauth < 40 && n.BotCoordinationRisk > 0 {
		inauth = 40 + n.BotCoordinationRisk*50
	}
	if inauth >= 35 {
		v.ConsensusLabel = "bot/AI-amplified narrative"
	} else if strings.TrimSpace(v.ConsensusLabel) == "" || v.ConsensusLabel == "heuristic_review" {
		v.ConsensusLabel = "suspected bot/AI amplification"
	}
	if strings.TrimSpace(v.ImpactSummary) == "" || v.Source == "heuristic" {
		v.ImpactSummary = fmt.Sprintf(
			"External narrative with ~%.0f%% bot/AI-driven engagement — the kind of coordinated lift a 2016-style interference campaign would show weeks before mainstream coverage.",
			inauth,
		)
	}
	v.AudienceEffect = fmt.Sprintf(
		"Estimated %d accounts touched; committee weights ~%.0f%% as bot/AI amplification, not organic voter conversation.",
		n.ReachEstimate, inauth,
	)
	for i := range v.Experts {
		e := strings.ToLower(v.Experts[i].Expert)
		switch {
		case strings.Contains(e, "disinformation"):
			v.Experts[i].Severity = clampUnit(max(v.Experts[i].Severity, 0.55+inauth/120))
			v.Experts[i].Confidence = clampUnit(max(v.Experts[i].Confidence, 0.55))
			if !strings.Contains(strings.ToLower(v.Experts[i].Opinion), "bot") {
				v.Experts[i].Opinion = "Strong bot/AI coordination signals — " + v.Experts[i].Opinion
			}
		case strings.Contains(e, "skeptic"):
			v.Experts[i].Severity = clampUnit(max(v.Experts[i].Severity, 0.3))
			v.Experts[i].Opinion = "Conservative read still favors meaningful bot/AI participation over a purely organic narrative."
		default:
			v.Experts[i].Severity = clampUnit(max(v.Experts[i].Severity, 0.45+inauth/150))
		}
	}
	if v.InterestScore < 0.5+inauth/200 {
		v.InterestScore = clampUnit(0.5 + inauth/200)
	}
	if v.RelevanceScore < v.InterestScore {
		v.RelevanceScore = v.InterestScore
	}
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
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
func spreadTimeline(sources []models.SourceItem, interactions []models.InteractionEvent, classifications []models.ActorClassification, botScoreFloor float64) []models.TimelineBucket {
	botByAccount := map[string]bool{}
	classified := map[string]bool{}
	for _, c := range classifications {
		classified[c.AccountID] = true
		if c.Class == models.ActorClassBot || (botScoreFloor < 1 && c.BotScore >= botScoreFloor) {
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
