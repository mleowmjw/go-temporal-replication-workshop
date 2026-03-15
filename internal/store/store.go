package store

import (
	"context"

	"app/internal/domain"
)

// MetadataStore persists tenant and pipeline metadata.
type MetadataStore interface {
	CreateTenant(ctx context.Context, t domain.Tenant) error
	GetTenant(ctx context.Context, id domain.TenantID) (domain.Tenant, error)

	CreatePipeline(ctx context.Context, spec domain.PipelineSpec) error
	GetPipeline(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID) (domain.PipelineSpec, error)
	UpdatePipeline(ctx context.Context, spec domain.PipelineSpec) error

	SetPipelineState(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID, state domain.PipelineState) error
	GetPipelineState(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID) (domain.PipelineState, error)
}
