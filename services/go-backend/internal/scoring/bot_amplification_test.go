package scoring

import (
	"testing"

	"github.com/hnweb/provenance/internal/models"
)

// post builds a single authored "post" pseudo-interaction carrying text, mirroring how the engine
// surfaces a source author's own post to the scorer.
func post(text string) []models.InteractionEvent {
	return []models.InteractionEvent{{
		InteractionType: "post",
		Metadata:        map[string]any{"text": text},
	}}
}

// TestActorBotScoreFlagsLowReachAmplifiers uses accounts drawn from the real #IStandWithRussia
// surge to confirm the scorer flags low-reach hashtag-stuffing amplifiers while leaving genuine
// high-reach commentators classified as authentic. The signal is derived entirely from observed
// reach, handle shape, bio presence and post content — nothing is fabricated.
func TestActorBotScoreFlagsLowReachAmplifiers(t *testing.T) {
	const botThreshold = 0.5 // bot-amplification campaign threshold

	cases := []struct {
		name    string
		account models.AccountProfile
		texts   []models.InteractionEvent
		wantBot bool
	}{
		{
			name:    "near-zero follower hashtag stuffer",
			account: models.AccountProfile{Handle: "Nehalsingh96", FollowersCount: 2},
			texts:   post("#IStandWithPutin Russia always support India #iSupportRussia"),
			wantBot: true,
		},
		{
			name:    "low follower pure hashtag amplifier",
			account: models.AccountProfile{Handle: "IamTshireletso", FollowersCount: 307},
			texts:   post("#istandwithrussia"),
			wantBot: true,
		},
		{
			name:    "digit-suffix handle low reach",
			account: models.AccountProfile{Handle: "FaizaRa62995937", FollowersCount: 346},
			texts:   post("#IStandWithPutin #IStandWithRussia victory"),
			wantBot: true,
		},
		{
			name:    "high-reach single hashtag (not an amplifier)",
			account: models.AccountProfile{Handle: "RobertAlai", FollowersCount: 2201871, Bio: "Kenyan blogger and activist"},
			texts:   post("#iSupportRussia"),
			wantBot: false,
		},
		{
			name:    "high-reach organic commentary",
			account: models.AccountProfile{Handle: "SaketGokhale", FollowersCount: 285485, Bio: "Politician"},
			texts:   post("The hashtag #IStandWithPutin is trending in India. Many of the accounts tweeting are bots and BJP supporters."),
			wantBot: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, evidence := ActorBotScore(tc.account, tc.texts)
			gotBot := score >= botThreshold
			if gotBot != tc.wantBot {
				t.Fatalf("%s: score=%.2f gotBot=%v wantBot=%v evidence=%v", tc.name, score, gotBot, tc.wantBot, evidence)
			}
		})
	}
}

func TestFilterClassificationsDropsMegaOrganic(t *testing.T) {
	accounts := map[string]models.AccountProfile{
		"mega": {AccountID: "mega", Handle: "@CNN", FollowersCount: 15_000_000, Verified: true, Bio: "Breaking news from around the world"},
		"bot1": {AccountID: "bot1", Handle: "@user48291", FollowersCount: 12},
		"bot2": {AccountID: "bot2", Handle: "@amp12345", FollowersCount: 89},
	}
	classifications := []models.ActorClassification{
		{AccountID: "mega", Class: models.ActorClassNonBot, BotScore: 0.05},
		{AccountID: "bot1", Class: models.ActorClassBot, BotScore: 0.72},
		{AccountID: "bot2", Class: models.ActorClassBot, BotScore: 0.68},
	}
	filtered := FilterClassificationsForAmplificationPool(classifications, accounts)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 amplification actors, got %d", len(filtered))
	}
	_, inauth, _ := AuthenticityPercentagesHighRecall(filtered)
	if inauth < 90 {
		t.Fatalf("expected high inauthentic %% after dropping mega organic, got %.1f", inauth)
	}
}

func TestIsObviousOrganicMegaAccount(t *testing.T) {
	if !IsObviousOrganicMegaAccount(models.AccountProfile{FollowersCount: 2_000_000, Verified: true}) {
		t.Fatal("expected mega verified to be organic")
	}
	if IsObviousOrganicMegaAccount(models.AccountProfile{FollowersCount: 300, Handle: "@amp99"}) {
		t.Fatal("expected low-reach to stay in pool")
	}
}

func TestHashtagDominated(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"#istandwithrussia", true},
		{"#IStandWithPutin Russia always support India #iSupportRussia", true},
		{"The hashtag #IStandWithPutin is trending in India and many accounts are bots", false},
		{"Just sharing my honest thoughts about the war today", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := hashtagDominated(tc.text); got != tc.want {
			t.Errorf("hashtagDominated(%q)=%v want %v", tc.text, got, tc.want)
		}
	}
}
