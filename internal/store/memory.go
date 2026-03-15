package store

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"app/internal/domain"
)

// ErrNotFound is returned when a resource does not exist.
var ErrNotFound = errors.New("not found")

type tenantKey = domain.TenantID

type pipelineKey struct {
	tenant   domain.TenantID
	pipeline domain.PipelineID
}

// InMemoryMetadataStore is a thread-safe in-memory implementation of MetadataStore.
type InMemoryMetadataStore struct {
	mu       sync.RWMutex
	tenants  map[tenantKey]domain.Tenant
	pipelines map[pipelineKey]domain.PipelineSpec
	states    map[pipelineKey]domain.PipelineState
}

// NewInMemoryMetadataStore creates an empty store.
func NewInMemoryMetadataStore() *InMemoryMetadataStore {
	return &InMemoryMetadataStore{
		tenants:   make(map[tenantKey]domain.Tenant),
		pipelines: make(map[pipelineKey]domain.PipelineSpec),
		states:    make(map[pipelineKey]domain.PipelineState),
	}
}

func (s *InMemoryMetadataStore) CreateTenant(_ context.Context, t domain.Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tenants[t.ID]; ok {
		return fmt.Errorf("tenant %q already exists", t.ID)
	}
	s.tenants[t.ID] = t
	return nil
}

func (s *InMemoryMetadataStore) GetTenant(_ context.Context, id domain.TenantID) (domain.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return domain.Tenant{}, fmt.Errorf("tenant %q: %w", id, ErrNotFound)
	}
	return t, nil
}

func (s *InMemoryMetadataStore) CreatePipeline(_ context.Context, spec domain.PipelineSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pipelineKey{spec.TenantID, spec.PipelineID}
	if _, ok := s.pipelines[k]; ok {
		return fmt.Errorf("pipeline %q already exists for tenant %q", spec.PipelineID, spec.TenantID)
	}
	s.pipelines[k] = spec
	s.states[k] = domain.StatePending
	return nil
}

func (s *InMemoryMetadataStore) GetPipeline(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID) (domain.PipelineSpec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := pipelineKey{tenant, pipeline}
	spec, ok := s.pipelines[k]
	if !ok {
		return domain.PipelineSpec{}, fmt.Errorf("pipeline %q for tenant %q: %w", pipeline, tenant, ErrNotFound)
	}
	return spec, nil
}

func (s *InMemoryMetadataStore) UpdatePipeline(_ context.Context, spec domain.PipelineSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pipelineKey{spec.TenantID, spec.PipelineID}
	if _, ok := s.pipelines[k]; !ok {
		return fmt.Errorf("pipeline %q for tenant %q: %w", spec.PipelineID, spec.TenantID, ErrNotFound)
	}
	s.pipelines[k] = spec
	return nil
}

func (s *InMemoryMetadataStore) SetPipelineState(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID, state domain.PipelineState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pipelineKey{tenant, pipeline}
	if _, ok := s.pipelines[k]; !ok {
		return fmt.Errorf("pipeline %q for tenant %q: %w", pipeline, tenant, ErrNotFound)
	}
	s.states[k] = state
	return nil
}

func (s *InMemoryMetadataStore) GetPipelineState(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID) (domain.PipelineState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := pipelineKey{tenant, pipeline}
	if _, ok := s.pipelines[k]; !ok {
		return "", fmt.Errorf("pipeline %q for tenant %q: %w", pipeline, tenant, ErrNotFound)
	}
	return s.states[k], nil
}
