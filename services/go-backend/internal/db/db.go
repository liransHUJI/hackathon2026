package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hnweb/provenance/internal/models"
)

type Store struct {
	pool *pgxpool.Pool
}

func Connect(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) RunMigrations(ctx context.Context) error {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join("migrations", entry.Name()))
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("run migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) CreateJob(ctx context.Context, job models.JobRecord) error {
	progress, err := json.Marshal(job.Progress)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO jobs (id, report_id, status, current_stage, progress, created_at, updated_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, job.ID, job.ReportID, job.Status, job.CurrentStage, progress, job.CreatedAt, job.UpdatedAt, job.ExpiresAt)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO reports (id, job_id, status, created_at, updated_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, job.ReportID, job.ID, job.Status, job.CreatedAt, job.UpdatedAt, job.ExpiresAt)
	return err
}

func (s *Store) GetJob(ctx context.Context, id string) (*models.JobRecord, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id::text, report_id::text, status, current_stage, cancel_requested, error, progress,
		       created_at, updated_at, completed_at, expires_at
		FROM jobs WHERE id = $1
	`, id)
	return scanJob(row)
}

func (s *Store) MarkJobStage(ctx context.Context, jobID string, status models.JobStatus, stage models.Stage, progress models.JobProgress) error {
	progressBytes, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE jobs
		SET status = $2, current_stage = $3, progress = $4, updated_at = now()
		WHERE id = $1
	`, jobID, status, stage, progressBytes)
	return err
}

func (s *Store) CompleteJob(ctx context.Context, jobID string, status models.JobStatus) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs
		SET status = $2, updated_at = now(), completed_at = now()
		WHERE id = $1
	`, jobID, status)
	return err
}

func (s *Store) FailJob(ctx context.Context, jobID string, status models.JobStatus, jobErr error) error {
	var msg *string
	if jobErr != nil {
		text := jobErr.Error()
		msg = &text
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs
		SET status = $2, error = $3, updated_at = now(), completed_at = now()
		WHERE id = $1
	`, jobID, status, msg)
	return err
}

func (s *Store) RequestCancel(ctx context.Context, jobID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE jobs
		SET cancel_requested = TRUE, status = CASE WHEN status = 'completed' THEN status ELSE 'cancelled' END,
		    updated_at = now()
		WHERE id = $1
	`, jobID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *Store) IsCancelled(ctx context.Context, jobID string) (bool, error) {
	var cancelled bool
	err := s.pool.QueryRow(ctx, `SELECT cancel_requested FROM jobs WHERE id = $1`, jobID).Scan(&cancelled)
	return cancelled, err
}

func (s *Store) SaveNewsItem(ctx context.Context, item models.NewsItem) error {
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO news_items (item_id, report_id, content_hash, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (item_id) DO UPDATE SET payload = EXCLUDED.payload
	`, item.ItemID, item.ReportID, item.ContentHash, payload)
	return err
}

func (s *Store) SavePermutationSet(ctx context.Context, reportID string, set models.PermutationSet) error {
	return s.upsertJSON(ctx, "permutation_sets", "report_id", reportID, set)
}

func (s *Store) SaveSourceResults(ctx context.Context, reportID string, results []models.SourceResult) error {
	for _, result := range results {
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		textHash := ""
		if result.FullText != nil {
			textHash = hashString(*result.FullText)
		}
		_, err = s.pool.Exec(ctx, `
			INSERT INTO source_results (
				source_id, report_id, global_dedup_key, source_type, provider, canonical_url,
				author_handle, text_hash, published_at, indexed_at, payload
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (source_id) DO UPDATE SET payload = EXCLUDED.payload
		`, result.SourceID, reportID, result.GlobalDedupKey, result.SourceType, result.Provider,
			result.CanonicalURL, result.AuthorHandle, textHash, result.PublishedAt, result.IndexedAt, payload)
		if err != nil {
			return err
		}
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO dedup_index (dedup_key, key_type, last_seen_at)
			VALUES ($1, 'source', now())
			ON CONFLICT (dedup_key) DO UPDATE SET last_seen_at = now()
		`, result.GlobalDedupKey)
	}
	return nil
}

func (s *Store) SaveRankedResults(ctx context.Context, reportID string, results []models.RankedResult) error {
	for idx, result := range results {
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, `
			INSERT INTO ranked_results (report_id, source_id, rank, payload)
			VALUES ($1, $2, $3, $4)
		`, reportID, result.EnrichedSource.SourceResult.SourceID, idx+1, payload)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveAIResults(ctx context.Context, reportID string, results []models.AISignatureResult) error {
	for _, result := range results {
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, `
			INSERT INTO ai_signature_results (report_id, source_id, payload)
			VALUES ($1, $2, $3)
		`, reportID, result.RankedResult.EnrichedSource.SourceResult.SourceID, payload)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveExpertReview(ctx context.Context, reportID string, review models.ExpertCommitteeReview) error {
	payload, err := json.Marshal(review)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO expert_reviews (review_id, report_id, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (review_id) DO UPDATE SET payload = EXCLUDED.payload
	`, review.ReviewID, reportID, payload)
	return err
}

func (s *Store) SaveReport(ctx context.Context, report models.ProvenanceReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE reports
		SET status = $2, canonical_claim = $3, summary = $4, risk_label = $5, confidence = $6,
		    report = $7, updated_at = now(), generated_at = $8
		WHERE id = $1
	`, report.ReportID, report.Status, report.CanonicalClaim, report.Summary, report.RiskLabel,
		report.Confidence, payload, report.GeneratedAt)
	return err
}

func (s *Store) GetReport(ctx context.Context, reportID string) (*models.ProvenanceReport, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT report FROM reports WHERE id = $1`, reportID).Scan(&payload)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || string(payload) == "{}" {
		return nil, pgx.ErrNoRows
	}
	var report models.ProvenanceReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

func (s *Store) ListReports(ctx context.Context, query, status string, limit, offset int) ([]models.ReportListItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, job_id::text, status, canonical_claim, risk_label, confidence, summary,
		       COALESCE(generated_at, updated_at)
		FROM reports
		WHERE ($1 = '' OR canonical_claim ILIKE '%' || $1 || '%' OR summary ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR status = $2)
		ORDER BY updated_at DESC
		LIMIT $3 OFFSET $4
	`, query, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.ReportListItem
	for rows.Next() {
		var item models.ReportListItem
		if err := rows.Scan(&item.ReportID, &item.JobID, &item.Status, &item.CanonicalClaim,
			&item.RiskLabel, &item.Confidence, &item.Summary, &item.GeneratedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RecordProviderUsage(ctx context.Context, provider, jobID, reportID, operation string, cost float64, success bool, providerErr error) {
	var msg *string
	if providerErr != nil {
		text := providerErr.Error()
		msg = &text
	}
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO provider_usage (provider, job_id, report_id, operation, estimated_cost_usd, success, error)
		VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, $6, $7)
	`, provider, jobID, reportID, operation, cost, success, msg)
}

func (s *Store) CleanupExpired(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE expires_at < now()`)
	return err
}

func (s *Store) upsertJSON(ctx context.Context, table, idColumn, id string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s (%s, payload)
		VALUES ($1, $2)
		ON CONFLICT (%s) DO UPDATE SET payload = EXCLUDED.payload
	`, table, idColumn, idColumn), id, payload)
	return err
}

func scanJob(row pgx.Row) (*models.JobRecord, error) {
	var job models.JobRecord
	var progress []byte
	err := row.Scan(&job.ID, &job.ReportID, &job.Status, &job.CurrentStage, &job.CancelRequested,
		&job.Error, &progress, &job.CreatedAt, &job.UpdatedAt, &job.CompletedAt, &job.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if len(progress) > 0 {
		if err := json.Unmarshal(progress, &job.Progress); err != nil {
			return nil, err
		}
	}
	return &job, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func (s *Store) UpsertCampaign(ctx context.Context, campaign models.CampaignProfile) error {
	payload, err := json.Marshal(campaign)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO campaigns (campaign_id, client_name, status, profile, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (campaign_id) DO UPDATE SET
			client_name = EXCLUDED.client_name,
			status = EXCLUDED.status,
			profile = EXCLUDED.profile,
			updated_at = now()
	`, campaign.CampaignID, campaign.ClientName, campaign.Status, payload, campaign.CreatedAt, campaign.UpdatedAt)
	if err != nil {
		return err
	}
	for _, group := range campaign.InterestGroups {
		if err := s.UpsertInterestGroup(ctx, campaign.CampaignID, group); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetCampaign(ctx context.Context, campaignID string) (*models.CampaignProfile, error) {
	row := s.pool.QueryRow(ctx, `SELECT profile FROM campaigns WHERE campaign_id = $1`, campaignID)
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return nil, err
	}
	var campaign models.CampaignProfile
	if err := json.Unmarshal(payload, &campaign); err != nil {
		return nil, err
	}
	return &campaign, nil
}

func (s *Store) ListCampaigns(ctx context.Context) ([]models.CampaignProfile, error) {
	rows, err := s.pool.Query(ctx, `SELECT profile FROM campaigns ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	campaigns := []models.CampaignProfile{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var campaign models.CampaignProfile
		if err := json.Unmarshal(payload, &campaign); err != nil {
			return nil, err
		}
		campaigns = append(campaigns, campaign)
	}
	return campaigns, rows.Err()
}

func (s *Store) UpsertInterestGroup(ctx context.Context, campaignID string, group models.InterestGroup) error {
	payload, err := json.Marshal(group)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO interest_groups (group_id, campaign_id, name, payload, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())
		ON CONFLICT (group_id) DO UPDATE SET
			name = EXCLUDED.name,
			payload = EXCLUDED.payload,
			updated_at = now()
	`, group.GroupID, campaignID, group.Name, payload)
	return err
}

func (s *Store) ListInterestGroups(ctx context.Context, campaignID string) ([]models.InterestGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM interest_groups WHERE campaign_id = $1 ORDER BY name`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []models.InterestGroup{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var group models.InterestGroup
		if err := json.Unmarshal(payload, &group); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *Store) MarkCampaignRunStarted(ctx context.Context, campaignID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE campaigns SET status = 'running', last_run_at = now(), updated_at = now()
		WHERE campaign_id = $1
	`, campaignID)
	return err
}

func (s *Store) MarkCampaignRunCompleted(ctx context.Context, campaignID string, status models.EngineStatus) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE campaigns SET status = $2, last_completed_at = now(), updated_at = now()
		WHERE campaign_id = $1
	`, campaignID, status)
	return err
}

func (s *Store) SaveSourceItems(ctx context.Context, campaignID string, items []models.SourceItem) error {
	for _, item := range items {
		payload, err := json.Marshal(item)
		if err != nil {
			return err
		}
		authorID := item.Author.AccountID
		_, err = s.pool.Exec(ctx, `
			INSERT INTO campaign_source_items (
				source_id, campaign_id, global_dedup_key, source_type, provider,
				author_account_id, canonical_url, published_at, collected_at, payload
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (source_id) DO UPDATE SET payload = EXCLUDED.payload
		`, item.SourceID, campaignID, item.GlobalDedupKey, item.SourceType, item.Provider,
			authorID, item.CanonicalURL, item.PublishedAt, item.CollectedAt, payload)
		if err != nil {
			return err
		}
		if item.Author.AccountID != "" {
			if err := s.UpsertAccountProfile(ctx, item.Author); err != nil {
				return err
			}
		}
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO dedup_index (dedup_key, key_type, last_seen_at)
			VALUES ($1, 'campaign_source', now())
			ON CONFLICT (dedup_key) DO UPDATE SET last_seen_at = now()
		`, item.GlobalDedupKey)
	}
	return nil
}

func (s *Store) UpsertAccountProfile(ctx context.Context, account models.AccountProfile) error {
	if account.AccountID == "" {
		return nil
	}
	payload, err := json.Marshal(account)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO account_profiles (account_id, platform, handle, payload, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (account_id) DO UPDATE SET
			platform = EXCLUDED.platform,
			handle = EXCLUDED.handle,
			payload = EXCLUDED.payload,
			updated_at = now()
	`, account.AccountID, account.Platform, account.Handle, payload)
	return err
}

func (s *Store) SaveInteractions(ctx context.Context, campaignID string, narrativeID string, interactions []models.InteractionEvent) error {
	for _, interaction := range interactions {
		payload, err := json.Marshal(interaction)
		if err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, `
			INSERT INTO interaction_events (
				interaction_id, campaign_id, source_id, account_id, interaction_type, occurred_at, payload
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (interaction_id) DO UPDATE SET payload = EXCLUDED.payload
		`, interaction.InteractionID, campaignID, interaction.SourceID, interaction.AccountID,
			interaction.InteractionType, interaction.OccurredAt, payload)
		if err != nil {
			return err
		}
		if narrativeID != "" {
			_, _ = s.pool.Exec(ctx, `
				INSERT INTO narrative_interactions (narrative_id, interaction_id, campaign_id)
				VALUES ($1,$2,$3)
				ON CONFLICT DO NOTHING
			`, narrativeID, interaction.InteractionID, campaignID)
		}
	}
	return nil
}

func (s *Store) SaveNarrative(ctx context.Context, narrative models.NarrativeCluster) error {
	payload, err := json.Marshal(narrative)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO narrative_clusters (
			narrative_id, campaign_id, narrative, summary, status, popularity_score,
			authentic_percentage, inauthentic_percentage, reach_estimate, velocity_per_hour,
			payload, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,'active',$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (narrative_id) DO UPDATE SET
			narrative = EXCLUDED.narrative,
			summary = EXCLUDED.summary,
			popularity_score = EXCLUDED.popularity_score,
			authentic_percentage = EXCLUDED.authentic_percentage,
			inauthentic_percentage = EXCLUDED.inauthentic_percentage,
			reach_estimate = EXCLUDED.reach_estimate,
			velocity_per_hour = EXCLUDED.velocity_per_hour,
			payload = EXCLUDED.payload,
			updated_at = now()
	`, narrative.NarrativeID, narrative.CampaignID, narrative.Narrative, narrative.Summary,
		narrative.PopularityScore, narrative.AuthenticPercentage, narrative.InauthenticPercentage,
		narrative.ReachEstimate, narrative.VelocityPerHour, payload, narrative.CreatedAt, narrative.UpdatedAt)
	if err != nil {
		return err
	}
	for _, sourceID := range narrative.SourceIDs {
		_, _ = s.pool.Exec(ctx, `
			INSERT INTO narrative_sources (narrative_id, source_id, campaign_id, role)
			VALUES ($1,$2,$3,'mention')
			ON CONFLICT DO NOTHING
		`, narrative.NarrativeID, sourceID, narrative.CampaignID)
	}
	if narrative.PrimarySourceAttribution != nil {
		if err := s.SavePrimarySourceAttribution(ctx, *narrative.PrimarySourceAttribution); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListNarratives(ctx context.Context, campaignID string, limit, offset int) ([]models.NarrativeCluster, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT payload FROM narrative_clusters
		WHERE campaign_id = $1
		ORDER BY popularity_score DESC, updated_at DESC
		LIMIT $2 OFFSET $3
	`, campaignID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	narratives := []models.NarrativeCluster{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var narrative models.NarrativeCluster
		if err := json.Unmarshal(payload, &narrative); err != nil {
			return nil, err
		}
		narratives = append(narratives, narrative)
	}
	return narratives, rows.Err()
}

func (s *Store) GetNarrative(ctx context.Context, narrativeID string) (*models.NarrativeCluster, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM narrative_clusters WHERE narrative_id = $1`, narrativeID).Scan(&payload)
	if err != nil {
		return nil, err
	}
	var narrative models.NarrativeCluster
	if err := json.Unmarshal(payload, &narrative); err != nil {
		return nil, err
	}
	return &narrative, nil
}

func (s *Store) SavePrimarySourceAttribution(ctx context.Context, attribution models.PrimarySourceAttribution) error {
	payload, err := json.Marshal(attribution)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO primary_source_attributions (
			attribution_id, narrative_id, campaign_id, source_id, account_id, source_type, confidence, payload
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (attribution_id) DO UPDATE SET payload = EXCLUDED.payload
	`, attribution.AttributionID, attribution.NarrativeID, attribution.CampaignID,
		attribution.SourceID, attribution.AccountID, attribution.SourceType, attribution.Confidence, payload)
	return err
}

func (s *Store) SaveActorClassifications(ctx context.Context, classifications []models.ActorClassification) error {
	for _, classification := range classifications {
		payload, err := json.Marshal(classification)
		if err != nil {
			return err
		}
		_, err = s.pool.Exec(ctx, `
			INSERT INTO actor_classifications (
				classification_id, narrative_id, campaign_id, account_id, class, bot_score, confidence, payload
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (narrative_id, account_id) DO UPDATE SET
				class = EXCLUDED.class,
				bot_score = EXCLUDED.bot_score,
				confidence = EXCLUDED.confidence,
				payload = EXCLUDED.payload
		`, classification.ClassificationID, classification.NarrativeID, classification.CampaignID,
			classification.AccountID, classification.Class, classification.BotScore, classification.Confidence, payload)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveDashboardSnapshot(ctx context.Context, snapshot models.DashboardSnapshot) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO dashboard_snapshots (snapshot_id, campaign_id, status, payload, generated_at)
		VALUES ($1,$2,$3,$4,$5)
	`, snapshot.SnapshotID, snapshot.CampaignID, snapshot.Status, payload, snapshot.GeneratedAt)
	return err
}

func (s *Store) LatestDashboardSnapshot(ctx context.Context, campaignID string) (*models.DashboardSnapshot, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `
		SELECT payload FROM dashboard_snapshots
		WHERE campaign_id = $1
		ORDER BY generated_at DESC
		LIMIT 1
	`, campaignID).Scan(&payload)
	if err != nil {
		return nil, err
	}
	var snapshot models.DashboardSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) SaveAlert(ctx context.Context, alert models.Alert) error {
	payload, err := json.Marshal(alert)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO campaign_alerts (alert_id, campaign_id, narrative_id, alert_type, severity, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (alert_id) DO UPDATE SET payload = EXCLUDED.payload
	`, alert.AlertID, alert.CampaignID, alert.NarrativeID, alert.AlertType, alert.Severity, payload, alert.CreatedAt)
	return err
}

func (s *Store) ListAlerts(ctx context.Context, campaignID string) ([]models.Alert, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT payload FROM campaign_alerts
		WHERE campaign_id = $1
		ORDER BY created_at DESC
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := []models.Alert{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var alert models.Alert
		if err := json.Unmarshal(payload, &alert); err != nil {
			return nil, err
		}
		alerts = append(alerts, alert)
	}
	return alerts, rows.Err()
}

func (s *Store) ListNarrativeSources(ctx context.Context, narrativeID string) ([]models.SourceItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT csi.payload
		FROM campaign_source_items csi
		JOIN narrative_sources ns ON ns.source_id = csi.source_id
		WHERE ns.narrative_id = $1
		ORDER BY csi.published_at NULLS LAST, csi.collected_at
	`, narrativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []models.SourceItem{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item models.SourceItem
		if err := json.Unmarshal(payload, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListNarrativeInteractions(ctx context.Context, narrativeID string, limit, offset int) ([]models.InteractionEvent, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ie.payload
		FROM interaction_events ie
		JOIN narrative_interactions ni ON ni.interaction_id = ie.interaction_id
		WHERE ni.narrative_id = $1
		ORDER BY ie.occurred_at
		LIMIT $2 OFFSET $3
	`, narrativeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []models.InteractionEvent{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var event models.InteractionEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ListNarrativeClassifications(ctx context.Context, narrativeID string, limit, offset int) ([]models.ActorClassification, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT payload FROM actor_classifications
		WHERE narrative_id = $1
		ORDER BY bot_score DESC
		LIMIT $2 OFFSET $3
	`, narrativeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []models.ActorClassification{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item models.ActorClassification
		if err := json.Unmarshal(payload, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) AcknowledgeAlert(ctx context.Context, alertID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE campaign_alerts SET acknowledged_at = now()
		WHERE alert_id = $1
	`, alertID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
