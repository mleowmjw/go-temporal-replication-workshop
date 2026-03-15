package connectors

import (
	"context"

	"app/internal/domain"
)

// SecretProvider resolves secret references to key/value maps.
type SecretProvider interface {
	Resolve(ctx context.Context, secretRef string) (map[string]string, error)
}

// SchemaRegistry registers and retrieves schemas with compatibility enforcement.
type SchemaRegistry interface {
	Register(ctx context.Context, subject string, schema string, mode domain.CompatibilityMode) (schemaID int, err error)
	GetLatest(ctx context.Context, subject string) (schema string, id int, err error)
}

// CDCProvisioner prepares and manages CDC capture from a source.
type CDCProvisioner interface {
	PrepareSource(ctx context.Context, spec domain.SourceSpec) error
	StartCapture(ctx context.Context, spec domain.SourceSpec) (CaptureHandle, error)
	StopCapture(ctx context.Context, h CaptureHandle) error
}

// CaptureHandle identifies a running CDC capture.
type CaptureHandle struct {
	ID string
}

// StreamProvisioner manages logical streams (topics/queues).
type StreamProvisioner interface {
	EnsureStream(ctx context.Context, tenant domain.TenantID, pipeline domain.PipelineID, schemaSubject string) (streamID string, err error)
	DeleteStream(ctx context.Context, streamID string) error
}

// SinkProvisioner manages sink connectors.
type SinkProvisioner interface {
	EnsureSink(ctx context.Context, spec domain.SinkSpec) (sinkID string, err error)
	DeleteSink(ctx context.Context, sinkID string) error
}
