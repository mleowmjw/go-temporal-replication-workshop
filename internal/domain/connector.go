package domain

import "errors"

// ConnectorState represents the lifecycle state of a Kafka Connect connector.
type ConnectorState string

const (
	ConnectorStateUnassigned ConnectorState = "UNASSIGNED"
	ConnectorStateRunning    ConnectorState = "RUNNING"
	ConnectorStatePaused     ConnectorState = "PAUSED"
	ConnectorStateFailed     ConnectorState = "FAILED"
	ConnectorStateStopped    ConnectorState = "STOPPED"
)

// TaskStatus describes a single connector task's state.
type TaskStatus struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Trace string `json:"trace,omitempty"`
}

// ConnectorStatus is the runtime status returned by Kafka Connect.
type ConnectorStatus struct {
	Name     string         `json:"name"`
	State    ConnectorState `json:"state"`
	WorkerID string         `json:"worker_id"`
	Tasks    []TaskStatus   `json:"tasks"`
}

// ConnectorConfig is the declarative configuration submitted to Kafka Connect.
type ConnectorConfig struct {
	// Name is the unique connector name in Kafka Connect.
	Name string `json:"name"`
	// Config holds the raw key/value connector configuration.
	Config map[string]string `json:"config"`
}

// ConnectorRecord persists a connector's identity and current status in the store.
type ConnectorRecord struct {
	PipelineID  PipelineID
	TenantID    TenantID
	Name        string
	Config      ConnectorConfig
	State       ConnectorState
	LastUpdated int64 // Unix nano
}

// ValidateConnectorConfig checks required fields.
func ValidateConnectorConfig(c ConnectorConfig) error {
	if c.Name == "" {
		return errors.New("connector name is required")
	}
	if len(c.Config) == 0 {
		return errors.New("connector config must not be empty")
	}
	if c.Config["connector.class"] == "" {
		return errors.New("connector.class is required in config")
	}
	return nil
}
