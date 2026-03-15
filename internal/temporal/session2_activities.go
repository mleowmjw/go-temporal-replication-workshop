package temporal

import (
	"context"
	"fmt"
	"time"

	"app/internal/connectors"
	"app/internal/domain"
	"app/internal/store"

	"go.temporal.io/sdk/activity"
)

// safeHeartbeat calls activity.RecordHeartbeat but recovers from panics that occur
// when the activity is invoked directly (e.g. in unit tests outside a Temporal worker).
func safeHeartbeat(ctx context.Context, details ...interface{}) {
	defer func() { _ = recover() }()
	activity.RecordHeartbeat(ctx, details...)
}

// Session2Dependencies extends session-1 Dependencies with Kafka Connect and connector store.
type Session2Dependencies struct {
	Dependencies
	KafkaConnect   connectors.KafkaConnectClient
	ConnectorStore store.ConnectorStore
}

// Session2Activities exposes Temporal activity methods for Kafka Connect management.
type Session2Activities struct {
	deps Session2Dependencies
}

// NewSession2Activities creates a Session2Activities with the given dependencies.
func NewSession2Activities(deps Session2Dependencies) *Session2Activities {
	return &Session2Activities{deps: deps}
}

// CreateConnectorActivityInput carries everything needed to register a connector.
type CreateConnectorActivityInput struct {
	Spec   domain.PipelineSpec
	Config domain.ConnectorConfig
}

// CreateConnectorActivity registers the Debezium connector via Kafka Connect and records it in the store.
func (a *Session2Activities) CreateConnectorActivity(ctx context.Context, in CreateConnectorActivityInput) error {
	if err := domain.ValidateConnectorConfig(in.Config); err != nil {
		return fmt.Errorf("invalid connector config: %w", err)
	}
	if err := a.deps.KafkaConnect.CreateConnector(ctx, in.Config); err != nil {
		return fmt.Errorf("create connector %q: %w", in.Config.Name, err)
	}
	rec := domain.ConnectorRecord{
		TenantID:   in.Spec.TenantID,
		PipelineID: in.Spec.PipelineID,
		Name:       in.Config.Name,
		Config:     in.Config,
		State:      domain.ConnectorStateUnassigned,
	}
	if err := a.deps.ConnectorStore.CreateConnector(ctx, rec); err != nil {
		return fmt.Errorf("store connector record: %w", err)
	}
	return nil
}

// WaitForConnectorRunningActivityInput configures the polling behaviour.
type WaitForConnectorRunningActivityInput struct {
	Spec          domain.PipelineSpec
	ConnectorName string
	// PollInterval controls how frequently we check the connector status.
	// Defaults to 2s when zero.
	PollInterval time.Duration
	// Timeout is the maximum time to wait for RUNNING before giving up.
	// Defaults to 2 minutes when zero.
	Timeout time.Duration
}

// WaitForConnectorRunningActivity polls the connector until it reaches RUNNING,
// heartbeating on each iteration so Temporal knows the activity is alive.
func (a *Session2Activities) WaitForConnectorRunningActivity(ctx context.Context, in WaitForConnectorRunningActivityInput) error {
	interval := in.PollInterval
	if interval == 0 {
		interval = 2 * time.Second
	}
	timeout := in.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	deadline := time.Now().Add(timeout)
	for {
		safeHeartbeat(ctx, fmt.Sprintf("polling connector %q", in.ConnectorName))

		status, err := a.deps.KafkaConnect.GetConnectorStatus(ctx, in.ConnectorName)
		if err != nil {
			return fmt.Errorf("get connector status %q: %w", in.ConnectorName, err)
		}

		switch status.State {
		case domain.ConnectorStateRunning:
			if err := a.deps.ConnectorStore.UpdateConnectorState(ctx, in.Spec.TenantID, in.Spec.PipelineID, in.ConnectorName, domain.ConnectorStateRunning); err != nil {
				return fmt.Errorf("update connector state: %w", err)
			}
			return nil
		case domain.ConnectorStateFailed:
			trace := ""
			if len(status.Tasks) > 0 {
				trace = status.Tasks[0].Trace
			}
			return fmt.Errorf("connector %q failed: %s", in.ConnectorName, trace)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("connector %q did not reach RUNNING within %s (last state: %s)",
				in.ConnectorName, timeout, status.State)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// DeleteConnectorActivityInput identifies the connector to delete.
type DeleteConnectorActivityInput struct {
	Spec          domain.PipelineSpec
	ConnectorName string
}

// DeleteConnectorActivity removes a connector from Kafka Connect and the store.
// It is idempotent: no error if the connector does not exist.
func (a *Session2Activities) DeleteConnectorActivity(ctx context.Context, in DeleteConnectorActivityInput) error {
	if err := a.deps.KafkaConnect.DeleteConnector(ctx, in.ConnectorName); err != nil {
		return fmt.Errorf("delete connector %q: %w", in.ConnectorName, err)
	}
	if err := a.deps.ConnectorStore.DeleteConnector(ctx, in.Spec.TenantID, in.Spec.PipelineID, in.ConnectorName); err != nil {
		return fmt.Errorf("remove connector record: %w", err)
	}
	return nil
}

// PauseConnectorActivityInput identifies the connector to pause.
type PauseConnectorActivityInput struct {
	Spec          domain.PipelineSpec
	ConnectorName string
}

// PauseConnectorActivity suspends the connector and updates the store.
func (a *Session2Activities) PauseConnectorActivity(ctx context.Context, in PauseConnectorActivityInput) error {
	if err := a.deps.KafkaConnect.PauseConnector(ctx, in.ConnectorName); err != nil {
		return fmt.Errorf("pause connector %q: %w", in.ConnectorName, err)
	}
	if err := a.deps.ConnectorStore.UpdateConnectorState(ctx, in.Spec.TenantID, in.Spec.PipelineID, in.ConnectorName, domain.ConnectorStatePaused); err != nil {
		return fmt.Errorf("update connector state: %w", err)
	}
	return nil
}

// ResumeConnectorActivityInput identifies the connector to resume.
type ResumeConnectorActivityInput struct {
	Spec          domain.PipelineSpec
	ConnectorName string
}

// ResumeConnectorActivity restarts the connector and updates the store.
func (a *Session2Activities) ResumeConnectorActivity(ctx context.Context, in ResumeConnectorActivityInput) error {
	if err := a.deps.KafkaConnect.ResumeConnector(ctx, in.ConnectorName); err != nil {
		return fmt.Errorf("resume connector %q: %w", in.ConnectorName, err)
	}
	if err := a.deps.ConnectorStore.UpdateConnectorState(ctx, in.Spec.TenantID, in.Spec.PipelineID, in.ConnectorName, domain.ConnectorStateRunning); err != nil {
		return fmt.Errorf("update connector state: %w", err)
	}
	return nil
}
