package domain

import "time"

// AuditAction represents a lifecycle event performed on a pipeline.
type AuditAction string

const (
	AuditActionCreate       AuditAction = "pipeline.create"
	AuditActionPause        AuditAction = "pipeline.pause"
	AuditActionResume       AuditAction = "pipeline.resume"
	AuditActionDecommission AuditAction = "pipeline.decommission"
	AuditActionDelete       AuditAction = "pipeline.delete"

	AuditActionConnectorCreate      AuditAction = "connector.create"
	AuditActionConnectorPause       AuditAction = "connector.pause"
	AuditActionConnectorResume      AuditAction = "connector.resume"
	AuditActionConnectorDecommission AuditAction = "connector.decommission"
)

// AuditEvent records a single lifecycle operation for a tenant pipeline.
type AuditEvent struct {
	ID         string
	TenantID   TenantID
	PipelineID PipelineID
	Action     AuditAction
	Actor      string
	Timestamp  time.Time
	Details    map[string]string
}
