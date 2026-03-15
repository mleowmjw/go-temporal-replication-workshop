package temporal

import (
	"context"
	"fmt"

	"app/internal/connectors"
	"app/internal/domain"
	"app/internal/store"
)

// Dependencies holds all external interfaces needed by activities.
// Using a struct allows straightforward replacement in tests.
type Dependencies struct {
	Store          store.MetadataStore
	Secrets        connectors.SecretProvider
	SchemaRegistry connectors.SchemaRegistry
	CDC            connectors.CDCProvisioner
	Stream         connectors.StreamProvisioner
	Sink           connectors.SinkProvisioner
}

// Activities wraps Dependencies and exposes methods registered as Temporal activities.
type Activities struct {
	deps Dependencies
}

func NewActivities(deps Dependencies) *Activities {
	return &Activities{deps: deps}
}

// ValidatePipelineSpecActivity ensures the spec is structurally valid.
func (a *Activities) ValidatePipelineSpecActivity(_ context.Context, spec domain.PipelineSpec) error {
	return domain.ValidatePipelineSpec(spec)
}

// ValidateSchemaPolicyActivityInput carries both spec and the proposed schema for validation.
type ValidateSchemaPolicyActivityInput struct {
	Spec   domain.PipelineSpec
	Schema string
}

// ValidateSchemaPolicyActivity checks schema compatibility before provisioning.
func (a *Activities) ValidateSchemaPolicyActivity(ctx context.Context, in ValidateSchemaPolicyActivityInput) error {
	if in.Spec.Schema.Subject == "" || in.Schema == "" {
		return nil
	}
	_, _, err := a.deps.SchemaRegistry.GetLatest(ctx, in.Spec.Schema.Subject)
	if err != nil {
		// No prior schema — first registration; skip compat check here.
		return nil
	}
	_, err = a.deps.SchemaRegistry.Register(ctx, in.Spec.Schema.Subject+".__validate__", in.Schema, in.Spec.Schema.Compatibility)
	return err
}

// EnsureSchemaSubjectActivity registers the initial schema subject.
func (a *Activities) EnsureSchemaSubjectActivity(ctx context.Context, spec domain.PipelineSpec) (int, error) {
	if spec.Schema.Subject == "" {
		return 0, nil
	}
	mode := spec.Schema.Compatibility
	if mode == "" {
		mode = domain.CompatBackward
	}
	id, err := a.deps.SchemaRegistry.Register(ctx, spec.Schema.Subject, spec.Schema.Format, mode)
	if err != nil {
		return 0, fmt.Errorf("register schema subject %q: %w", spec.Schema.Subject, err)
	}
	return id, nil
}

// PrepareSourceActivity sets up logical replication prerequisites on the source.
func (a *Activities) PrepareSourceActivity(ctx context.Context, spec domain.PipelineSpec) error {
	if err := a.deps.CDC.PrepareSource(ctx, spec.Source); err != nil {
		return fmt.Errorf("prepare source: %w", err)
	}
	return nil
}

// EnsureStreamActivity provisions the logical stream (topic/queue).
func (a *Activities) EnsureStreamActivity(ctx context.Context, spec domain.PipelineSpec) (string, error) {
	streamID, err := a.deps.Stream.EnsureStream(ctx, spec.TenantID, spec.PipelineID, spec.Schema.Subject)
	if err != nil {
		return "", fmt.Errorf("ensure stream: %w", err)
	}
	return streamID, nil
}

// EnsureSinkActivity provisions the sink connector.
func (a *Activities) EnsureSinkActivity(ctx context.Context, spec domain.PipelineSpec) (string, error) {
	sinkID, err := a.deps.Sink.EnsureSink(ctx, spec.Sink)
	if err != nil {
		return "", fmt.Errorf("ensure sink: %w", err)
	}
	return sinkID, nil
}

// StartCaptureActivityInput carries spec and captured resource IDs.
type StartCaptureActivityInput struct {
	Spec domain.PipelineSpec
}

// StartCaptureActivity starts CDC capture and returns a handle.
func (a *Activities) StartCaptureActivity(ctx context.Context, in StartCaptureActivityInput) (connectors.CaptureHandle, error) {
	h, err := a.deps.CDC.StartCapture(ctx, in.Spec.Source)
	if err != nil {
		return connectors.CaptureHandle{}, fmt.Errorf("start capture: %w", err)
	}
	return h, nil
}

// MarkPipelineActiveActivityInput carries all provisioned resource IDs.
type MarkPipelineActiveActivityInput struct {
	Spec      domain.PipelineSpec
	SchemaID  int
	StreamID  string
	SinkID    string
	CaptureID string
}

// MarkPipelineActiveActivity records the pipeline as ACTIVE in the metadata store.
func (a *Activities) MarkPipelineActiveActivity(ctx context.Context, in MarkPipelineActiveActivityInput) error {
	return a.deps.Store.SetPipelineState(ctx, in.Spec.TenantID, in.Spec.PipelineID, domain.StateActive)
}

// MarkPipelinePausedActivity marks the pipeline state as PAUSED.
func (a *Activities) MarkPipelinePausedActivity(ctx context.Context, spec domain.PipelineSpec) error {
	return a.deps.Store.SetPipelineState(ctx, spec.TenantID, spec.PipelineID, domain.StatePaused)
}

// StopCaptureActivity stops CDC capture (used in compensation and pause).
func (a *Activities) StopCaptureActivity(ctx context.Context, h connectors.CaptureHandle) error {
	return a.deps.CDC.StopCapture(ctx, h)
}

// DeleteSinkActivity removes a sink connector (used in compensation).
func (a *Activities) DeleteSinkActivity(ctx context.Context, sinkID string) error {
	return a.deps.Sink.DeleteSink(ctx, sinkID)
}

// DeleteStreamActivity removes a stream (used in compensation).
func (a *Activities) DeleteStreamActivity(ctx context.Context, streamID string) error {
	return a.deps.Stream.DeleteStream(ctx, streamID)
}

// MarkPipelineErrorActivity marks the pipeline state as ERROR.
func (a *Activities) MarkPipelineErrorActivity(ctx context.Context, spec domain.PipelineSpec) error {
	return a.deps.Store.SetPipelineState(ctx, spec.TenantID, spec.PipelineID, domain.StateError)
}
