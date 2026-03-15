package store

import (
	"context"

	"app/internal/domain"
)

// ConnectorStore persists connector records for a pipeline.
type ConnectorStore interface {
	CreateConnector(ctx context.Context, rec domain.ConnectorRecord) error
	GetConnector(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID, name string) (domain.ConnectorRecord, error)
	ListConnectors(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID) ([]domain.ConnectorRecord, error)
	UpdateConnectorState(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID, name string, state domain.ConnectorState) error
	DeleteConnector(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID, name string) error
}
