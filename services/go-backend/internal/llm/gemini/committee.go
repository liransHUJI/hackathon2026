package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hnweb/provenance/internal/models"
)

// CandidateNarrative is the minimal description of a clustered narrative handed to the committee.
type CandidateNarrative struct {
	ID             string
	Title          string
	Summary        string
	SamplePosts    []string
	Hashtags       []string
	TopHandles     []string
	SourceCount    int
	ReachEstimate  int64
	InauthenticPct float64
}

// CommitteeInput bundles the campaign context with the candidate narratives to assess.
type CommitteeInput struct {
	ClientName     string
	ClientAliases  []string
	ClientAccounts []string
	Industry       string
	Region         string
	Candidates     []CandidateNarrative
}

const committeeSystem = `You are a committee of five senior analysts advising a political campaign's PR/communications manager. The committee members are:
- Disinformation Analyst (coordinated/inauthentic amplification, bot/AI signals)
- Reputation & PR Strategist (reputational damage, message control, what action to take)
- Political Media Analyst (reach, persuadable audiences, news pickup potential)
- Financial Impact Economist (donations, fundraising, sponsor/market/electoral capital at risk in USD)
- Skeptic (guards against overreaction, flags low-signal or already-handled chatter)

You judge narratives spreading on X (Twitter) ABOUT the client. Be decisive and concrete. Only mark a narrative relevant if it is an EXTERNAL narrative the campaign manager could and should act on. Mark client_originated=true when the narrative is primarily the client's OWN messaging or pushed by their official campaign/affiliates (these must be filtered out as not useful to the manager). Respond with STRICT JSON only.`

// CommitteeAssess runs a single batched LLM call evaluating all candidate narratives. It returns a
// map keyed by narrative ID. On any error (including missing key / rate limits) it returns
// ErrNotConfigured-style errors so the engine can fall back to deterministic heuristics.
func (c *Client) CommitteeAssess(ctx context.Context, in CommitteeInput) (map[string]models.CommitteeVerdict, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	if len(in.Candidates) == 0 {
		return map[string]models.CommitteeVerdict{}, nil
	}
	prompt := buildCommitteePrompt(in)
	raw, err := c.generateJSON(ctx, c.primaryModel(), committeeSystem, prompt)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Verdicts []struct {
			ID                string  `json:"id"`
			Relevant          bool    `json:"relevant"`
			RelevanceScore    float64 `json:"relevance_score"`
			InterestScore     float64 `json:"interest_score"`
			ImpactSummary     string  `json:"impact_summary"`
			AudienceEffect    string  `json:"audience_effect"`
			ClientOriginated  bool    `json:"client_originated"`
			OriginRationale   string  `json:"origin_rationale"`
			ConsensusLabel    string  `json:"consensus_label"`
			RecommendedAction string  `json:"recommended_action"`
			Experts           []struct {
				Expert     string  `json:"expert"`
				Opinion    string  `json:"opinion"`
				Severity   float64 `json:"severity"`
				Confidence float64 `json:"confidence"`
			} `json:"experts"`
			CapitalLoss struct {
				Applies     bool    `json:"applies"`
				MinUSD      int64   `json:"min_usd"`
				ExpectedUSD int64   `json:"expected_usd"`
				MaxUSD      int64   `json:"max_usd"`
				Confidence  float64 `json:"confidence"`
				Explanation string  `json:"explanation"`
			} `json:"capital_loss"`
		} `json:"verdicts"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &parsed); err != nil {
		return nil, fmt.Errorf("parse committee response: %w", err)
	}
	out := make(map[string]models.CommitteeVerdict, len(parsed.Verdicts))
	for _, v := range parsed.Verdicts {
		if strings.TrimSpace(v.ID) == "" {
			continue
		}
		experts := make([]models.ExpertAssessment, 0, len(v.Experts))
		for _, e := range v.Experts {
			experts = append(experts, models.ExpertAssessment{
				Expert:     e.Expert,
				Opinion:    e.Opinion,
				Severity:   clamp01(e.Severity),
				Confidence: clamp01(e.Confidence),
			})
		}
		out[v.ID] = models.CommitteeVerdict{
			Relevant:          v.Relevant,
			RelevanceScore:    clamp01(v.RelevanceScore),
			InterestScore:     clamp01(v.InterestScore),
			ImpactSummary:     strings.TrimSpace(v.ImpactSummary),
			AudienceEffect:    strings.TrimSpace(v.AudienceEffect),
			ClientOriginated:  v.ClientOriginated,
			OriginRationale:   strings.TrimSpace(v.OriginRationale),
			ConsensusLabel:    strings.TrimSpace(v.ConsensusLabel),
			RecommendedAction: strings.TrimSpace(v.RecommendedAction),
			Experts:           experts,
			CapitalLoss: models.CapitalLossEstimate{
				Applies:     v.CapitalLoss.Applies,
				MinUSD:      v.CapitalLoss.MinUSD,
				ExpectedUSD: v.CapitalLoss.ExpectedUSD,
				MaxUSD:      v.CapitalLoss.MaxUSD,
				Confidence:  clamp01(v.CapitalLoss.Confidence),
				Explanation: strings.TrimSpace(v.CapitalLoss.Explanation),
				Source:      "gemini-committee",
				Disclaimer:  "Directional estimate from an AI expert committee; not financial advice.",
			},
			Source: "gemini",
		}
	}
	return out, nil
}

func buildCommitteePrompt(in CommitteeInput) string {
	var b strings.Builder
	b.WriteString("CLIENT: ")
	b.WriteString(in.ClientName)
	b.WriteString("\n")
	if len(in.ClientAliases) > 0 {
		b.WriteString("CLIENT ALIASES: " + strings.Join(in.ClientAliases, ", ") + "\n")
	}
	if len(in.ClientAccounts) > 0 {
		b.WriteString("CLIENT/AFFILIATE ACCOUNTS (treat narratives primarily pushed by these as client_originated): " + strings.Join(in.ClientAccounts, ", ") + "\n")
	}
	if in.Industry != "" {
		b.WriteString("CONTEXT: " + in.Industry + "\n")
	}
	b.WriteString("\nAssess each candidate narrative below. Return STRICT JSON of the form:\n")
	b.WriteString(`{"verdicts":[{"id":"<id>","relevant":true,"relevance_score":0.0,"interest_score":0.0,"impact_summary":"","audience_effect":"","client_originated":false,"origin_rationale":"","consensus_label":"","recommended_action":"","experts":[{"expert":"Disinformation Analyst","opinion":"","severity":0.0,"confidence":0.0}],"capital_loss":{"applies":true,"min_usd":0,"expected_usd":0,"max_usd":0,"confidence":0.0,"explanation":""}}]}`)
	b.WriteString("\nInclude one experts entry per committee member (5 total). capital_loss is the campaign capital at risk (lost donations, fundraising, sponsor/market or electoral capital) if the narrative spreads unchecked; use 0 and applies=false if not financially material.\n\nCANDIDATE NARRATIVES:\n")
	for i, cand := range in.Candidates {
		fmt.Fprintf(&b, "\n[%d] id=%s\nTitle: %s\nSummary: %s\n", i+1, cand.ID, oneLine(cand.Title), oneLine(cand.Summary))
		if len(cand.Hashtags) > 0 {
			b.WriteString("Hashtags: " + strings.Join(cand.Hashtags, ", ") + "\n")
		}
		if len(cand.TopHandles) > 0 {
			b.WriteString("Top accounts: " + strings.Join(cand.TopHandles, ", ") + "\n")
		}
		fmt.Fprintf(&b, "Signals: %d source posts, est reach %d, inauthentic ~%.0f%%\n", cand.SourceCount, cand.ReachEstimate, cand.InauthenticPct)
		if len(cand.SamplePosts) > 0 {
			b.WriteString("Sample posts:\n")
			for _, p := range cand.SamplePosts {
				b.WriteString("  - " + oneLine(p) + "\n")
			}
		}
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 280 {
		s = string([]rune(s)[:280])
	}
	return s
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
