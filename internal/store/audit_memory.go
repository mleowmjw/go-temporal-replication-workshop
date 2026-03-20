package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"app/internal/domain"

	"github.com/google/uuid"
)

// InMemoryAuditStore is a thread-safe in-process implementation of AuditStore.
type InMemoryAuditStore struct {
	mu     sync.RWMutex
	events []domain.AuditEvent
}

// NewInMemoryAuditStore returns a ready-to-use InMemoryAuditStore.
func NewInMemoryAuditStore() *InMemoryAuditStore {
	return &InMemoryAuditStore{}
}

// RecordEvent appends an audit event; assigns an ID and timestamp if not set.
func (s *InMemoryAuditStore) RecordEvent(_ context.Context, event domain.AuditEvent) error {
	if event.TenantID == "" || event.PipelineID == "" {
		return fmt.Errorf("audit: TenantID and PipelineID are required")
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

// ListEvents returns all events for the given tenant+pipeline ordered oldest-first.
func (s *InMemoryAuditStore) ListEvents(_ context.Context, tenantID domain.TenantID, pipelineID domain.PipelineID) ([]domain.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []domain.AuditEvent
	for _, e := range s.events {
		if e.TenantID == tenantID && e.PipelineID == pipelineID {
			result = append(result, e)
		}
	}
	return result, nil
}
