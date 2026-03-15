package domain

import "time"

type TenantID string

type Tenant struct {
	ID   TenantID
	Name string
	Tier string // "standard" | "enterprise"
}

type PipelineID string

type PipelineState string

const (
	StatePending        PipelineState = "PENDING"
	StateProvisioning   PipelineState = "PROVISIONING"
	StateActive         PipelineState = "ACTIVE"
	StatePaused         PipelineState = "PAUSED"
	StateError          PipelineState = "ERROR"
	StateDecommissioned PipelineState = "DECOMMISSIONED"
)

type SourceType string

const (
	SourcePostgres SourceType = "postgres"
)

type SinkType string

const (
	SinkSearch   SinkType = "search"
	SinkLake     SinkType = "lake"
	SinkPostgres SinkType = "postgres"
)

type CompatibilityMode string

const (
	CompatBackward CompatibilityMode = "BACKWARD"
)

type PipelineSpec struct {
	TenantID   TenantID
	PipelineID PipelineID
	Name       string

	Source     SourceSpec
	Sink       SinkSpec
	Transform  TransformSpec
	Enrichment EnrichmentSpec
	Schema     SchemaSpec
	Ops        OpsSpec
}

type SourceSpec struct {
	Type      SourceType `json:"type"`
	SecretRef string     `json:"secret_ref"`
	Database  string     `json:"database,omitempty"`
	Schema    string     `json:"schema,omitempty"`
	Tables    []string   `json:"tables"`
}

type SinkSpec struct {
	Type      SinkType `json:"type"`
	SecretRef string   `json:"secret_ref,omitempty"`
	Target    string   `json:"target"`
}

type TransformSpec struct {
	Enabled bool
	Rules   []TransformRule
}

type TransformRule struct {
	Kind   string            // "filter" | "project" | "rename" | "composite_key"
	Params map[string]string
}

type EnrichmentSpec struct {
	Enabled     bool
	Endpoint    string
	Timeout     time.Duration
	FailureMode string // "drop" | "retry" | "dlq"
}

type SchemaSpec struct {
	Format        string            // "avro" | "protobuf" | "jsonschema"
	Compatibility CompatibilityMode // default BACKWARD
	Subject       string
}

type OpsSpec struct {
	DesiredLag time.Duration
	MaxRetries int
	DLQEnabled bool
}

// ProvisionResult is returned by ProvisionPipelineWorkflow.
type ProvisionResult struct {
	SchemaID  int
	StreamID  string
	SinkID    string
	CaptureID string
}
