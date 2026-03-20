package store

import (
	"context"

	"app/internal/domain"
)

// AuditStore records and retrieves pipeline lifecycle audit events.
type AuditStore interface {
	// RecordEvent persists a new audit event.
	RecordEvent(ctx context.Context, event domain.AuditEvent) error

	// ListEvents returns all audit events for a tenant's pipeline, ordered oldest-first.
	ListEvents(ctx context.Context, tenantID domain.TenantID, pipelineID domain.PipelineID) ([]domain.AuditEvent, error)
}
