package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hnweb/provenance/internal/models"
)

const Version = "go-nats-mvp-0.1"

var StageOrder = []models.Stage{
	models.StageNormalize,
	models.StageSemantic,
	models.StagePlan,
	models.StageSearch,
	models.StageEnrich,
	models.StageRank,
	models.StageAIDetect,
	models.StageExperts,
	models.StageFinalize,
}

func NewEnvelope[T any](jobID, reportID, traceID string, stage models.Stage, payload T) models.Envelope[T] {
	if traceID == "" {
		traceID = uuid.NewString()
	}
	return models.Envelope[T]{
		MessageID:     uuid.NewString(),
		JobID:         jobID,
		ReportID:      reportID,
		TraceID:       traceID,
		Stage:         stage,
		Attempt:       1,
		CreatedAt:     time.Now().UTC(),
		SchemaVersion: models.SchemaVersion,
		Payload:       payload,
	}
}

func HashText(value string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(value)), " ")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func Progress(stage models.Stage, sourcesFound, sourcesAnalyzed int) models.JobProgress {
	completed := 0
	for idx, known := range StageOrder {
		if known == stage {
			completed = idx
			break
		}
	}
	return models.JobProgress{
		CompletedStages: completed,
		TotalStages:     len(StageOrder),
		SourcesFound:    sourcesFound,
		SourcesAnalyzed: sourcesAnalyzed,
	}
}

func Clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func ProviderFailure(provider string, stage models.Stage, err error) models.ProviderFailure {
	msg := "unavailable"
	if err != nil {
		msg = err.Error()
	}
	return models.ProviderFailure{
		Provider: provider,
		Stage:    stage,
		Error:    msg,
		At:       time.Now().UTC(),
	}
}
