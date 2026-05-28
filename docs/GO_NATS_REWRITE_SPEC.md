# Autonomous Campaign Intelligence Platform Specification

## Mission

Build a complete backend MVP for a political campaign intelligence platform focused on security
in the age of AI, misinformation, fake news, coordinated influence, and public opinion monitoring.

The system no longer waits for a user to submit a claim, URL, or headline. It crawls
autonomously around predefined topics, currently politics, and continuously turns raw public data
into a dashboard that is useful immediately when a campaign manager opens it.

The primary user is a campaign manager or campaign intelligence lead. Their job is to understand:

1. What narratives are spreading about the candidate, opponents, and key political topics.
2. Which narratives appear organic, important, and worth engaging.
3. Which narratives appear artificial, coordinated, bot-amplified, foreign-influenced, or
   AI-generated.
4. Which important users, communities, influencers, accounts, and media sources interacted with
   those narratives.
5. Where sentiment is geographically concentrated.
6. What the campaign could do next.

The product promise is:

> "When misinformation begins moving, the campaign sees it early, understands who is pushing it,
> where it is landing, whether it looks organic or manipulated, and what action to take."

## Demo Narrative

The final hackathon demo should tell a concrete story inspired by the 2016 US election, where
Russian bot and troll networks spread political misinformation and amplified divisive narratives.

The demo perspective is a candidate's campaign manager.

The campaign manager opens the dashboard and immediately sees:

- political topics and narratives already collected by the autonomous crawler
- sentiment distribution by geography
- a high-risk alert showing abnormal, inauthentic, bot-driven activity traced to Russia-linked
  behavior
- which accounts and important users amplified the narrative
- which communities or interest groups were affected
- source evidence, metadata, confidence, and explanations
- a contrast between suspicious inauthentic activity and prominent organic discussion relevant to
  the candidate
- recommended campaign response actions

The close of the demo should explain:

- what happened in 2016
- what the campaign manager would have seen earlier with this system
- how the campaign could have responded faster, more accurately, and with less guesswork
- why AI makes this threat more scalable today

The demo must feel like a live intelligence dashboard, not a claim-checking form.

## Fixed Product Decisions

- The platform is backend-only. The web app/dashboard is built in another repository.
- The system autonomously crawls predefined topics. Initial topic: politics.
- Users do not submit individual claims as the normal workflow.
- Users configure campaign context, candidate profile, monitored topics, opponents, interest
  groups, regions, keywords, accounts, and communities.
- The dashboard must be useful from the first screen without requiring the user to enter input.
- X/Twitter is the primary source surface.
- Web/news/RSS sources are supporting surfaces for corroboration and provenance.
- Bright Data is the preferred provider for X and difficult web data.
- The provider layer must be extensible so future sources can be added without changing pipeline
  stages.
- The backend orchestrator is Go.
- NATS JetStream is required.
- Postgres is the system of record.
- The deployable backend is one Go binary containing the HTTP API, scheduler, crawler workers,
  RSS poller, analysis workers, and orchestrator.
- The system uses at-least-once delivery with retries, idempotency, and dead-letter subjects.
- Raw posts, articles, payloads, account metadata, and interaction metadata must be stored for
  auditability.
- The dashboard must include sources and metadata about the users/accounts surfacing the data.
- The dashboard must show important users who interacted with each narrative.
- The system must distinguish relevant data from irrelevant data using user-configured interest
  groups and campaign context.
- The system must surface both suspicious/manipulated narratives and prominent organic narratives.
- AI detection runs on posts, article text, snippets, and generated-seeming account behavior when
  enough evidence exists.
- Gemini is the LLM provider for narrative extraction, entity expansion, expert agents, report
  summarization, and LLM-based AI/misinformation reasoning.
- GPTZero and Sapling remain optional AI-text detector providers.
- The MVP may use demo fixture data to guarantee the 2016 scenario works, but the pipeline must
  also support live provider collection when API keys are configured.
- Authentication is not required for the hackathon demo.
- There is no test-suite requirement, but the backend must compile and run.

## Non-Goals

- Do not build the web app in this repository.
- Do not build a generic social media management tool.
- Do not attempt to determine absolute truth. The platform detects suspicious provenance,
  coordination, AI generation, influence activity, and risk.
- Do not require official X API credentials.
- Do not require Kubernetes.
- Do not make the MVP dependent on live access to controversial or unavailable 2016 data. Demo
  fixture/replay mode is allowed and encouraged.
- Do not expose raw private data. Only public or provider-returned public metadata is in scope.

## Core Product Experience

The campaign manager dashboard should have these primary surfaces.

### 1. Executive Situation Overview

Shows what matters now:

- top emerging narratives
- high-risk misinformation alerts
- sentiment trend
- geography heatmap
- influence velocity
- suspected inauthentic activity
- prominent organic discussion
- recommended response actions

### 2. Narrative Intelligence

Groups posts/articles into narratives such as:

- "candidate health rumor"
- "election fraud allegation"
- "foreign policy attack"
- "leaked email conspiracy"
- "candidate corruption claim"
- "supporter enthusiasm around rally"

Each narrative includes:

- title
- summary
- stance toward candidate: supportive, hostile, neutral, mixed
- relevance to configured interest groups
- first seen time
- timeline of spread
- geographic distribution
- sentiment distribution
- source mix
- key posts/articles
- important accounts involved
- bot/coordination risk
- AI-generation risk
- organic prominence score
- recommended action

### 3. Geo-Sentiment Map

Shows geopolitical or regional distribution of sentiment.

For the MVP:

- country-level distribution is required
- US state-level distribution is preferred for the 2016 demo
- city-level distribution is optional

Geo inference may use:

- account profile location
- post geotag if available
- language and timezone hints
- linked source domain/country
- content references to places
- provider metadata

Every geolocation result must include a confidence score and explanation. Unknown locations must
remain unknown; do not fabricate precision.

### 4. Influence Graph

Shows how a narrative moved:

- origin or earliest known sources
- amplifiers
- important users
- media sources
- communities
- quote/repost/reply relationships when available
- links between X posts and web articles
- suspicious clusters

Important users include:

- high-follower accounts
- verified/public figure accounts
- journalists
- campaign staff or political figures
- accounts central to spread
- accounts followed or configured by the campaign
- accounts frequently engaging with configured interest groups

### 5. Evidence Drawer

Every alert and narrative must let the frontend show evidence:

- source posts/articles
- raw provider metadata summary
- account metadata
- interaction counts
- timestamp evidence
- why the system thinks it is relevant
- why the system thinks it may be organic or inauthentic
- which signals are weak or uncertain

### 6. Response Recommendation Panel

The platform should recommend actions, not just identify problems.

Recommendations may include:

- monitor only
- prepare internal briefing
- respond publicly
- seed corrective information to specific interest groups
- contact journalists or validators
- avoid engagement because the narrative is low-relevance or likely bait
- escalate to security/legal/comms leadership

Each recommendation must include rationale and confidence.

## Campaign Configuration

The user configures what the system cares about. This replaces per-claim submission.

Required configuration model: `CampaignProfile`.

Fields:

- `campaign_id`
- `candidate_name`
- `candidate_aliases`
- `party`
- `office`
- `election_region`
- `monitored_topics`
- `opponents`
- `supporting_issues`
- `sensitive_issues`
- `interest_groups`
- `priority_regions`
- `important_accounts`
- `trusted_sources`
- `known_hostile_sources`
- `languages`
- `crawl_budget`
- `created_at`
- `updated_at`

`InterestGroup` fields:

- `group_id`
- `name`
- `description`
- `keywords`
- `hashtags`
- `accounts`
- `regions`
- `issues`
- `relevance_rules`
- `priority`

Examples:

- suburban undecided voters
- young voters
- veterans
- labor unions
- Jewish voters
- evangelical voters
- Ukrainian diaspora
- local journalists
- political donors
- election-security activists

Interest groups are central to the product. Data that does not map to the campaign, candidate,
opponents, topics, regions, or interest groups should be stored but not promoted to the dashboard.

## Autonomous Collection Flow

The logical pipeline is:

```text
CampaignProfile
  -> TopicSeedGeneration
  -> CrawlPlan
  -> SourceCollection
  -> RawSourceStorage
  -> AccountAndInteractionEnrichment
  -> NarrativeClustering
  -> RelevanceFiltering
  -> SentimentAndGeoAnalysis
  -> CoordinationAndBotAnalysis
  -> AIAndMisinformationAnalysis
  -> OrganicProminenceAnalysis
  -> ExpertCommitteeReview
  -> DashboardSnapshot
  -> Alerts
```

The runtime loop:

```text
Scheduler wakes up
  -> generate or refresh crawl plan from campaign profile
  -> crawl X heavily and web/RSS lightly
  -> collect posts, articles, authors, interactions, and metadata
  -> deduplicate globally
  -> enrich accounts and interactions
  -> cluster content into narratives
  -> filter by campaign relevance and interest groups
  -> score sentiment, geo, coordination, AI risk, misinformation risk, organic prominence
  -> update dashboard snapshot
  -> create alerts for high-risk or high-opportunity narratives
```

The crawler must run continuously on an interval. Default:

- high-priority X crawl: every 5 minutes
- broader X crawl: every 15 minutes
- web/news crawl: every 30 minutes
- RSS crawl: every 5 minutes
- narrative recomputation: every 10 minutes
- dashboard snapshot refresh: every 2 minutes

## Demo Fixture Mode

The demo must be complete even if live providers are rate-limited, unavailable, or insufficient.

Implement fixture/replay mode:

- `DEMO_MODE=true`
- load curated 2016-inspired fixture data from a service-local fixture directory or Postgres seed
- replay data in time windows so the dashboard appears live
- include Russian bot/troll-style account metadata, narrative spread, geo/sentiment, and
  important user interactions
- include both suspicious inauthentic narratives and organic prominent narratives

Fixture data should be realistic but must not falsely claim exact private knowledge. Label demo
data internally as fixture/replay data. The presentation can say it is inspired by documented
2016 influence operations.

Required demo scenario:

- Candidate campaign manager monitors a US political race.
- A hostile misinformation narrative appears about the candidate.
- Initial posts originate from suspicious accounts with Russia-linked signals.
- The narrative is amplified by bot-like accounts.
- Some important political users or media-adjacent accounts interact with it.
- The dashboard shows abnormal unauthentic behavior, geographic anomaly, bot risk, AI/text risk,
  source evidence, and recommended response.
- A second narrative appears organic and relevant to the candidate, showing the tool is not only
  a panic dashboard.

## HTTP API Contract

The web app should use these endpoints.

### Campaign Configuration

`POST /v1/campaigns`

Create a campaign profile.

`GET /v1/campaigns/{campaign_id}`

Retrieve profile.

`PUT /v1/campaigns/{campaign_id}`

Update candidate, topics, interest groups, important accounts, regions, and crawl budget.

`GET /v1/campaigns/{campaign_id}/interest-groups`

List interest groups.

`POST /v1/campaigns/{campaign_id}/interest-groups`

Create an interest group.

### Crawler Control

`POST /v1/campaigns/{campaign_id}/crawl/start`

Starts autonomous collection.

`POST /v1/campaigns/{campaign_id}/crawl/stop`

Stops autonomous collection without deleting data.

`POST /v1/campaigns/{campaign_id}/crawl/run-once`

Runs one crawl cycle for demos and debugging.

`GET /v1/campaigns/{campaign_id}/crawl/status`

Returns scheduler status, last run times, source counts, provider failures, and current crawl
budget usage.

### Dashboard

`GET /v1/campaigns/{campaign_id}/dashboard`

Returns the latest dashboard snapshot:

- executive overview
- risk alerts
- top narratives
- geo sentiment
- influence graph summary
- important users
- source mix
- recommendations
- generated timestamp

`GET /v1/campaigns/{campaign_id}/narratives`

List narratives with filters:

- `risk_label`
- `stance`
- `interest_group_id`
- `region`
- `organic_min`
- `bot_risk_min`
- `limit`
- `offset`

`GET /v1/narratives/{narrative_id}`

Retrieve one narrative with full evidence.

`GET /v1/narratives/{narrative_id}/sources`

Retrieve source posts/articles.

`GET /v1/narratives/{narrative_id}/graph`

Retrieve influence graph nodes and edges.

`GET /v1/campaigns/{campaign_id}/alerts`

List active alerts.

`POST /v1/alerts/{alert_id}/ack`

Mark alert as acknowledged.

### Demo

`POST /v1/demo/seed-2016`

Seeds or resets the 2016-inspired demo data.

`POST /v1/demo/replay/start`

Starts replaying fixture data into the pipeline.

`POST /v1/demo/replay/stop`

Stops replay.

### Health

- `GET /healthz`
- `GET /readyz`

## Data Contracts

All inter-stage messages are JSON and include:

- `message_id`
- `campaign_id`
- `trace_id`
- `stage`
- `attempt`
- `created_at`
- `schema_version`
- stage-specific payload

### TopicSeed

Generated from `CampaignProfile`.

Fields:

- `seed_id`
- `campaign_id`
- `text`
- `seed_type`: `candidate`, `opponent`, `issue`, `interest_group`, `hashtag`, `account`, `region`
- `priority`
- `source_interest_group_ids`
- `languages`
- `created_at`

### CrawlPlan

Fields:

- `crawl_plan_id`
- `campaign_id`
- `window_start`
- `window_end`
- `source_targets`
- `queries`
- `account_targets`
- `hashtag_targets`
- `region_targets`
- `max_x_results`
- `max_web_results`
- `max_rss_items`
- `priority`
- `created_at`

Default allocation:

- X: 75 percent of collection budget.
- Web/news/RSS: 20 percent.
- Fallback/discovery sources: 5 percent.

### SourceItem

Represents a post, article, RSS item, or provider result.

Fields:

- `source_id`
- `campaign_id`
- `global_dedup_key`
- `source_type`: `x_post`, `web_article`, `rss_item`, `search_result`, `unknown`
- `provider`
- `url`
- `canonical_url`
- `title`
- `text`
- `snippet`
- `language`
- `author`
- `published_at`
- `indexed_at`
- `collected_at`
- `engagement`
- `interactions`
- `linked_urls`
- `mentioned_entities`
- `hashtags`
- `raw_payload_ref`
- `availability_status`
- `collection_query`
- `fixture`
- `error`

### AccountProfile

Metadata about users/accounts surfacing or interacting with data.

Fields:

- `account_id`
- `platform`
- `handle`
- `display_name`
- `profile_url`
- `bio`
- `declared_location`
- `inferred_country`
- `inferred_region`
- `geo_confidence`
- `followers_count`
- `following_count`
- `created_at_platform`
- `verified`
- `account_type`: `unknown`, `person`, `media`, `politician`, `campaign`, `bot_like`,
  `organization`
- `influence_score`
- `bot_likelihood`
- `coordination_score`
- `reliability_score`
- `known_affiliations`
- `raw_metadata_ref`

### InteractionEvent

Fields:

- `interaction_id`
- `source_id`
- `account_id`
- `interaction_type`: `post`, `reply`, `repost`, `quote`, `like`, `mention`, `link_share`
- `occurred_at`
- `engagement_snapshot`
- `importance_score`
- `metadata`

### NarrativeCluster

Fields:

- `narrative_id`
- `campaign_id`
- `title`
- `summary`
- `canonical_claims`
- `stance_toward_candidate`
- `source_ids`
- `important_account_ids`
- `interest_group_matches`
- `first_seen_at`
- `last_seen_at`
- `timeline`
- `geo_distribution`
- `sentiment_distribution`
- `source_mix`
- `organic_prominence_score`
- `bot_coordination_risk`
- `foreign_influence_risk`
- `ai_generation_risk`
- `misinformation_risk`
- `overall_risk`
- `risk_label`
- `relevance_score`
- `recommended_action`
- `decision_explanation`
- `created_at`
- `updated_at`

### DashboardSnapshot

Fields:

- `snapshot_id`
- `campaign_id`
- `generated_at`
- `executive_summary`
- `top_alerts`
- `top_risky_narratives`
- `top_organic_narratives`
- `geo_sentiment`
- `important_users`
- `influence_graph_summary`
- `source_counts`
- `provider_failures`
- `recommended_actions`
- `demo_story_progress`

### Alert

Fields:

- `alert_id`
- `campaign_id`
- `narrative_id`
- `alert_type`: `misinformation`, `bot_activity`, `foreign_influence`, `ai_generated`,
  `organic_opportunity`, `important_user_engagement`
- `severity`: `low`, `medium`, `high`, `critical`
- `title`
- `summary`
- `why_now`
- `evidence`
- `recommended_action`
- `confidence`
- `created_at`
- `acknowledged_at`

## Source Providers and Extensibility

Every provider implements:

```go
type SourceProvider interface {
    ID() string
    Capabilities() ProviderCapabilities
    Collect(ctx context.Context, target SourceTarget) ([]SourceItem, error)
    FetchAccount(ctx context.Context, ref AccountRef) (*AccountProfile, error)
    FetchInteractions(ctx context.Context, ref SourceRef) ([]InteractionEvent, error)
}
```

Initial providers:

- `brightdata_x`: primary X collection, posts, account metadata, interactions when available.
- `brightdata_web`: web/news search and difficult page extraction.
- `rss`: configured feeds.
- `basic_web`: simple HTTP fallback.
- `demo_fixture`: deterministic 2016-inspired data replay provider.

Provider failures must not kill the dashboard. They are surfaced as degraded coverage.

## Relevance and Interest Group Filtering

The system should store broad data but only promote relevant data.

Relevance score:

```text
relevance_score =
  0.30 * candidate_or_opponent_match +
  0.20 * issue_match +
  0.20 * interest_group_match +
  0.15 * region_match +
  0.10 * important_account_match +
  0.05 * language_match
```

Promotion rules:

- `relevance_score >= 0.60`: show in dashboard if important.
- `0.40 <= relevance_score < 0.60`: store and use as context, but do not alert unless risk is
  high.
- `< 0.40`: store as background data, do not engage the campaign manager.

Interest group explanations must say which configured group matched and why.

## Scoring Model

### Organic Prominence

Organic-deemed data relevant to the candidate must be surfaced, not buried behind threat alerts.

Signals:

- diverse unrelated accounts
- natural time spread
- credible local sources
- non-repetitive text
- balanced engagement patterns
- low bot likelihood
- low coordination score
- high relevance to campaign topics or interest groups

Formula:

```text
organic_prominence_score =
  0.25 * relevance_score +
  0.20 * engagement_quality +
  0.20 * source_diversity +
  0.15 * important_user_engagement +
  0.10 * geo_relevance +
  0.10 * low_coordination_signal
```

### Bot and Coordination Risk

Signals:

- many new or low-credibility accounts
- synchronized posting
- repeated text/templates
- unusual repost-to-original ratio
- dense cluster behavior
- account creation bursts
- location/timezone mismatch
- high amplification from accounts with low organic history

Formula:

```text
bot_coordination_risk =
  0.25 * synchronization_score +
  0.20 * text_similarity_repetition +
  0.20 * suspicious_account_ratio +
  0.15 * amplification_anomaly +
  0.10 * network_density_anomaly +
  0.10 * account_age_anomaly
```

### Foreign Influence Risk

Signals:

- inferred geography inconsistent with claimed local identity
- Russia-linked fixture/provider indicators in demo mode
- timezone/language/domain patterns
- known hostile source matches
- unusual concentration outside the election region
- coordination with accounts that repeatedly push geopolitical narratives

For the demo, the dashboard should clearly show Russia-linked suspicion as a risk signal, with
evidence and confidence, not as an unsupported absolute accusation.

### Misinformation and AI Risk

Signals:

- AI text detector scores
- Gemini misinformation reasoning
- source reliability concerns
- lack of corroboration from trusted sources
- propagation from suspicious origins
- narrative mutation across posts
- debunking/contradictory signals

Risk is not truthfulness. It is the urgency and credibility of a possible manipulation event.

### Overall Narrative Risk

```text
overall_risk =
  0.25 * misinformation_risk +
  0.25 * bot_coordination_risk +
  0.20 * foreign_influence_risk +
  0.15 * ai_generation_risk +
  0.10 * spread_velocity +
  0.05 * important_user_engagement
```

Risk labels:

- `LOW`: background monitoring.
- `MEDIUM`: watch closely or prepare internal briefing.
- `HIGH`: likely campaign impact; comms/security should review.
- `CRITICAL`: immediate attention; suspicious high-impact manipulation is likely enough to act.

## Expert Committee

The expert committee reviews top narratives and alerts.

Required expert agents:

- `campaign_strategist`: judges campaign relevance and actionability.
- `misinformation_analyst`: evaluates deceptive or false narrative risk.
- `bot_network_analyst`: evaluates inauthentic coordination.
- `geopolitical_influence_analyst`: evaluates foreign influence indicators.
- `public_opinion_analyst`: evaluates sentiment, geography, and interest group impact.
- `skeptic`: challenges overclaiming and asks whether evidence supports the alert.

Each expert returns structured JSON:

- `score`
- `confidence`
- `key_reasons`
- `weaknesses`
- `recommended_action`
- `dashboard_explanation`

The committee output must be understandable to a non-technical campaign manager.

## NATS JetStream Design

Use one stream: `CAMPAIGN_INTEL`.

Subjects:

- `campaign.profile.updated`
- `crawl.plan.generate`
- `crawl.execute`
- `source.store`
- `account.enrich`
- `narrative.cluster`
- `narrative.relevance`
- `sentiment.geo.analyze`
- `coordination.analyze`
- `ai.misinfo.analyze`
- `organic.prominence.analyze`
- `experts.review`
- `dashboard.snapshot`
- `alert.create`
- `demo.replay`
- `dlq`

Retention:

- JetStream messages: 7 days.
- Raw source data: 30 days by default for demo usefulness.
- Dashboard snapshots: 30 days.
- Alerts/narratives: 90 days unless configured otherwise.

Delivery:

- at-least-once
- durable consumers per stage
- idempotency keys for source, account, interaction, narrative, and alert writes
- max 5 delivery attempts
- failed messages go to `dlq`

## Postgres Schema

Required tables:

- `campaigns`
- `interest_groups`
- `topic_seeds`
- `crawl_plans`
- `source_items`
- `raw_payloads`
- `account_profiles`
- `interaction_events`
- `narrative_clusters`
- `narrative_sources`
- `narrative_accounts`
- `sentiment_geo_points`
- `influence_edges`
- `dashboard_snapshots`
- `alerts`
- `provider_usage`
- `dedup_index`
- `demo_replay_state`

Important indexes:

- `campaign_id`
- `narrative_id`
- `source_type`
- `published_at`
- `collected_at`
- `global_dedup_key`
- `account_id`
- `handle`
- `risk_label`
- `relevance_score`
- `overall_risk`
- `country/region`

## Gemini Usage

Gemini is used for:

- topic seed expansion
- narrative naming and summarization
- entity extraction
- relevance reasoning
- misinformation reasoning
- expert committee agents
- response recommendations
- dashboard executive summary

Gemini must return structured JSON. The system must fall back to deterministic summaries and
rules if Gemini is unavailable.

## Directory Layout

The Go backend remains in `services/go-backend/`.

Recommended packages:

```text
cmd/provenance-api/main.go
internal/api/
internal/config/
internal/db/
internal/nats/
internal/models/
internal/scheduler/
internal/pipeline/topicseed/
internal/pipeline/crawlplan/
internal/pipeline/collect/
internal/pipeline/enrich/
internal/pipeline/cluster/
internal/pipeline/relevance/
internal/pipeline/sentimentgeo/
internal/pipeline/coordination/
internal/pipeline/aimisinfo/
internal/pipeline/organic/
internal/pipeline/experts/
internal/pipeline/snapshot/
internal/pipeline/alerts/
internal/providers/brightdata/
internal/providers/rss/
internal/providers/basicweb/
internal/providers/demofixture/
internal/llm/gemini/
internal/scoring/
internal/ratelimit/
internal/retry/
internal/storage/
migrations/
fixtures/demo-2016/
docker-compose.yml
.env.example
```

## Configuration

Required:

- `DATABASE_URL`
- `NATS_URL`

Recommended:

- `GEMINI_API_KEY`
- `BRIGHTDATA_API_KEY`

Optional:

- `DEMO_MODE`
- `DEMO_REPLAY_SPEED`
- `DEFAULT_TOPIC=politics`
- `DEFAULT_CAMPAIGN_ID`
- `RSS_FEEDS`
- `CRAWL_X_INTERVAL_SECONDS`
- `CRAWL_WEB_INTERVAL_SECONDS`
- `RSS_POLL_INTERVAL_SECONDS`
- `DASHBOARD_REFRESH_SECONDS`
- `MAX_X_RESULTS_PER_CYCLE`
- `MAX_WEB_RESULTS_PER_CYCLE`
- `BRIGHTDATA_BUDGET_USD`
- `GPTZERO_API_KEY`
- `SAPLING_API_KEY`

Defaults:

- `DEMO_MODE=true` for hackathon demo
- `DEFAULT_TOPIC=politics`
- `CRAWL_X_INTERVAL_SECONDS=300`
- `CRAWL_WEB_INTERVAL_SECONDS=1800`
- `RSS_POLL_INTERVAL_SECONDS=300`
- `DASHBOARD_REFRESH_SECONDS=120`
- `MAX_X_RESULTS_PER_CYCLE=500`
- `MAX_WEB_RESULTS_PER_CYCLE=100`

## Failure Behavior

- If live providers fail, use demo fixture data if `DEMO_MODE=true`.
- If Gemini fails, continue with deterministic rules and mark reduced explanation quality.
- If account enrichment fails, keep source items and mark account metadata incomplete.
- If geolocation is uncertain, show unknown rather than guessing.
- If a narrative is high-risk but evidence is thin, show it as "needs review" rather than
  overclaiming.
- If only organic data is found, the dashboard should still be useful and show relevant public
  opinion trends.

## Implementation Order

1. Update models from report-centric to campaign/narrative-centric.
2. Add campaign profile and interest group APIs.
3. Add autonomous scheduler and crawl control APIs.
4. Add demo fixture/replay provider for the 2016-inspired scenario.
5. Add provider registry methods for collection, account enrichment, and interactions.
6. Add source storage, account storage, interaction storage, and deduplication.
7. Add narrative clustering.
8. Add relevance and interest group filtering.
9. Add sentiment and geo analysis.
10. Add bot/coordination and foreign-influence scoring.
11. Add AI/misinformation analysis.
12. Add organic prominence scoring.
13. Add expert committee and recommendations.
14. Add dashboard snapshot and alert endpoints.
15. Verify Docker Compose, API startup, demo seeding, replay, and dashboard retrieval.

## Acceptance Criteria

The MVP is complete when:

- Opening the dashboard for the seeded campaign returns useful data immediately.
- The system crawls or replays autonomously without a user submitting a claim.
- A campaign profile can define topics, candidate, opponents, interest groups, regions, and
  important accounts.
- Interest groups affect what is promoted to the dashboard.
- The dashboard shows geo-sentiment distribution.
- The dashboard shows suspicious misinformation/bot/foreign-influence alerts.
- The dashboard shows prominent organic relevant narratives.
- Narratives include source evidence, account metadata, interaction metadata, and important users.
- The 2016-inspired demo clearly shows Russia-linked bot-driven misinformation behavior with
  explanations and confidence.
- The demo also shows how the campaign manager could have responded better.
- Provider failures degrade gracefully.
- The Go backend compiles and runs through Docker Compose.
- The API returns dashboard-ready JSON for the web app.

## Hackathon Winning Criteria

The project should optimize for:

- immediate visual impact
- a clear security-in-the-age-of-AI story
- evidence-backed explanations
- campaign-specific relevance
- credible handling of uncertainty
- actionability, not just detection
- a demo that works even without live data
- a clear before/after story around 2016-style influence operations

The "wow" moment is the campaign manager seeing an apparently organic political narrative, then
watching the system reveal that the spread pattern, account metadata, geography, timing, and text
signals point to coordinated inauthentic activity, while also separating genuine public opinion
that the campaign should care about.
# Provenance Platform Go/NATS Rewrite Specification

## Mission

Build a backend-only hackathon MVP that traces the likely origin and spread of a claim, news
article, tweet-like text, headline, phrase, or arbitrary user-provided string. The platform must
prioritize Twitter/X as a first-class source, use limited broader web search for corroboration,
detect whether early sources appear AI-generated, and return a dashboard-ready report containing
a timeline, ranked sources, confidence explanations, and a severity score.

The system exists to answer four user-facing questions:

1. Where did this claim or story likely first appear online?
2. How did it spread across X and the broader web?
3. How likely is it that the earliest meaningful sources were AI-generated?
4. How serious is the potential issue, and why did the system reach that conclusion?

This rewrite replaces the current Python in-process async queue pipeline with a Go orchestrator,
NATS JetStream, Postgres, source adapters, and an HTTP API designed for a separate web app.

## Fixed Product Decisions

- The deliverable is a backend HTTP API for a web app built in another repository.
- The backend must support one-off submissions, bulk submissions, continuous RSS ingestion, status
  polling, cancellation, and report retrieval.
- User input can be either a URL or arbitrary text. Arbitrary text may be a single word, a tweet,
  a title, a claim, a paragraph, or any other short text.
- URL input must be fetched and converted into canonical article text before analysis.
- Twitter/X is the primary search surface and must be treated as a first-class provenance source.
- Broader web search remains part of the platform, but it is limited and used mainly for
  corroboration, article discovery, timestamp validation, and non-X provenance.
- Bright Data is the preferred provider for X and difficult web content. The design must allow
  adding new providers without changing pipeline stages.
- No direct X API credentials are assumed.
- AI detection runs on tweets, article pages, snippets, and any other text-bearing source when
  enough text is available.
- Gemini is the LLM provider for semantic expansion, source reasoning, expert committee analysis,
  report summarization, and LLM-based AI detection.
- Existing AI detection methods remain: GPTZero, Sapling, local statistical/linguistic analysis,
  and LLM judge. A final expert committee stage is added.
- NATS JetStream is required.
- The deployable application is one Go binary containing the HTTP API, RSS poller, scheduler,
  workers, and orchestrator.
- Processing uses at-least-once delivery with retries, idempotency keys, and dead-letter subjects.
- Jobs must be resumable after process restart.
- Intermediate job state, raw source records, and reports are retained for one week by default.
- Postgres is the system of record.
- Raw scraped pages and raw tweet/source payloads must be stored for auditability.
- Deduplication must work across submissions.
- Report IDs are UUIDs.
- Docker Compose is the target deployment environment.
- There is no test suite requirement for the hackathon MVP.
- Observability is not a major requirement, but the service must still expose basic health and
  readiness endpoints and use structured logs.
- Authentication is not required; this is a demo backend.
- User-triggered cancellation is required.

## Non-Goals

- Do not build the web app in this repository.
- Do not implement a production-grade auth system.
- Do not require official X API credentials.
- Do not depend on Kubernetes.
- Do not make the system perfectly production-grade. Reliability, resumability, and clear report
  explanations matter more than enterprise completeness.
- Do not require tests for this MVP.

## High-Level Architecture

The backend is one Go binary with several internal components:

```text
HTTP API
  |
  v
Postgres job/report state <----> NATS JetStream
  |                              |
  |                              v
  |                       Pipeline workers
  |                              |
  v                              v
Dashboard-ready report     External providers
                             - Bright Data X search
                             - Bright Data web search/fetch
                             - Optional web search adapters
                             - Gemini
                             - GPTZero
                             - Sapling
```

The Go process starts:

- an HTTP server
- a NATS JetStream connection
- a Postgres connection pool
- worker goroutines for each pipeline stage
- an RSS poller if RSS feeds are configured
- a retention cleanup loop
- a cancellation watcher

Although it is one binary, each pipeline stage must be implemented as a separate package with a
small interface and a JetStream worker wrapper. This preserves the ability to split stages into
separate services later without redesigning the code.

## Pipeline Flow

The logical flow preserves the current model chain and adds source-specific enrichment and expert
committee analysis:

```text
NewsItem
  -> InputNormalization
  -> PermutationSet
  -> SourceSearchPlan
  -> SourceResultSet
  -> EnrichedSourceSet
  -> AnalyzedSet
  -> AISignatureSet
  -> ExpertCommitteeReview
  -> ProvenanceReport
```

The minimum dashboard flow is:

```text
User submits URL/text
  -> API creates job/report UUID
  -> Normalize input
  -> Generate dynamic search phrases
  -> Search X heavily and web lightly
  -> Fetch/enrich source content and metadata
  -> Deduplicate and persist raw sources
  -> Rank by time, similarity, source confidence, and AI-origin signal
  -> Run AI detection
  -> Run expert committee
  -> Compute final severity/confidence
  -> Store dashboard-ready report
```

## HTTP API Contract

The web app should only need these endpoints.

### Submit One Job

`POST /v1/reports`

Request:

```json
{
  "input_type": "auto",
  "input": "https://example.com/news/story",
  "priority": "normal",
  "options": {
    "include_raw_sources": false,
    "max_sources": 250,
    "x_search_depth": "standard",
    "web_search_depth": "limited"
  }
}
```

Rules:

- `input_type` can be `auto`, `url`, or `text`.
- `auto` must detect URLs by parsing, not by a fragile string prefix only.
- `priority` can be `low`, `normal`, or `high`.
- `max_sources` default is `250`.
- `x_search_depth` default is `standard`.
- `web_search_depth` default is `limited`.

Response:

```json
{
  "report_id": "uuid",
  "job_id": "uuid",
  "status": "queued"
}
```

### Submit Bulk Jobs

`POST /v1/reports/bulk`

Request:

```json
{
  "items": [
    { "input": "claim text", "input_type": "text" },
    { "input": "https://example.com/article", "input_type": "url" }
  ],
  "priority": "normal"
}
```

Response:

```json
{
  "jobs": [
    { "report_id": "uuid", "job_id": "uuid", "status": "queued" }
  ]
}
```

Bulk submission must enqueue one independent pipeline job per item.

### Job Status

`GET /v1/jobs/{job_id}`

Response:

```json
{
  "job_id": "uuid",
  "report_id": "uuid",
  "status": "running",
  "current_stage": "source_search",
  "progress": {
    "completed_stages": 3,
    "total_stages": 9,
    "sources_found": 84,
    "sources_analyzed": 12
  },
  "created_at": "2026-05-28T10:00:00Z",
  "updated_at": "2026-05-28T10:03:12Z"
}
```

Statuses:

- `queued`
- `running`
- `completed`
- `failed`
- `cancelled`
- `partial`

Use `partial` when a report exists but some providers failed or exhausted budget/rate limits.

### Cancel Job

`POST /v1/jobs/{job_id}/cancel`

Cancellation must:

- mark the job as cancellation requested in Postgres
- stop future stage processing for that job
- allow currently running provider calls to finish or timeout
- emit no more downstream messages after the next cancellation checkpoint
- return any partial report if enough data exists

### Retrieve Report

`GET /v1/reports/{report_id}`

Returns the complete dashboard-ready `ProvenanceReport`.

### Search Report History

`GET /v1/reports?query=...&status=...&limit=...&offset=...`

This endpoint powers a demo history page. It searches report summaries, original input, source
titles, domains, X handles, and normalized claim text.

### Health

- `GET /healthz`: process is alive.
- `GET /readyz`: Postgres and NATS are reachable.

## Data Contracts

All inter-stage messages are JSON. Every message must include:

- `message_id`
- `job_id`
- `report_id`
- `trace_id`
- `stage`
- `attempt`
- `created_at`
- `schema_version`
- stage-specific payload

`schema_version` starts at `1`.

### NewsItem

Represents the original user or RSS input after first normalization.

Fields:

- `item_id`: UUID
- `report_id`: UUID
- `input_type`: `url`, `text`, or `rss`
- `original_input`: raw user input
- `canonical_url`: nullable URL
- `headline`: nullable string
- `body`: nullable string
- `summary`: nullable string
- `language`: BCP-47 language code, default `en`
- `source_type`: `manual`, `rss`
- `source_channel`: nullable RSS feed URL
- `published_at`: nullable timestamp
- `ingested_at`: timestamp
- `content_hash`: SHA-256 of normalized meaningful text
- `raw_metadata`: JSON object

For URL input, `body` should contain extracted page text when available. If extraction fails,
the system still proceeds using URL, title, Open Graph metadata, and any available snippet.

### PermutationSet

Represents dynamic query phrases generated from the normalized input.

Fields:

- `source_item`: embedded `NewsItem`
- `canonical_claim`: one concise normalized claim or topic
- `input_classification`: `word`, `phrase`, `tweet`, `headline`, `article`, `unknown`
- `permutations`: array of `Permutation`
- `entity_terms`: extracted people, organizations, places, products, hashtags, cashtags
- `url_terms`: normalized domains, path fragments, quoted article title
- `model_used`: Gemini model name
- `generated_at`: timestamp
- `total_count`: integer

`Permutation` fields:

- `text`
- `strategy`
- `intent`
- `confidence`
- `recommended_sources`

Strategies must include, where applicable:

- exact quote
- core claim paraphrase
- entity-only query
- hashtag query
- URL/link query
- author/account query
- temporal phrasing
- negated/debunking phrasing
- translated or transliterated phrase if language detection suggests it

The count is dynamic:

- Single word: 10-25 focused permutations.
- Short phrase/title/tweet: 30-80 permutations.
- Article URL with extracted body: 60-120 permutations.
- Poor extraction or low-confidence input: keep the count lower and preserve exact terms.

### SourceSearchPlan

Determines how much work to spend per source type.

Fields:

- `source_permutation_set`
- `source_targets`: array of target plans
- `max_total_results`
- `max_x_results`
- `max_web_results`
- `rate_limit_profile`
- `created_at`

Default result allocation:

- X: 70 percent of search budget.
- Web/news: 25 percent of search budget.
- Fallback/general search: 5 percent of search budget.

This allocation can shift when the input is a URL. URL input should search X for links and quoted
phrases first, then use web search to find copies, syndication, and canonical article references.

### SourceResult

Represents a single tweet, article, search result, RSS item, or other external source.

Fields:

- `source_id`: UUID
- `global_dedup_key`: stable hash
- `source_type`: `x_post`, `web_article`, `rss_item`, `search_result`, `unknown`
- `provider`: provider ID, such as `brightdata_x`
- `url`: nullable URL
- `canonical_url`: nullable URL
- `title`: nullable string
- `snippet`: string
- `full_text`: nullable string
- `author_name`: nullable string
- `author_handle`: nullable string
- `author_url`: nullable URL
- `published_at`: nullable timestamp
- `indexed_at`: nullable timestamp
- `scraped_at`: timestamp
- `engagement`: JSON object
- `source_metadata`: JSON object
- `raw_payload_ref`: Postgres/object reference
- `query_used`: string
- `http_status`: nullable integer
- `availability_status`: `available`, `deleted`, `private`, `rate_limited`, `unknown`
- `error`: nullable string

For X posts, `engagement` should include available values for reposts, replies, likes, quotes,
views, bookmarks, and any provider-specific fields. If a post is deleted or unavailable, store
whatever evidence the provider gives, mark `availability_status`, and keep the record as a
partial source.

### EnrichedSource

Adds normalized content, reliability signals, and extracted relationships.

Fields:

- embedded `SourceResult`
- `normalized_text`
- `text_hash`
- `language`
- `entities`
- `linked_urls`
- `quoted_sources`
- `account_reliability`
- `timestamp_confidence`
- `content_completeness`
- `source_confidence`
- `enrichment_explanation`

Confidence fields are floats from `0.0` to `1.0`.

### RankedResult

Ranks a source for provenance relevance.

Fields:

- embedded `EnrichedSource`
- `similarity_score`
- `chronological_score`
- `source_confidence`
- `ai_origin_signal`
- `spread_signal`
- `composite_score`
- `chronological_rank`
- `is_candidate_origin`
- `ranking_explanation`

### AISignatureResult

Fields:

- embedded `RankedResult`
- `detection_methods`
- `ensemble_score`
- `is_ai_generated`
- `confidence`
- `explanation`

`detection_methods` includes GPTZero, Sapling, statistical analysis, Gemini LLM judge, and any
method that was attempted. Failed optional methods are included with an error and excluded from
renormalized scoring.

### ExpertCommitteeReview

The expert committee is a final reasoning layer that reviews the top candidate origins and the
spread pattern. It must not replace deterministic scores; it adds an explainable adjustment.

Fields:

- `review_id`
- `reviewed_candidates`
- `experts`
- `committee_score`
- `confidence_adjustment`
- `risk_adjustment`
- `consensus_label`
- `dissenting_views`
- `summary`
- `created_at`

Required expert agents:

- `provenance_expert`: judges whether a source plausibly originated the story.
- `disinformation_expert`: judges coordinated or suspicious spread patterns.
- `linguistic_forensics_expert`: judges AI-like text patterns and unnatural phrasing.
- `source_reliability_expert`: judges account/domain credibility and timestamp quality.
- `skeptic_expert`: argues against overclaiming and identifies weak evidence.

Each expert returns structured JSON with:

- `score`: float `0.0` to `1.0`
- `confidence`: float `0.0` to `1.0`
- `key_reasons`: array of strings
- `concerns`: array of strings
- `recommended_label`

### ProvenanceReport

The report must be directly usable by a dashboard.

Fields:

- `report_id`
- `job_id`
- `source_item`
- `status`
- `canonical_claim`
- `timeline`
- `candidate_origins`
- `ai_signature_results`
- `expert_committee_review`
- `earliest_high_confidence_source`
- `earliest_indexed_source`
- `disinformation_risk`
- `risk_label`
- `confidence`
- `severity_explanation`
- `source_decision_explanations`
- `provider_failures`
- `pipeline_version`
- `generated_at`
- `expires_at`
- `total_duration_seconds`
- `summary`

Risk labels:

- `LOW`: weak evidence of AI origin or suspicious spread.
- `MEDIUM`: some AI or spread concerns, but provenance is uncertain.
- `HIGH`: strong AI-generation signals on early high-confidence sources or suspicious spread.
- `CRITICAL`: early high-confidence source appears AI-generated and spread pattern is rapid,
  coordinated, or otherwise materially suspicious.

## Source Providers and Extensibility

Every external source implementation must satisfy this Go interface shape:

```go
type SourceProvider interface {
    ID() string
    Capabilities() ProviderCapabilities
    Search(ctx context.Context, query SourceQuery) ([]SourceResult, error)
    Fetch(ctx context.Context, ref SourceRef) (*SourceResult, error)
}
```

`ProviderCapabilities` includes:

- supported source types
- whether X search is supported
- whether deleted/unavailable evidence may be returned
- whether full text fetch is supported
- whether published timestamps are reliable
- cost model
- rate limit profile

Provider implementations must live under `internal/providers/`.

Initial providers:

- `brightdata_x`: primary X search/fetch provider.
- `brightdata_web`: primary difficult web fetch/search provider.
- `basic_web`: limited fallback using regular HTTP and a simple search provider where available.
- `rss`: continuous ingestion provider, not a general search provider.

Future providers should be added by implementing `SourceProvider` and registering it in a provider
registry. Pipeline stages must depend on the interface and registry, never on concrete providers.

## Why These Sources Are Applicable

The final report must explicitly explain why each selected source was considered applicable or
not applicable.

Applicability factors:

- semantic match to the original input or canonical claim
- exact phrase, URL, hashtag, entity, or author match
- timestamp availability and confidence
- whether it is early enough to plausibly be an origin or amplifier
- source type relevance, especially X posts for social spread
- author/account/domain reliability
- content completeness
- whether the source links to, quotes, or appears copied from another source
- AI-generation probability
- provider quality and raw evidence availability

Every candidate origin must include a `decision_explanation` with:

- why it was included
- what evidence supports its timestamp
- what evidence supports semantic relevance
- what weakens the conclusion
- whether it appears to be an origin, amplifier, derivative, or unrelated mention

This is mandatory because the dashboard must explain seriousness and not merely display scores.

## Scoring Model

The system computes three major scores:

1. `source_confidence`: whether a source record is trustworthy enough to use.
2. `provenance_score`: whether it plausibly belongs near the origin of the story.
3. `disinformation_risk`: how serious the overall finding is.

### Source Confidence

Default formula:

```text
source_confidence =
  0.30 * timestamp_confidence +
  0.20 * content_completeness +
  0.20 * provider_reliability +
  0.15 * author_or_domain_reliability +
  0.15 * cross_source_corroboration
```

For X posts:

- verified/known account signals improve reliability but must not dominate.
- high engagement improves spread significance, not truthfulness.
- deleted/private/unavailable status lowers content completeness but may still be useful if
  timestamp and metadata are available.

### Provenance Ranking

Default formula:

```text
composite_score =
  0.35 * semantic_similarity +
  0.25 * chronological_score +
  0.20 * source_confidence +
  0.10 * ai_origin_signal +
  0.10 * spread_signal
```

`chronological_score` favors earlier indexed or published sources. Null timestamps receive a low
score unless there is strong provider evidence.

The selected `earliest_high_confidence_source` must satisfy:

- `source_confidence >= 0.65`
- `semantic_similarity >= 0.55`
- has a usable `published_at` or `indexed_at`

If no source meets these thresholds, the report must say provenance is uncertain.

### AI Detection Ensemble

Default method weights:

- GPTZero: `0.25`
- Sapling: `0.20`
- local statistical/linguistic: `0.20`
- Gemini LLM judge: `0.20`
- expert committee linguistic/provenance contribution: `0.15`

When optional methods fail or are not configured, renormalize over successful methods. Do not
treat missing providers as zero. If fewer than two methods succeed, cap AI confidence at `0.50`.

The AI threshold is dynamic:

- Very short text under 40 words: threshold `0.75`, confidence capped at `0.60`.
- Tweet-length text: threshold `0.70`.
- Article-length text: threshold `0.65`.
- Strong multi-method agreement: allow threshold `0.60` if at least three methods succeed.

### Overall Risk

Default formula:

```text
disinformation_risk =
  0.30 * max_ai_score_on_candidate_origins +
  0.25 * provenance_confidence +
  0.20 * spread_velocity_or_engagement +
  0.15 * source_reliability_concern +
  0.10 * expert_committee_risk_adjustment
```

Risk is not a truthfulness score. It means "this item deserves attention because early or
important sources may be AI-generated and the spread pattern is concerning."

## NATS JetStream Design

Use one stream:

`PROVENANCE`

Subjects:

- `provenance.input.normalize`
- `provenance.semantic.generate`
- `provenance.search.plan`
- `provenance.search.execute`
- `provenance.source.enrich`
- `provenance.analyze.rank`
- `provenance.ai.detect`
- `provenance.experts.review`
- `provenance.report.finalize`
- `provenance.job.cancel`
- `provenance.dlq`

Retention:

- JetStream messages: 7 days.
- Max delivery attempts per stage: 5.
- Ack timeout: 2 minutes by default.
- Backoff: 10 seconds, 30 seconds, 2 minutes, 5 minutes, 15 minutes.

Each stage uses a durable consumer. Workers must ack only after:

- the stage output is persisted to Postgres when appropriate
- the downstream message is published successfully
- job cancellation has been checked

At-least-once delivery means every stage must be idempotent. Use `(job_id, stage)` and stable
input hashes to prevent duplicate work from producing duplicate database records.

Dead-letter rules:

- After max delivery attempts, publish a normalized error envelope to `provenance.dlq`.
- Mark the job `partial` if a report can still be produced.
- Mark the job `failed` only if no meaningful report can be produced.

## Postgres Schema

Use Postgres as the source of truth.

Required tables:

- `jobs`
- `reports`
- `news_items`
- `permutation_sets`
- `source_results`
- `source_raw_payloads`
- `ranked_results`
- `ai_signature_results`
- `expert_reviews`
- `provider_usage`
- `rss_feeds`
- `dedup_index`

`jobs` fields:

- `id`
- `report_id`
- `status`
- `current_stage`
- `cancel_requested`
- `error`
- `created_at`
- `updated_at`
- `completed_at`
- `expires_at`

`source_results` must index:

- `report_id`
- `global_dedup_key`
- `source_type`
- `provider`
- `published_at`
- `indexed_at`
- `canonical_url`
- `author_handle`
- `text_hash`

`dedup_index` stores cross-submission deduplication keys:

- normalized URL hash
- text hash
- X post ID when available
- canonical provider ID when available
- first_seen_at
- last_seen_at

Raw payloads can be stored in Postgres JSONB for the MVP. If payloads become large, switch
`source_raw_payloads` to object storage references without changing higher-level models.

## RSS Continuous Ingestion

RSS is the only continuous ingestion source in scope.

Behavior:

- Poll configured RSS feeds on an interval, default 5 minutes.
- Parse feed items into `NewsItem` records.
- Deduplicate by feed GUID, link, and normalized title hash.
- Enqueue new items into `provenance.input.normalize`.
- Store RSS feed health and last poll time.

Configuration:

- `RSS_FEEDS`
- `RSS_POLL_INTERVAL_SECONDS`
- `RSS_MAX_ITEMS_PER_POLL`

RSS ingestion must use the same pipeline as manual submissions.

## Provider Rate Limiting and Cost Controls

Rate limits are per external provider, not per user.

Configurable limiters:

- Bright Data X requests per minute.
- Bright Data web requests per minute.
- Gemini requests and tokens per minute.
- GPTZero requests per minute.
- Sapling requests per minute.
- Basic web per-domain requests per second.

The current Bright Data budget guard must remain conceptually intact. If a configured
`BRIGHTDATA_BUDGET_USD` exists, the provider must estimate cost before requests and refuse calls
that exceed the budget. No additional global cost cap is required beyond existing configured
provider caps.

Provider usage must be recorded in `provider_usage` for demo transparency.

## Gemini Usage

Gemini is used for:

- input classification
- canonical claim extraction
- dynamic permutation generation
- entity extraction when local extraction is insufficient
- LLM AI-generation judge
- expert committee agents
- final report summary

Gemini calls must request structured JSON responses. Prompts must instruct the model to avoid
claiming certainty when timestamps or content are weak.

Suggested config:

- `GEMINI_API_KEY`
- `GEMINI_MODEL`
- `GEMINI_FAST_MODEL`
- `GEMINI_REQUESTS_PER_MINUTE`
- `GEMINI_TOKENS_PER_MINUTE`

Use the cheaper/faster model for permutations and committee experts unless report quality is
insufficient.

## Directory Layout for the Go Backend

Recommended layout:

```text
cmd/provenance-api/main.go
internal/api/
internal/config/
internal/db/
internal/nats/
internal/models/
internal/pipeline/
internal/pipeline/normalize/
internal/pipeline/semantic/
internal/pipeline/searchplan/
internal/pipeline/search/
internal/pipeline/enrich/
internal/pipeline/rank/
internal/pipeline/aidetect/
internal/pipeline/experts/
internal/pipeline/finalize/
internal/providers/
internal/providers/brightdata/
internal/providers/basicweb/
internal/providers/rss/
internal/llm/gemini/
internal/scoring/
internal/storage/
internal/ratelimit/
internal/retry/
internal/cancel/
migrations/
docker-compose.yml
.env.example
```

## Docker Compose

Compose must include:

- Go backend service.
- NATS with JetStream enabled.
- Postgres.

Optional for convenience:

- `nats-box` for debugging.
- Postgres admin UI if desired.

The backend must run database migrations on startup or provide a documented migration command.

## Configuration

Required:

- `DATABASE_URL`
- `NATS_URL`
- `GEMINI_API_KEY`

Optional:

- `BRIGHTDATA_API_KEY`
- `BRIGHTDATA_BUDGET_USD`
- `GPTZERO_API_KEY`
- `SAPLING_API_KEY`
- `RSS_FEEDS`
- `RSS_POLL_INTERVAL_SECONDS`
- `RETENTION_DAYS`
- `MAX_SOURCES_PER_JOB`
- `MAX_X_RESULTS_PER_JOB`
- `MAX_WEB_RESULTS_PER_JOB`
- `TOP_CANDIDATES`
- `SOURCE_CONFIDENCE_THRESHOLD`
- `SEMANTIC_SIMILARITY_THRESHOLD`
- provider-specific rate limits

Defaults:

- `RETENTION_DAYS=7`
- `MAX_SOURCES_PER_JOB=250`
- `MAX_X_RESULTS_PER_JOB=175`
- `MAX_WEB_RESULTS_PER_JOB=60`
- `TOP_CANDIDATES=15`
- `SOURCE_CONFIDENCE_THRESHOLD=0.65`
- `SEMANTIC_SIMILARITY_THRESHOLD=0.55`

## Failure Behavior

The platform should prefer partial useful reports over total failure.

Examples:

- Gemini permutation generation fails: continue with exact query, extracted entities, URL terms,
  and title phrases.
- Bright Data X fails: continue with web search and mark provider failure.
- GPTZero or Sapling is missing or fails: exclude from AI ensemble and explain reduced confidence.
- Article extraction fails: continue with URL metadata, title, snippets, and X link searches.
- No high-confidence source found: produce report with `risk_label=LOW` or `MEDIUM` depending on
  AI/spread evidence, and state that provenance is uncertain.

All provider errors must be visible in `provider_failures`.

## Cancellation Checkpoints

Every stage must check cancellation:

- before starting provider calls
- after provider calls return
- before publishing the next JetStream message
- before finalizing a report

If cancelled after partial useful data exists, finalize a partial report with status `cancelled`.

## Implementation Order

1. Create Go module, config loader, Docker Compose, Postgres migrations, and NATS JetStream setup.
2. Implement shared models and JSON envelopes.
3. Implement HTTP API for submission, status, cancellation, and report retrieval.
4. Implement Postgres repositories and idempotency helpers.
5. Implement JetStream worker framework with retries, acking, cancellation checks, and DLQ.
6. Implement input normalization for URL/text/RSS.
7. Implement Gemini canonical claim and dynamic permutation generation.
8. Implement provider registry and Bright Data X/web providers.
9. Implement source search planning and execution.
10. Implement source enrichment, raw payload storage, deduplication, and metadata extraction.
11. Implement ranking and scoring.
12. Implement AI detection methods.
13. Implement expert committee review.
14. Implement final report generation.
15. Implement RSS poller.
16. Implement retention cleanup.
17. Run end-to-end manually through Docker Compose with a text input and a URL input.

## Acceptance Criteria

The MVP is complete when:

- A user can submit arbitrary text and receive a completed report.
- A user can submit a URL and receive a completed report.
- The backend can process multiple submitted jobs concurrently.
- RSS ingestion can continuously enqueue new items.
- X sources are included as first-class timeline entries.
- Web sources are included as supporting timeline entries.
- The report contains candidate origins, timeline, AI scores, confidence, risk label, and clear
  explanations.
- Each selected source explains why it was included and how serious its evidence is.
- Provider failures degrade to partial reports instead of crashing the job.
- Jobs survive process restart because state is stored in Postgres and JetStream.
- Cancellation works.
- Dead-letter messages are produced for repeatedly failing stages.
- Reports and raw source data expire after one week by default.
