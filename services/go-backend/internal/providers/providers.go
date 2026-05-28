package providers

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hnweb/provenance/internal/models"
)

var ErrUnavailable = errors.New("provider unavailable")

type SourceQuery struct {
	Query      string
	Source     string
	MaxResults int
	JobID      string
	ReportID   string
}

type SourceRef struct {
	URL      string
	SourceID string
}

type ProviderCapabilities struct {
	SupportedSourceTypes        []models.SourceType `json:"supported_source_types"`
	SupportsXSearch             bool                `json:"supports_x_search"`
	ReturnsUnavailableEvidence  bool                `json:"returns_unavailable_evidence"`
	SupportsFullTextFetch       bool                `json:"supports_full_text_fetch"`
	ReliablePublishedTimestamps bool                `json:"reliable_published_timestamps"`
	CostModel                   string              `json:"cost_model"`
	RateLimitProfile            string              `json:"rate_limit_profile"`
}

type SourceProvider interface {
	ID() string
	Capabilities() ProviderCapabilities
	Search(ctx context.Context, query SourceQuery) ([]models.SourceResult, error)
	Fetch(ctx context.Context, ref SourceRef) (*models.SourceResult, error)
}

type CampaignProvider interface {
	ID() string
	Capabilities() ProviderCapabilities
	Collect(ctx context.Context, target models.CollectionTarget) ([]models.SourceItem, error)
	FetchAccount(ctx context.Context, accountRef string) (*models.AccountProfile, error)
	FetchInteractions(ctx context.Context, source models.SourceItem, limit int) ([]models.InteractionEvent, error)
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]SourceProvider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]SourceProvider)}
}

func (r *Registry) Register(provider SourceProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.ID()] = provider
}

func (r *Registry) Get(id string) (SourceProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[id]
	return provider, ok
}

func (r *Registry) MustGet(id string) (SourceProvider, error) {
	provider, ok := r.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, id)
	}
	return provider, nil
}

func (r *Registry) All() []SourceProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SourceProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		out = append(out, provider)
	}
	return out
}

func (r *Registry) CampaignProviders() []CampaignProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []CampaignProvider{}
	for _, provider := range r.providers {
		if campaignProvider, ok := provider.(CampaignProvider); ok {
			out = append(out, campaignProvider)
		}
	}
	return out
}
