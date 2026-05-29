package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/config"
	"github.com/hnweb/provenance/internal/db"
	"github.com/hnweb/provenance/internal/engine"
	"github.com/hnweb/provenance/internal/models"
	js "github.com/hnweb/provenance/internal/nats"
	"github.com/hnweb/provenance/internal/pipeline"
)

type Server struct {
	cfg    config.Config
	store  *db.Store
	nats   *js.Client
	engine *engine.Engine
	logger *slog.Logger
	mux    *http.ServeMux
}

func New(cfg config.Config, store *db.Store, nats *js.Client, engine *engine.Engine, logger *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: store, nats: nats, engine: engine, logger: logger, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("POST /v1/reports", s.submitReport)
	s.mux.HandleFunc("POST /v1/reports/bulk", s.submitBulk)
	s.mux.HandleFunc("GET /v1/reports", s.listReports)
	s.mux.HandleFunc("GET /v1/reports/{report_id}", s.getReport)
	s.mux.HandleFunc("GET /v1/jobs/{job_id}", s.getJob)
	s.mux.HandleFunc("POST /v1/jobs/{job_id}/cancel", s.cancelJob)
	s.mux.HandleFunc("POST /v1/campaigns", s.createCampaign)
	s.mux.HandleFunc("GET /v1/campaigns", s.listCampaigns)
	s.mux.HandleFunc("GET /v1/campaigns/{campaign_id}", s.getCampaign)
	s.mux.HandleFunc("PUT /v1/campaigns/{campaign_id}", s.updateCampaign)
	s.mux.HandleFunc("GET /v1/campaigns/{campaign_id}/interest-groups", s.listInterestGroups)
	s.mux.HandleFunc("POST /v1/campaigns/{campaign_id}/interest-groups", s.createInterestGroup)
	s.mux.HandleFunc("POST /v1/campaigns/{campaign_id}/crawl/start", s.startCrawl)
	s.mux.HandleFunc("POST /v1/campaigns/{campaign_id}/crawl/stop", s.stopCrawl)
	s.mux.HandleFunc("POST /v1/campaigns/{campaign_id}/crawl/run-once", s.runOnce)
	s.mux.HandleFunc("GET /v1/campaigns/{campaign_id}/crawl/status", s.crawlStatus)
	s.mux.HandleFunc("GET /v1/campaigns/{campaign_id}/dashboard", s.getDashboard)
	s.mux.HandleFunc("GET /v1/campaigns/{campaign_id}/narratives", s.listNarratives)
	s.mux.HandleFunc("GET /v1/narratives/{narrative_id}", s.getNarrative)
	s.mux.HandleFunc("GET /v1/narratives/{narrative_id}/sources", s.getNarrativeSources)
	s.mux.HandleFunc("GET /v1/narratives/{narrative_id}/interactions", s.getNarrativeInteractions)
	s.mux.HandleFunc("GET /v1/narratives/{narrative_id}/actors", s.getNarrativeActors)
	s.mux.HandleFunc("GET /v1/campaigns/{campaign_id}/alerts", s.listAlerts)
	s.mux.HandleFunc("POST /v1/alerts/{alert_id}/ack", s.ackAlert)
	s.mux.Handle("GET /", http.FileServer(http.Dir(staticDir())))
}

func staticDir() string {
	candidates := []string{
		"public",
		filepath.Join("..", "..", "public"),
		filepath.Join("..", "public"),
		filepath.Join("services", "go-backend", "public"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return candidate
		}
	}
	return "public"
}

func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	var req models.CampaignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	campaign, err := s.engine.CreateCampaign(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, campaign)
}

func (s *Server) listCampaigns(w http.ResponseWriter, r *http.Request) {
	campaigns, err := s.engine.ListCampaigns(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": campaigns})
}

func (s *Server) getCampaign(w http.ResponseWriter, r *http.Request) {
	campaign, err := s.store.GetCampaign(r.Context(), r.PathValue("campaign_id"))
	if err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, campaign)
}

func (s *Server) updateCampaign(w http.ResponseWriter, r *http.Request) {
	var req models.CampaignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	campaign, err := s.engine.UpdateCampaign(r.Context(), r.PathValue("campaign_id"), req)
	if err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, campaign)
}

func (s *Server) listInterestGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListInterestGroups(r.Context(), r.PathValue("campaign_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interest_groups": groups})
}

func (s *Server) createInterestGroup(w http.ResponseWriter, r *http.Request) {
	campaignID := r.PathValue("campaign_id")
	var group models.InterestGroup
	if err := json.NewDecoder(r.Body).Decode(&group); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if group.GroupID == "" {
		group.GroupID = uuid.NewString()
	}
	if err := s.store.UpsertInterestGroup(r.Context(), campaignID, group); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

func (s *Server) startCrawl(w http.ResponseWriter, r *http.Request) {
	campaign, err := s.store.GetCampaign(r.Context(), r.PathValue("campaign_id"))
	if err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	campaign.Status = models.EngineStatusActive
	campaign.UpdatedAt = time.Now().UTC()
	if err := s.store.UpsertCampaign(r.Context(), *campaign); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign_id": campaign.CampaignID, "status": campaign.Status})
}

func (s *Server) stopCrawl(w http.ResponseWriter, r *http.Request) {
	campaign, err := s.store.GetCampaign(r.Context(), r.PathValue("campaign_id"))
	if err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	campaign.Status = models.EngineStatusStopped
	campaign.UpdatedAt = time.Now().UTC()
	if err := s.store.UpsertCampaign(r.Context(), *campaign); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaign_id": campaign.CampaignID, "status": campaign.Status})
}

func (s *Server) runOnce(w http.ResponseWriter, r *http.Request) {
	campaignID := r.PathValue("campaign_id")
	campaign, err := s.store.GetCampaign(r.Context(), campaignID)
	if err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	// Mark running immediately so the dashboard/status reflect the in-flight crawl, then run the
	// (minutes-long) live provider discovery in the background to avoid HTTP timeouts.
	campaign.Status = models.EngineStatusRunning
	campaign.UpdatedAt = time.Now().UTC()
	if err := s.store.UpsertCampaign(r.Context(), *campaign); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		if _, err := s.engine.RunDiscovery(ctx, campaignID); err != nil {
			s.logger.Error("background discovery run failed", "campaign_id", campaignID, "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, models.DiscoveryRunResponse{
		CampaignID: campaignID,
		Status:     models.EngineStatusRunning,
		Message:    "discovery started in the background",
	})
}

func (s *Server) crawlStatus(w http.ResponseWriter, r *http.Request) {
	campaign, err := s.store.GetCampaign(r.Context(), r.PathValue("campaign_id"))
	if err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	narratives, _ := s.store.ListNarratives(r.Context(), campaign.CampaignID, 100, 0)
	interactions := 0
	for _, narrative := range narratives {
		interactions += narrative.TotalInteractions
	}
	writeJSON(w, http.StatusOK, models.CrawlStatus{
		CampaignID:      campaign.CampaignID,
		Status:          campaign.Status,
		NarrativesFound: len(narratives),
		Interactions:    interactions,
		Message:         "live provider crawl status",
	})
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.store.LatestDashboardSnapshot(r.Context(), r.PathValue("campaign_id"))
	if err != nil {
		status := notFoundStatus(err)
		if status == http.StatusNotFound {
			writeJSON(w, http.StatusOK, models.DashboardSnapshot{
				SnapshotID:       uuid.NewString(),
				CampaignID:       r.PathValue("campaign_id"),
				Status:           models.EngineStatusInsufficientData,
				GeneratedAt:      time.Now().UTC(),
				ExecutiveSummary: "No live provider data has been collected yet.",
				Narratives:       []models.NarrativeCard{},
				ProviderFailures: []models.ProviderFailure{},
			})
			return
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) listNarratives(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	narratives, err := s.store.ListNarratives(r.Context(), r.PathValue("campaign_id"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"narratives": narratives})
}

func (s *Server) getNarrative(w http.ResponseWriter, r *http.Request) {
	narrativeID := r.PathValue("narrative_id")
	narrative, err := s.store.GetNarrative(r.Context(), narrativeID)
	if err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	sources, _ := s.store.ListNarrativeSources(r.Context(), narrativeID)
	classifications, _ := s.store.ListNarrativeClassifications(r.Context(), narrativeID, 2000, 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"narrative":             narrative,
		"sources":               sources,
		"actor_classifications": classifications,
	})
}

func (s *Server) getNarrativeSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.store.ListNarrativeSources(r.Context(), r.PathValue("narrative_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
}

func (s *Server) getNarrativeInteractions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	interactions, err := s.store.ListNarrativeInteractions(r.Context(), r.PathValue("narrative_id"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interactions": interactions})
}

func (s *Server) getNarrativeActors(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	classifications, err := s.store.ListNarrativeClassifications(r.Context(), r.PathValue("narrative_id"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actors": classifications})
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	alerts, err := s.store.ListAlerts(r.Context(), r.PathValue("campaign_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request) {
	if err := s.store.AcknowledgeAlert(r.Context(), r.PathValue("alert_id")); err != nil {
		writeError(w, notFoundStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert_id": r.PathValue("alert_id"), "acknowledged": true})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	if err := s.nats.Ping(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) submitReport(w http.ResponseWriter, r *http.Request) {
	var req models.SubmitReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resp, err := s.createAndEnqueue(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) submitBulk(w http.ResponseWriter, r *http.Request) {
	var req models.BulkSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("items are required"))
		return
	}
	resp := models.BulkSubmitResponse{Jobs: make([]models.SubmitReportResponse, 0, len(req.Items))}
	for _, item := range req.Items {
		if item.Priority == "" {
			item.Priority = req.Priority
		}
		job, err := s.createAndEnqueue(r.Context(), item)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		resp.Jobs = append(resp.Jobs, job)
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) createAndEnqueue(ctx context.Context, req models.SubmitReportRequest) (models.SubmitReportResponse, error) {
	if strings.TrimSpace(req.Input) == "" {
		return models.SubmitReportResponse{}, errors.New("input is required")
	}
	jobID := uuid.NewString()
	reportID := uuid.NewString()
	now := time.Now().UTC()
	job := models.JobRecord{
		ID:           jobID,
		ReportID:     reportID,
		Status:       models.JobStatusQueued,
		CurrentStage: models.StageNormalize,
		Progress:     pipeline.Progress(models.StageNormalize, 0, 0),
		CreatedAt:    now,
		UpdatedAt:    now,
		ExpiresAt:    now.AddDate(0, 0, s.cfg.RetentionDays),
	}
	if err := s.store.CreateJob(ctx, job); err != nil {
		return models.SubmitReportResponse{}, err
	}
	env := pipeline.NewEnvelope(jobID, reportID, "", models.StageNormalize, req)
	if err := s.nats.PublishJSON(ctx, js.SubjectNormalize, env); err != nil {
		_ = s.store.FailJob(ctx, jobID, models.JobStatusFailed, err)
		return models.SubmitReportResponse{}, err
	}
	return models.SubmitReportResponse{ReportID: reportID, JobID: jobID, Status: models.JobStatusQueued}, nil
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		status := http.StatusInternalServerError
		if db.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	if err := s.store.RequestCancel(r.Context(), jobID); err != nil {
		status := http.StatusInternalServerError
		if db.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	_ = s.nats.PublishJSON(r.Context(), js.SubjectCancel, map[string]string{"job_id": jobID})
	writeJSON(w, http.StatusOK, map[string]any{"job_id": jobID, "status": models.JobStatusCancelled})
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	report, err := s.store.GetReport(r.Context(), r.PathValue("report_id"))
	if err != nil {
		status := http.StatusInternalServerError
		if db.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	status := r.URL.Query().Get("status")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, err := s.store.ListReports(r.Context(), query, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if limit <= 0 {
		limit = 50
	}
	writeJSON(w, http.StatusOK, models.ReportListResponse{Reports: items, Limit: limit, Offset: offset})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error(), "status": status})
}

func notFoundStatus(err error) int {
	if db.IsNotFound(err) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}
