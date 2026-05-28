CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS jobs (
    id UUID PRIMARY KEY,
    report_id UUID NOT NULL UNIQUE,
    status TEXT NOT NULL,
    current_stage TEXT NOT NULL,
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
    error TEXT,
    progress JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS reports (
    id UUID PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    canonical_claim TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    risk_label TEXT NOT NULL DEFAULT 'LOW',
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    report JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    generated_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS news_items (
    item_id UUID PRIMARY KEY,
    report_id UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    content_hash TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS permutation_sets (
    report_id UUID PRIMARY KEY REFERENCES reports(id) ON DELETE CASCADE,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS source_results (
    source_id UUID PRIMARY KEY,
    report_id UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    global_dedup_key TEXT NOT NULL,
    source_type TEXT NOT NULL,
    provider TEXT NOT NULL,
    canonical_url TEXT,
    author_handle TEXT,
    text_hash TEXT,
    published_at TIMESTAMPTZ,
    indexed_at TIMESTAMPTZ,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_source_results_report_id ON source_results(report_id);
CREATE INDEX IF NOT EXISTS idx_source_results_dedup_key ON source_results(global_dedup_key);
CREATE INDEX IF NOT EXISTS idx_source_results_source_type ON source_results(source_type);
CREATE INDEX IF NOT EXISTS idx_source_results_provider ON source_results(provider);
CREATE INDEX IF NOT EXISTS idx_source_results_published_at ON source_results(published_at);
CREATE INDEX IF NOT EXISTS idx_source_results_indexed_at ON source_results(indexed_at);
CREATE INDEX IF NOT EXISTS idx_source_results_canonical_url ON source_results(canonical_url);
CREATE INDEX IF NOT EXISTS idx_source_results_author_handle ON source_results(author_handle);
CREATE INDEX IF NOT EXISTS idx_source_results_text_hash ON source_results(text_hash);

CREATE TABLE IF NOT EXISTS source_raw_payloads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id UUID REFERENCES source_results(source_id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ranked_results (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    rank INTEGER NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ai_signature_results (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    report_id UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS expert_reviews (
    review_id UUID PRIMARY KEY,
    report_id UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS provider_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider TEXT NOT NULL,
    job_id UUID,
    report_id UUID,
    operation TEXT NOT NULL,
    estimated_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    success BOOLEAN NOT NULL,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rss_feeds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    url TEXT NOT NULL UNIQUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_polled_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS dedup_index (
    dedup_key TEXT PRIMARY KEY,
    key_type TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS campaigns (
    campaign_id UUID PRIMARY KEY,
    client_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    profile JSONB NOT NULL,
    last_run_at TIMESTAMPTZ,
    last_completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS interest_groups (
    group_id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS topic_seeds (
    seed_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    seed_type TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS crawl_plans (
    crawl_plan_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending',
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS campaign_source_items (
    source_id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    global_dedup_key TEXT NOT NULL,
    source_type TEXT NOT NULL,
    provider TEXT NOT NULL,
    author_account_id TEXT,
    canonical_url TEXT,
    published_at TIMESTAMPTZ,
    collected_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_campaign_source_campaign ON campaign_source_items(campaign_id);
CREATE INDEX IF NOT EXISTS idx_campaign_source_dedup ON campaign_source_items(global_dedup_key);
CREATE INDEX IF NOT EXISTS idx_campaign_source_provider ON campaign_source_items(provider);
CREATE INDEX IF NOT EXISTS idx_campaign_source_published ON campaign_source_items(published_at);

CREATE TABLE IF NOT EXISTS raw_payloads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id UUID REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    source_id UUID,
    account_id TEXT,
    provider TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS account_profiles (
    account_id TEXT PRIMARY KEY,
    platform TEXT NOT NULL,
    handle TEXT NOT NULL,
    payload JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_account_profiles_handle ON account_profiles(handle);

CREATE TABLE IF NOT EXISTS interaction_events (
    interaction_id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    account_id TEXT NOT NULL,
    interaction_type TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_interactions_campaign ON interaction_events(campaign_id);
CREATE INDEX IF NOT EXISTS idx_interactions_source ON interaction_events(source_id);
CREATE INDEX IF NOT EXISTS idx_interactions_account ON interaction_events(account_id);
CREATE INDEX IF NOT EXISTS idx_interactions_occurred ON interaction_events(occurred_at);

CREATE TABLE IF NOT EXISTS narrative_clusters (
    narrative_id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    narrative TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    popularity_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    authentic_percentage DOUBLE PRECISION NOT NULL DEFAULT 0,
    inauthentic_percentage DOUBLE PRECISION NOT NULL DEFAULT 0,
    reach_estimate BIGINT NOT NULL DEFAULT 0,
    velocity_per_hour DOUBLE PRECISION NOT NULL DEFAULT 0,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_narratives_campaign ON narrative_clusters(campaign_id);
CREATE INDEX IF NOT EXISTS idx_narratives_popularity ON narrative_clusters(popularity_score DESC);

CREATE TABLE IF NOT EXISTS narrative_sources (
    narrative_id UUID NOT NULL REFERENCES narrative_clusters(narrative_id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'mention',
    PRIMARY KEY (narrative_id, source_id)
);

CREATE TABLE IF NOT EXISTS narrative_interactions (
    narrative_id UUID NOT NULL REFERENCES narrative_clusters(narrative_id) ON DELETE CASCADE,
    interaction_id UUID NOT NULL REFERENCES interaction_events(interaction_id) ON DELETE CASCADE,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    PRIMARY KEY (narrative_id, interaction_id)
);

CREATE TABLE IF NOT EXISTS primary_source_attributions (
    attribution_id UUID PRIMARY KEY,
    narrative_id UUID NOT NULL REFERENCES narrative_clusters(narrative_id) ON DELETE CASCADE,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    account_id TEXT NOT NULL,
    source_type TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS actor_classifications (
    classification_id UUID PRIMARY KEY,
    narrative_id UUID NOT NULL REFERENCES narrative_clusters(narrative_id) ON DELETE CASCADE,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    account_id TEXT NOT NULL,
    class TEXT NOT NULL,
    bot_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (narrative_id, account_id)
);

CREATE TABLE IF NOT EXISTS sentiment_geo_points (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    narrative_id UUID REFERENCES narrative_clusters(narrative_id) ON DELETE CASCADE,
    country TEXT,
    region TEXT,
    sentiment TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS influence_edges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    narrative_id UUID REFERENCES narrative_clusters(narrative_id) ON DELETE CASCADE,
    from_account_id TEXT NOT NULL,
    to_account_id TEXT NOT NULL,
    edge_type TEXT NOT NULL,
    weight DOUBLE PRECISION NOT NULL DEFAULT 1,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS dashboard_snapshots (
    snapshot_id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    payload JSONB NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dashboard_campaign_generated ON dashboard_snapshots(campaign_id, generated_at DESC);

CREATE TABLE IF NOT EXISTS campaign_alerts (
    alert_id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL REFERENCES campaigns(campaign_id) ON DELETE CASCADE,
    narrative_id UUID REFERENCES narrative_clusters(narrative_id) ON DELETE CASCADE,
    alert_type TEXT NOT NULL,
    severity TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    acknowledged_at TIMESTAMPTZ
);
