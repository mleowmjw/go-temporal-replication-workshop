package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"app/internal/domain"
)

type connectorKey struct {
	tenant   domain.TenantID
	pipeline domain.PipelineID
	name     string
}

// InMemoryConnectorStore is a thread-safe in-memory implementation of ConnectorStore.
type InMemoryConnectorStore struct {
	mu         sync.RWMutex
	connectors map[connectorKey]domain.ConnectorRecord
}

// NewInMemoryConnectorStore creates an empty store.
func NewInMemoryConnectorStore() *InMemoryConnectorStore {
	return &InMemoryConnectorStore{
		connectors: make(map[connectorKey]domain.ConnectorRecord),
	}
}

func (s *InMemoryConnectorStore) CreateConnector(_ context.Context, rec domain.ConnectorRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := connectorKey{rec.TenantID, rec.PipelineID, rec.Name}
	if _, ok := s.connectors[k]; ok {
		return fmt.Errorf("connector %q for pipeline %q: already exists", rec.Name, rec.PipelineID)
	}
	rec.LastUpdated = time.Now().UnixNano()
	s.connectors[k] = rec
	return nil
}

func (s *InMemoryConnectorStore) GetConnector(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID, name string) (domain.ConnectorRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := connectorKey{tenant, pipeline, name}
	rec, ok := s.connectors[k]
	if !ok {
		return domain.ConnectorRecord{}, fmt.Errorf("connector %q for pipeline %q: %w", name, pipeline, ErrNotFound)
	}
	return rec, nil
}

func (s *InMemoryConnectorStore) ListConnectors(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID) ([]domain.ConnectorRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var recs []domain.ConnectorRecord
	for k, v := range s.connectors {
		if k.tenant == tenant && k.pipeline == pipeline {
			recs = append(recs, v)
		}
	}
	return recs, nil
}

func (s *InMemoryConnectorStore) UpdateConnectorState(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID, name string, state domain.ConnectorState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := connectorKey{tenant, pipeline, name}
	rec, ok := s.connectors[k]
	if !ok {
		return fmt.Errorf("connector %q for pipeline %q: %w", name, pipeline, ErrNotFound)
	}
	rec.State = state
	rec.LastUpdated = time.Now().UnixNano()
	s.connectors[k] = rec
	return nil
}

func (s *InMemoryConnectorStore) DeleteConnector(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := connectorKey{tenant, pipeline, name}
	delete(s.connectors, k)
	return nil
}
