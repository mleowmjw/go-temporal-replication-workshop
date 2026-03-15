package connectors

import (
	"context"

	"app/internal/domain"
)

// KafkaConnectClient manages Debezium connectors via the Kafka Connect REST API.
type KafkaConnectClient interface {
	// CreateConnector registers a new connector. Returns ErrConnectorExists if
	// a connector with that name already exists.
	CreateConnector(ctx context.Context, cfg domain.ConnectorConfig) error

	// GetConnectorStatus returns the current runtime status of a connector.
	GetConnectorStatus(ctx context.Context, name string) (domain.ConnectorStatus, error)

	// DeleteConnector removes a connector. It is idempotent: deleting a
	// non-existent connector returns nil.
	DeleteConnector(ctx context.Context, name string) error

	// ListConnectors returns the names of all registered connectors.
	ListConnectors(ctx context.Context) ([]string, error)

	// PauseConnector suspends a connector and its tasks.
	PauseConnector(ctx context.Context, name string) error

	// ResumeConnector restarts a paused connector.
	ResumeConnector(ctx context.Context, name string) error
}
