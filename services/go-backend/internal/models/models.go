package models

import (
	"time"
)

const SchemaVersion = 1

type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
	JobStatusPartial   JobStatus = "partial"
)

type Stage string

const (
	StageNormalize Stage = "input_normalization"
	StageSemantic  Stage = "semantic_generation"
	StagePlan      Stage = "search_planning"
	StageSearch    Stage = "source_search"
	StageEnrich    Stage = "source_enrichment"
	StageRank      Stage = "ranking"
	StageAIDetect  Stage = "ai_detection"
	StageExperts   Stage = "expert_committee"
	StageFinalize  Stage = "finalize_report"
)

type InputType string

const (
	InputTypeAuto InputType = "auto"
	InputTypeURL  InputType = "url"
	InputTypeText InputType = "text"
	InputTypeRSS  InputType = "rss"
)

type SourceType string

const (
	SourceTypeXPost        SourceType = "x_post"
	SourceTypeWebArticle   SourceType = "web_article"
	SourceTypeRSSItem      SourceType = "rss_item"
	SourceTypeSearchResult SourceType = "search_result"
	SourceTypeUnknown      SourceType = "unknown"
)

type AvailabilityStatus string

const (
	AvailabilityAvailable   AvailabilityStatus = "available"
	AvailabilityDeleted     AvailabilityStatus = "deleted"
	AvailabilityPrivate     AvailabilityStatus = "private"
	AvailabilityRateLimited AvailabilityStatus = "rate_limited"
	AvailabilityUnknown     AvailabilityStatus = "unknown"
)

type SubmitReportRequest struct {
	InputType InputType      `json:"input_type"`
	Input     string         `json:"input"`
	Priority  string         `json:"priority"`
	Options   *ReportOptions `json:"options,omitempty"`
}

type BulkSubmitRequest struct {
	Items    []SubmitReportRequest `json:"items"`
	Priority string                `json:"priority"`
}

type ReportOptions struct {
	IncludeRawSources bool   `json:"include_raw_sources"`
	MaxSources        int    `json:"max_sources"`
	XSearchDepth      string `json:"x_search_depth"`
	WebSearchDepth    string `json:"web_search_depth"`
}

type SubmitReportResponse struct {
	ReportID string    `json:"report_id"`
	JobID    string    `json:"job_id"`
	Status   JobStatus `json:"status"`
}

type BulkSubmitResponse struct {
	Jobs []SubmitReportResponse `json:"jobs"`
}

type JobProgress struct {
	CompletedStages int `json:"completed_stages"`
	TotalStages     int `json:"total_stages"`
	SourcesFound    int `json:"sources_found"`
	SourcesAnalyzed int `json:"sources_analyzed"`
}

type JobRecord struct {
	ID              string      `json:"job_id"`
	ReportID        string      `json:"report_id"`
	Status          JobStatus   `json:"status"`
	CurrentStage    Stage       `json:"current_stage"`
	CancelRequested bool        `json:"cancel_requested"`
	Error           *string     `json:"error,omitempty"`
	Progress        JobProgress `json:"progress"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	CompletedAt     *time.Time  `json:"completed_at,omitempty"`
	ExpiresAt       time.Time   `json:"expires_at"`
}

type Envelope[T any] struct {
	MessageID     string    `json:"message_id"`
	JobID         string    `json:"job_id"`
	ReportID      string    `json:"report_id"`
	TraceID       string    `json:"trace_id"`
	Stage         Stage     `json:"stage"`
	Attempt       int       `json:"attempt"`
	CreatedAt     time.Time `json:"created_at"`
	SchemaVersion int       `json:"schema_version"`
	Payload       T         `json:"payload"`
}

type NewsItem struct {
	ItemID        string            `json:"item_id"`
	ReportID      string            `json:"report_id"`
	InputType     InputType         `json:"input_type"`
	OriginalInput string            `json:"original_input"`
	CanonicalURL  *string           `json:"canonical_url,omitempty"`
	Headline      *string           `json:"headline,omitempty"`
	Body          *string           `json:"body,omitempty"`
	Summary       *string           `json:"summary,omitempty"`
	Language      string            `json:"language"`
	SourceType    string            `json:"source_type"`
	SourceChannel *string           `json:"source_channel,omitempty"`
	PublishedAt   *time.Time        `json:"published_at,omitempty"`
	IngestedAt    time.Time         `json:"ingested_at"`
	ContentHash   string            `json:"content_hash"`
	RawMetadata   map[string]any    `json:"raw_metadata"`
	Options       ReportOptions     `json:"options"`
	ProviderNotes []ProviderFailure `json:"provider_notes,omitempty"`
}

type Permutation struct {
	Text               string   `json:"text"`
	Strategy           string   `json:"strategy"`
	Intent             string   `json:"intent"`
	Confidence         float64  `json:"confidence"`
	RecommendedSources []string `json:"recommended_sources"`
}

type PermutationSet struct {
	SourceItem          NewsItem          `json:"source_item"`
	CanonicalClaim      string            `json:"canonical_claim"`
	InputClassification string            `json:"input_classification"`
	Permutations        []Permutation     `json:"permutations"`
	EntityTerms         []string          `json:"entity_terms"`
	URLTerms            []string          `json:"url_terms"`
	ModelUsed           string            `json:"model_used"`
	GeneratedAt         time.Time         `json:"generated_at"`
	TotalCount          int               `json:"total_count"`
	ProviderFailures    []ProviderFailure `json:"provider_failures,omitempty"`
}

type SourceTarget struct {
	Provider       string   `json:"provider"`
	SourceTypes    []string `json:"source_types"`
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	Priority       int      `json:"priority"`
	SearchStrategy string   `json:"search_strategy"`
}

type SourceSearchPlan struct {
	SourcePermutationSet PermutationSet    `json:"source_permutation_set"`
	SourceTargets        []SourceTarget    `json:"source_targets"`
	MaxTotalResults      int               `json:"max_total_results"`
	MaxXResults          int               `json:"max_x_results"`
	MaxWebResults        int               `json:"max_web_results"`
	RateLimitProfile     string            `json:"rate_limit_profile"`
	CreatedAt            time.Time         `json:"created_at"`
	ProviderFailures     []ProviderFailure `json:"provider_failures,omitempty"`
}

type SourceResult struct {
	SourceID           string             `json:"source_id"`
	GlobalDedupKey     string             `json:"global_dedup_key"`
	SourceType         SourceType         `json:"source_type"`
	Provider           string             `json:"provider"`
	URL                *string            `json:"url,omitempty"`
	CanonicalURL       *string            `json:"canonical_url,omitempty"`
	Title              *string            `json:"title,omitempty"`
	Snippet            string             `json:"snippet"`
	FullText           *string            `json:"full_text,omitempty"`
	AuthorName         *string            `json:"author_name,omitempty"`
	AuthorHandle       *string            `json:"author_handle,omitempty"`
	AuthorURL          *string            `json:"author_url,omitempty"`
	PublishedAt        *time.Time         `json:"published_at,omitempty"`
	IndexedAt          *time.Time         `json:"indexed_at,omitempty"`
	ScrapedAt          time.Time          `json:"scraped_at"`
	Engagement         map[string]any     `json:"engagement"`
	SourceMetadata     map[string]any     `json:"source_metadata"`
	RawPayloadRef      *string            `json:"raw_payload_ref,omitempty"`
	QueryUsed          string             `json:"query_used"`
	HTTPStatus         *int               `json:"http_status,omitempty"`
	AvailabilityStatus AvailabilityStatus `json:"availability_status"`
	Error              *string            `json:"error,omitempty"`
}

type SourceResultSet struct {
	Plan             SourceSearchPlan  `json:"plan"`
	Results          []SourceResult    `json:"results"`
	ProviderFailures []ProviderFailure `json:"provider_failures,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type EnrichedSource struct {
	SourceResult          SourceResult `json:"source_result"`
	NormalizedText        string       `json:"normalized_text"`
	TextHash              string       `json:"text_hash"`
	Language              string       `json:"language"`
	Entities              []string     `json:"entities"`
	LinkedURLs            []string     `json:"linked_urls"`
	QuotedSources         []string     `json:"quoted_sources"`
	AccountReliability    float64      `json:"account_reliability"`
	TimestampConfidence   float64      `json:"timestamp_confidence"`
	ContentCompleteness   float64      `json:"content_completeness"`
	SourceConfidence      float64      `json:"source_confidence"`
	EnrichmentExplanation string       `json:"enrichment_explanation"`
}

type EnrichedSourceSet struct {
	SourceSet        SourceResultSet   `json:"source_set"`
	Sources          []EnrichedSource  `json:"sources"`
	ProviderFailures []ProviderFailure `json:"provider_failures,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type RankedResult struct {
	EnrichedSource     EnrichedSource `json:"enriched_source"`
	SimilarityScore    float64        `json:"similarity_score"`
	ChronologicalScore float64        `json:"chronological_score"`
	SourceConfidence   float64        `json:"source_confidence"`
	AIOriginSignal     float64        `json:"ai_origin_signal"`
	SpreadSignal       float64        `json:"spread_signal"`
	CompositeScore     float64        `json:"composite_score"`
	ChronologicalRank  int            `json:"chronological_rank"`
	IsCandidateOrigin  bool           `json:"is_candidate_origin"`
	RankingExplanation string         `json:"ranking_explanation"`
}

type AnalyzedSet struct {
	EnrichedSet      EnrichedSourceSet `json:"enriched_set"`
	RankedResults    []RankedResult    `json:"ranked_results"`
	ProviderFailures []ProviderFailure `json:"provider_failures,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type DetectionMethod struct {
	Name        string  `json:"name"`
	Score       float64 `json:"score"`
	Weight      float64 `json:"weight"`
	Succeeded   bool    `json:"succeeded"`
	Error       *string `json:"error,omitempty"`
	Explanation string  `json:"explanation"`
}

type AISignatureResult struct {
	RankedResult     RankedResult      `json:"ranked_result"`
	DetectionMethods []DetectionMethod `json:"detection_methods"`
	EnsembleScore    float64           `json:"ensemble_score"`
	IsAIGenerated    bool              `json:"is_ai_generated"`
	Confidence       float64           `json:"confidence"`
	Explanation      string            `json:"explanation"`
}

type AISignatureSet struct {
	AnalyzedSet      AnalyzedSet         `json:"analyzed_set"`
	Results          []AISignatureResult `json:"results"`
	ProviderFailures []ProviderFailure   `json:"provider_failures,omitempty"`
	CreatedAt        time.Time           `json:"created_at"`
}

type ExpertOpinion struct {
	Score            float64  `json:"score"`
	Confidence       float64  `json:"confidence"`
	KeyReasons       []string `json:"key_reasons"`
	Concerns         []string `json:"concerns"`
	RecommendedLabel string   `json:"recommended_label"`
}

type ExpertCommitteeReview struct {
	ReviewID             string                   `json:"review_id"`
	ReviewedCandidates   []string                 `json:"reviewed_candidates"`
	Experts              map[string]ExpertOpinion `json:"experts"`
	CommitteeScore       float64                  `json:"committee_score"`
	ConfidenceAdjustment float64                  `json:"confidence_adjustment"`
	RiskAdjustment       float64                  `json:"risk_adjustment"`
	ConsensusLabel       string                   `json:"consensus_label"`
	DissentingViews      []string                 `json:"dissenting_views"`
	Summary              string                   `json:"summary"`
	CreatedAt            time.Time                `json:"created_at"`
}

type ExpertReviewSet struct {
	AISignatureSet   AISignatureSet        `json:"ai_signature_set"`
	Review           ExpertCommitteeReview `json:"expert_committee_review"`
	ProviderFailures []ProviderFailure     `json:"provider_failures,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
}

type TimelineEntry struct {
	SourceID       string     `json:"source_id"`
	SourceType     SourceType `json:"source_type"`
	Provider       string     `json:"provider"`
	Title          *string    `json:"title,omitempty"`
	URL            *string    `json:"url,omitempty"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	IndexedAt      *time.Time `json:"indexed_at,omitempty"`
	CompositeScore float64    `json:"composite_score"`
	Explanation    string     `json:"explanation"`
}

type DecisionExplanation struct {
	SourceID            string `json:"source_id"`
	IncludedBecause     string `json:"included_because"`
	TimestampEvidence   string `json:"timestamp_evidence"`
	SemanticEvidence    string `json:"semantic_evidence"`
	Weaknesses          string `json:"weaknesses"`
	Role                string `json:"role"`
	SeriousnessEvidence string `json:"seriousness_evidence"`
}

type ProviderFailure struct {
	Provider string    `json:"provider"`
	Stage    Stage     `json:"stage"`
	Error    string    `json:"error"`
	At       time.Time `json:"at"`
}

type ProvenanceReport struct {
	ReportID                     string                `json:"report_id"`
	JobID                        string                `json:"job_id"`
	SourceItem                   NewsItem              `json:"source_item"`
	Status                       JobStatus             `json:"status"`
	CanonicalClaim               string                `json:"canonical_claim"`
	Timeline                     []TimelineEntry       `json:"timeline"`
	CandidateOrigins             []RankedResult        `json:"candidate_origins"`
	AISignatureResults           []AISignatureResult   `json:"ai_signature_results"`
	ExpertCommitteeReview        ExpertCommitteeReview `json:"expert_committee_review"`
	EarliestHighConfidenceSource *RankedResult         `json:"earliest_high_confidence_source,omitempty"`
	EarliestIndexedSource        *RankedResult         `json:"earliest_indexed_source,omitempty"`
	DisinformationRisk           float64               `json:"disinformation_risk"`
	RiskLabel                    string                `json:"risk_label"`
	Confidence                   float64               `json:"confidence"`
	SeverityExplanation          string                `json:"severity_explanation"`
	SourceDecisionExplanations   []DecisionExplanation `json:"source_decision_explanations"`
	ProviderFailures             []ProviderFailure     `json:"provider_failures"`
	PipelineVersion              string                `json:"pipeline_version"`
	GeneratedAt                  time.Time             `json:"generated_at"`
	ExpiresAt                    time.Time             `json:"expires_at"`
	TotalDurationSeconds         float64               `json:"total_duration_seconds"`
	Summary                      string                `json:"summary"`
}

type ReportListItem struct {
	ReportID       string    `json:"report_id"`
	JobID          string    `json:"job_id"`
	Status         JobStatus `json:"status"`
	CanonicalClaim string    `json:"canonical_claim"`
	RiskLabel      string    `json:"risk_label"`
	Confidence     float64   `json:"confidence"`
	Summary        string    `json:"summary"`
	GeneratedAt    time.Time `json:"generated_at"`
}

type ReportListResponse struct {
	Reports []ReportListItem `json:"reports"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

type EngineStatus string

const (
	EngineStatusActive           EngineStatus = "active"
	EngineStatusStopped          EngineStatus = "stopped"
	EngineStatusRunning          EngineStatus = "running"
	EngineStatusCompleted        EngineStatus = "completed"
	EngineStatusDegraded         EngineStatus = "degraded"
	EngineStatusInsufficientData EngineStatus = "insufficient_data"
	EngineStatusFailed           EngineStatus = "failed"
)

type ActorClass string

const (
	ActorClassBot     ActorClass = "Bot"
	ActorClassNonBot  ActorClass = "Non-Bot"
	ActorClassUnknown ActorClass = "Unknown"
)

type SourceAuthenticity string

const (
	SourceAuthenticityHuman     SourceAuthenticity = "Human"
	SourceAuthenticitySynthetic SourceAuthenticity = "Synthetic"
	SourceAuthenticityUnknown   SourceAuthenticity = "Unknown"
)

type TrendDirection string

const (
	TrendRising  TrendDirection = "rising"
	TrendFlat    TrendDirection = "flat"
	TrendFalling TrendDirection = "falling"
)

// AnalysisSettings tunes how harvested interactions and expert committee outputs are interpreted.
type AnalysisSettings struct {
	// IncludeMegaAccounts keeps major verified/publisher accounts in authenticity ratios and
	// top-source views instead of filtering them out as obvious organic noise.
	IncludeMegaAccounts bool `json:"include_mega_accounts,omitempty"`
	// PreferLowReachInteractions sorts interaction harvesting toward the least-followed accounts
	// replying/amplifying each tweet — the typical bot-coordination long tail.
	PreferLowReachInteractions bool `json:"prefer_low_reach_interactions,omitempty"`
	// AggressiveAIBiasCommittee pushes the expert committee and actor classifier toward labeling
	// engagement as bot/AI-driven when early synthetic-traffic signals are present.
	AggressiveAIBiasCommittee bool `json:"aggressive_ai_bias_committee,omitempty"`
}

type CampaignProfile struct {
	CampaignID        string           `json:"campaign_id"`
	ClientName        string           `json:"client_name"`
	ClientAliases     []string         `json:"client_aliases"`
	Industry          string           `json:"industry"`
	Region            string           `json:"region"`
	MonitoredTopics   []string         `json:"monitored_topics"`
	Opponents         []string         `json:"opponents,omitempty"`
	InterestGroups    []InterestGroup  `json:"interest_groups,omitempty"`
	ImportantAccounts []string         `json:"important_accounts,omitempty"`
	ClientAccounts    []string         `json:"client_accounts,omitempty"`
	TrustedSources    []string         `json:"trusted_sources,omitempty"`
	HostileSources    []string         `json:"known_hostile_sources,omitempty"`
	Languages         []string         `json:"languages"`
	CrawlBudget       CrawlBudget      `json:"crawl_budget"`
	AnalysisSettings  AnalysisSettings `json:"analysis_settings,omitempty"`
	Status            EngineStatus     `json:"status"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

type CampaignRequest struct {
	ClientName        string           `json:"client_name"`
	ClientAliases     []string         `json:"client_aliases"`
	Industry          string           `json:"industry"`
	Region            string           `json:"region"`
	MonitoredTopics   []string         `json:"monitored_topics"`
	Opponents         []string         `json:"opponents,omitempty"`
	InterestGroups    []InterestGroup  `json:"interest_groups,omitempty"`
	ImportantAccounts []string         `json:"important_accounts,omitempty"`
	ClientAccounts    []string         `json:"client_accounts,omitempty"`
	TrustedSources    []string         `json:"trusted_sources,omitempty"`
	HostileSources    []string         `json:"known_hostile_sources,omitempty"`
	Languages         []string         `json:"languages"`
	CrawlBudget       CrawlBudget      `json:"crawl_budget"`
	AnalysisSettings  AnalysisSettings `json:"analysis_settings,omitempty"`
}

type CrawlBudget struct {
	TopNarratives            int `json:"top_narratives"`
	InteractionsPerNarrative int `json:"interactions_per_narrative"`
	MaxCollectionResults     int `json:"max_collection_results"`
}

type InterestGroup struct {
	GroupID        string   `json:"group_id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Keywords       []string `json:"keywords"`
	Hashtags       []string `json:"hashtags"`
	Accounts       []string `json:"accounts"`
	Regions        []string `json:"regions"`
	Issues         []string `json:"issues"`
	RelevanceRules []string `json:"relevance_rules"`
	Priority       int      `json:"priority"`
}

type CrawlStatus struct {
	CampaignID       string            `json:"campaign_id"`
	Status           EngineStatus      `json:"status"`
	LastRunAt        *time.Time        `json:"last_run_at,omitempty"`
	LastCompletedAt  *time.Time        `json:"last_completed_at,omitempty"`
	NarrativesFound  int               `json:"narratives_found"`
	SourcesCollected int               `json:"sources_collected"`
	Interactions     int               `json:"interactions_collected"`
	ProviderFailures []ProviderFailure `json:"provider_failures,omitempty"`
	Message          string            `json:"message,omitempty"`
}

type CollectionTarget struct {
	CampaignID string   `json:"campaign_id"`
	Query      string   `json:"query"`
	Source     string   `json:"source"`
	MaxResults int      `json:"max_results"`
	Languages  []string `json:"languages,omitempty"`
	Region     string   `json:"region,omitempty"`
}

type SourceItem struct {
	SourceID           string             `json:"source_id"`
	CampaignID         string             `json:"campaign_id"`
	GlobalDedupKey     string             `json:"global_dedup_key"`
	SourceType         SourceType         `json:"source_type"`
	Provider           string             `json:"provider"`
	URL                *string            `json:"url,omitempty"`
	CanonicalURL       *string            `json:"canonical_url,omitempty"`
	Title              string             `json:"title"`
	Text               string             `json:"text"`
	Snippet            string             `json:"snippet"`
	Language           string             `json:"language"`
	Author             AccountProfile     `json:"author"`
	PublishedAt        *time.Time         `json:"published_at,omitempty"`
	IndexedAt          *time.Time         `json:"indexed_at,omitempty"`
	CollectedAt        time.Time          `json:"collected_at"`
	Engagement         map[string]any     `json:"engagement"`
	Interactions       []InteractionEvent `json:"interactions,omitempty"`
	LinkedURLs         []string           `json:"linked_urls"`
	MentionedEntities  []string           `json:"mentioned_entities"`
	Hashtags           []string           `json:"hashtags"`
	RawPayloadRef      *string            `json:"raw_payload_ref,omitempty"`
	AvailabilityStatus AvailabilityStatus `json:"availability_status"`
	CollectionQuery    string             `json:"collection_query"`
	Error              *string            `json:"error,omitempty"`
}

type AccountProfile struct {
	AccountID         string     `json:"account_id"`
	Platform          string     `json:"platform"`
	Handle            string     `json:"handle"`
	DisplayName       string     `json:"display_name"`
	ProfileURL        *string    `json:"profile_url,omitempty"`
	Bio               string     `json:"bio"`
	DeclaredLocation  string     `json:"declared_location"`
	InferredCountry   string     `json:"inferred_country"`
	InferredRegion    string     `json:"inferred_region"`
	GeoConfidence     float64    `json:"geo_confidence"`
	FollowersCount    int64      `json:"followers_count"`
	FollowingCount    int64      `json:"following_count"`
	CreatedAtPlatform *time.Time `json:"created_at_platform,omitempty"`
	Verified          bool       `json:"verified"`
	AccountType       string     `json:"account_type"`
	InfluenceScore    float64    `json:"influence_score"`
	BotLikelihood     float64    `json:"bot_likelihood"`
	CoordinationScore float64    `json:"coordination_score"`
	ReliabilityScore  float64    `json:"reliability_score"`
	KnownAffiliations []string   `json:"known_affiliations"`
	RawMetadataRef    *string    `json:"raw_metadata_ref,omitempty"`
}

type InteractionEvent struct {
	InteractionID      string               `json:"interaction_id"`
	SourceID           string               `json:"source_id"`
	AccountID          string               `json:"account_id"`
	InteractionType    string               `json:"interaction_type"`
	OccurredAt         time.Time            `json:"occurred_at"`
	EngagementSnapshot map[string]any       `json:"engagement_snapshot"`
	ImportanceScore    float64              `json:"importance_score"`
	Metadata           map[string]any       `json:"metadata"`
	ActorClass         ActorClass           `json:"actor_class,omitempty"`
	Classification     *ActorClassification `json:"classification,omitempty"`
}

type ActorClassification struct {
	ClassificationID string     `json:"classification_id"`
	CampaignID       string     `json:"campaign_id"`
	NarrativeID      string     `json:"narrative_id"`
	AccountID        string     `json:"account_id"`
	Class            ActorClass `json:"class"`
	BotScore         float64    `json:"bot_score"`
	Confidence       float64    `json:"confidence"`
	Evidence         []string   `json:"evidence"`
	CreatedAt        time.Time  `json:"created_at"`
}

type PrimarySourceAttribution struct {
	AttributionID string             `json:"attribution_id"`
	CampaignID    string             `json:"campaign_id"`
	NarrativeID   string             `json:"narrative_id"`
	SourceID      string             `json:"source_id"`
	AccountID     string             `json:"account_id"`
	SourceType    SourceAuthenticity `json:"source_type"`
	Confidence    float64            `json:"confidence"`
	Evidence      []string           `json:"evidence"`
	FirstSeenAt   *time.Time         `json:"first_seen_at,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
}

type NarrativeCluster struct {
	NarrativeID              string                    `json:"narrative_id"`
	CampaignID               string                    `json:"campaign_id"`
	Narrative                string                    `json:"narrative"`
	Summary                  string                    `json:"summary"`
	CanonicalClaims          []string                  `json:"canonical_claims"`
	SourceIDs                []string                  `json:"source_ids"`
	ImportantAccountIDs      []string                  `json:"important_account_ids"`
	InterestGroupMatches     []string                  `json:"interest_group_matches"`
	FirstSeenAt              *time.Time                `json:"first_seen_at,omitempty"`
	LastSeenAt               *time.Time                `json:"last_seen_at,omitempty"`
	GeoDistribution          map[string]float64        `json:"geo_distribution"`
	SentimentDistribution    map[string]float64        `json:"sentiment_distribution"`
	SourceMix                map[string]int            `json:"source_mix"`
	TotalInteractions        int                       `json:"total_interactions"`
	InteractionTarget        int                       `json:"interaction_target"`
	InsufficientDataReason   *string                   `json:"insufficient_data_reason,omitempty"`
	ReachEstimate            int64                     `json:"reach_estimate"`
	VelocityPerHour          float64                   `json:"velocity_per_hour"`
	Trend                    TrendDirection            `json:"trend"`
	PopularityScore          float64                   `json:"popularity_score"`
	OrganicProminenceScore   float64                   `json:"organic_prominence_score"`
	BotCoordinationRisk      float64                   `json:"bot_coordination_risk"`
	ForeignInfluenceRisk     float64                   `json:"foreign_influence_risk"`
	AIGenerationRisk         float64                   `json:"ai_generation_risk"`
	MisinformationRisk       float64                   `json:"misinformation_risk"`
	OverallRisk              float64                   `json:"overall_risk"`
	RiskLabel                string                    `json:"risk_label"`
	RelevanceScore           float64                   `json:"relevance_score"`
	AuthenticPercentage      float64                   `json:"authentic_percentage"`
	InauthenticPercentage    float64                   `json:"inauthentic_percentage"`
	UnknownPercentage        float64                   `json:"unknown_percentage"`
	PrimarySourceAttribution *PrimarySourceAttribution `json:"primary_source_attribution,omitempty"`
	TopSources               []SourcePopularity        `json:"top_sources"`
	RecommendedPRAction      string                    `json:"recommended_pr_action"`
	WhyItMatters             string                    `json:"why_it_matters"`
	CapitalLossEstimate      CapitalLossEstimate       `json:"capital_loss_estimate"`
	DecisionExplanation      string                    `json:"decision_explanation"`
	CommitteeVerdict         *CommitteeVerdict         `json:"committee_verdict,omitempty"`
	SpreadTimeline           []TimelineBucket          `json:"spread_timeline,omitempty"`
	InteractionBreakdown     map[string]int            `json:"interaction_breakdown,omitempty"`
	CreatedAt                time.Time                 `json:"created_at"`
	UpdatedAt                time.Time                 `json:"updated_at"`
}

type CapitalLossEstimate struct {
	Applies     bool    `json:"applies"`
	MinUSD      int64   `json:"min_usd"`
	MaxUSD      int64   `json:"max_usd"`
	ExpectedUSD int64   `json:"expected_usd"`
	Confidence  float64 `json:"confidence"`
	Source      string  `json:"source"`
	Explanation string  `json:"explanation"`
	Disclaimer  string  `json:"disclaimer"`
}

// ExpertAssessment is one member of the LLM committee weighing in on a narrative.
type ExpertAssessment struct {
	Expert     string  `json:"expert"`
	Opinion    string  `json:"opinion"`
	Severity   float64 `json:"severity"`
	Confidence float64 `json:"confidence"`
}

// CommitteeVerdict is the LLM committee's judgment on a candidate narrative: whether it is
// relevant and actionable for the campaign manager, whether it originates from the client's own
// camp (and should be filtered out), the estimated reputational/capital impact, and per-expert
// reasoning. Source is "gemini" for live LLM output or "heuristic" for the deterministic fallback.
type CommitteeVerdict struct {
	Relevant          bool                `json:"relevant"`
	RelevanceScore    float64             `json:"relevance_score"`
	InterestScore     float64             `json:"interest_score"`
	ImpactSummary     string              `json:"impact_summary"`
	AudienceEffect    string              `json:"audience_effect"`
	ClientOriginated  bool                `json:"client_originated"`
	OriginRationale   string              `json:"origin_rationale"`
	ConsensusLabel    string              `json:"consensus_label"`
	RecommendedAction string              `json:"recommended_action"`
	Experts           []ExpertAssessment  `json:"experts"`
	CapitalLoss       CapitalLossEstimate `json:"capital_loss"`
	Source            string              `json:"source"`
}

// TimelineBucket is one time slice of a narrative's spread, split by actor authenticity so the
// dashboard can chart organic vs bot/AI-driven activity over time.
type TimelineBucket struct {
	T           time.Time `json:"t"`
	Total       int       `json:"total"`
	Authentic   int       `json:"authentic"`
	Inauthentic int       `json:"inauthentic"`
	Unknown     int       `json:"unknown"`
	Reach       int64     `json:"reach"`
}

type SourcePopularity struct {
	SourceID          string             `json:"source_id"`
	AccountID         string             `json:"account_id"`
	Handle            string             `json:"handle"`
	DisplayName       string             `json:"display_name"`
	SourceType        SourceType         `json:"source_type"`
	PopularityScore   float64            `json:"popularity_score"`
	ReachEstimate     int64              `json:"reach_estimate"`
	InteractionCount  int                `json:"interaction_count"`
	AmplificationRole string             `json:"amplification_role"`
	ReliabilityScore  float64            `json:"reliability_score"`
	Authenticity      SourceAuthenticity `json:"authenticity"`
}

type NarrativeCard struct {
	NarrativeID            string                    `json:"narrative_id"`
	Narrative              string                    `json:"narrative"`
	Summary                string                    `json:"summary"`
	PopularityScore        float64                   `json:"popularity_score"`
	PopularityRank         int                       `json:"popularity_rank"`
	TotalInteractions      int                       `json:"total_interactions"`
	ReachEstimate          int64                     `json:"reach_estimate"`
	VelocityPerHour        float64                   `json:"velocity_per_hour"`
	Trend                  TrendDirection            `json:"trend"`
	AuthenticPercentage    float64                   `json:"authentic_percentage"`
	InauthenticPercentage  float64                   `json:"inauthentic_percentage"`
	UnknownPercentage      float64                   `json:"unknown_percentage"`
	PrimarySource          *PrimarySourceAttribution `json:"primary_source,omitempty"`
	PrimarySourceType      SourceAuthenticity        `json:"primary_source_type"`
	TopSources             []SourcePopularity        `json:"top_sources"`
	SourcePopularity       []SourcePopularity        `json:"source_popularity"`
	ImportantInteractors   []AccountProfile          `json:"important_interactors"`
	RecommendedPRAction    string                    `json:"recommended_pr_action"`
	WhyItMatters           string                    `json:"why_it_matters"`
	CapitalLossEstimate    CapitalLossEstimate       `json:"capital_loss_estimate"`
	DashboardPriority      float64                   `json:"dashboard_priority"`
	RelevanceScore         float64                   `json:"relevance_score"`
	ImpactSummary          string                    `json:"impact_summary"`
	OverallRisk            float64                   `json:"overall_risk"`
	RiskLabel              string                    `json:"risk_label"`
	BotCoordinationRisk    float64                   `json:"bot_coordination_risk"`
	AIGenerationRisk       float64                   `json:"ai_generation_risk"`
	CommitteeVerdict       *CommitteeVerdict         `json:"committee_verdict,omitempty"`
	SpreadTimeline         []TimelineBucket          `json:"spread_timeline,omitempty"`
	InteractionBreakdown   map[string]int            `json:"interaction_breakdown,omitempty"`
	Status                 EngineStatus              `json:"status"`
	InsufficientDataReason *string                   `json:"insufficient_data_reason,omitempty"`
}

type DashboardSnapshot struct {
	SnapshotID         string            `json:"snapshot_id"`
	CampaignID         string            `json:"campaign_id"`
	Status             EngineStatus      `json:"status"`
	GeneratedAt        time.Time         `json:"generated_at"`
	ExecutiveSummary   string            `json:"executive_summary"`
	Narratives         []NarrativeCard   `json:"narratives"`
	GeoSentiment       map[string]any    `json:"geo_sentiment"`
	ImportantUsers     []AccountProfile  `json:"important_users"`
	SourceCounts       map[string]int    `json:"source_counts"`
	ProviderFailures   []ProviderFailure `json:"provider_failures,omitempty"`
	RecommendedActions []string          `json:"recommended_actions"`
}

type Alert struct {
	AlertID           string     `json:"alert_id"`
	CampaignID        string     `json:"campaign_id"`
	NarrativeID       string     `json:"narrative_id"`
	AlertType         string     `json:"alert_type"`
	Severity          string     `json:"severity"`
	Title             string     `json:"title"`
	Summary           string     `json:"summary"`
	WhyNow            string     `json:"why_now"`
	Evidence          []string   `json:"evidence"`
	RecommendedAction string     `json:"recommended_action"`
	Confidence        float64    `json:"confidence"`
	CreatedAt         time.Time  `json:"created_at"`
	AcknowledgedAt    *time.Time `json:"acknowledged_at,omitempty"`
}

type DiscoveryRunResponse struct {
	CampaignID       string            `json:"campaign_id"`
	Status           EngineStatus      `json:"status"`
	NarrativesFound  int               `json:"narratives_found"`
	SourcesCollected int               `json:"sources_collected"`
	Interactions     int               `json:"interactions_collected"`
	ProviderFailures []ProviderFailure `json:"provider_failures,omitempty"`
	Message          string            `json:"message,omitempty"`
}
