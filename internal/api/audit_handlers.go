package api

import (
	"context"
	"net/http"
	"time"

	"app/internal/domain"
	"app/internal/store"
)

// AuditingWorkflowStarter wraps a WorkflowStarter and records audit events
// for every workflow start operation.
type AuditingWorkflowStarter struct {
	inner  WorkflowStarter
	audit  store.AuditStore
	actor  string
}

// NewAuditingWorkflowStarter wraps the given WorkflowStarter so that every
// StartProvisionWorkflow call records a pipeline.create audit event.
func NewAuditingWorkflowStarter(inner WorkflowStarter, audit store.AuditStore, actor string) *AuditingWorkflowStarter {
	if actor == "" {
		actor = "system"
	}
	return &AuditingWorkflowStarter{inner: inner, audit: audit, actor: actor}
}

func (a *AuditingWorkflowStarter) StartProvisionWorkflow(ctx context.Context, spec domain.PipelineSpec) (string, error) {
	id, err := a.inner.StartProvisionWorkflow(ctx, spec)
	if err != nil {
		return "", err
	}
	_ = a.audit.RecordEvent(ctx, domain.AuditEvent{
		TenantID:   spec.TenantID,
		PipelineID: spec.PipelineID,
		Action:     domain.AuditActionCreate,
		Actor:      a.actor,
		Timestamp:  time.Now().UTC(),
		Details:    map[string]string{"workflow_id": id},
	})
	return id, nil
}

// AuditingCDCWorkflowStarter wraps a CDCWorkflowStarter and records audit events.
type AuditingCDCWorkflowStarter struct {
	inner CDCWorkflowStarter
	audit store.AuditStore
	actor string
}

// NewAuditingCDCWorkflowStarter wraps the given CDCWorkflowStarter so that
// every CDC lifecycle operation records an audit event.
func NewAuditingCDCWorkflowStarter(inner CDCWorkflowStarter, audit store.AuditStore, actor string) *AuditingCDCWorkflowStarter {
	if actor == "" {
		actor = "system"
	}
	return &AuditingCDCWorkflowStarter{inner: inner, audit: audit, actor: actor}
}

func (a *AuditingCDCWorkflowStarter) StartProvisionCDCWorkflow(ctx context.Context, spec domain.PipelineSpec, cfg domain.ConnectorConfig) (string, error) {
	id, err := a.inner.StartProvisionCDCWorkflow(ctx, spec, cfg)
	if err != nil {
		return "", err
	}
	_ = a.audit.RecordEvent(ctx, domain.AuditEvent{
		TenantID:   spec.TenantID,
		PipelineID: spec.PipelineID,
		Action:     domain.AuditActionConnectorCreate,
		Actor:      a.actor,
		Timestamp:  time.Now().UTC(),
		Details:    map[string]string{"workflow_id": id, "connector": cfg.Name},
	})
	return id, nil
}

func (a *AuditingCDCWorkflowStarter) StartDeleteConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	id, err := a.inner.StartDeleteConnectorWorkflow(ctx, spec, connectorName)
	if err != nil {
		return "", err
	}
	_ = a.audit.RecordEvent(ctx, domain.AuditEvent{
		TenantID:   spec.TenantID,
		PipelineID: spec.PipelineID,
		Action:     domain.AuditActionConnectorDecommission,
		Actor:      a.actor,
		Timestamp:  time.Now().UTC(),
		Details:    map[string]string{"workflow_id": id, "connector": connectorName},
	})
	return id, nil
}

func (a *AuditingCDCWorkflowStarter) StartPauseConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	id, err := a.inner.StartPauseConnectorWorkflow(ctx, spec, connectorName)
	if err != nil {
		return "", err
	}
	_ = a.audit.RecordEvent(ctx, domain.AuditEvent{
		TenantID:   spec.TenantID,
		PipelineID: spec.PipelineID,
		Action:     domain.AuditActionConnectorPause,
		Actor:      a.actor,
		Timestamp:  time.Now().UTC(),
		Details:    map[string]string{"workflow_id": id, "connector": connectorName},
	})
	return id, nil
}

func (a *AuditingCDCWorkflowStarter) StartResumeConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	id, err := a.inner.StartResumeConnectorWorkflow(ctx, spec, connectorName)
	if err != nil {
		return "", err
	}
	_ = a.audit.RecordEvent(ctx, domain.AuditEvent{
		TenantID:   spec.TenantID,
		PipelineID: spec.PipelineID,
		Action:     domain.AuditActionConnectorResume,
		Actor:      a.actor,
		Timestamp:  time.Now().UTC(),
		Details:    map[string]string{"workflow_id": id, "connector": connectorName},
	})
	return id, nil
}

// AuditHandler handles HTTP requests for the audit trail.
type AuditHandler struct {
	Store  store.MetadataStore
	Audit  store.AuditStore
	Auth   Authorizer
}

// RegisterAuditRoutes wires the audit routes into the given mux.
func (h *AuditHandler) RegisterAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/tenants/{tenant}/pipelines/{pipeline}/audit", h.listAuditEvents)
}

type auditEventResponse struct {
	ID         string            `json:"id"`
	TenantID   string            `json:"tenant_id"`
	PipelineID string            `json:"pipeline_id"`
	Action     string            `json:"action"`
	Actor      string            `json:"actor"`
	Timestamp  time.Time         `json:"timestamp"`
	Details    map[string]string `json:"details,omitempty"`
}

func (h *AuditHandler) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	tenantID, pipelineID := tenantPipelinePathValues(r)

	if err := h.Auth.Authorize(r, tenantID, "audit:list"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	if _, ok := getPipelineOrWriteError(w, r, h.Store, tenantID, pipelineID); !ok {
		return
	}

	events, err := h.Audit.ListEvents(r.Context(), tenantID, pipelineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := make([]auditEventResponse, len(events))
	for i, e := range events {
		resp[i] = auditEventResponse{
			ID:         e.ID,
			TenantID:   string(e.TenantID),
			PipelineID: string(e.PipelineID),
			Action:     string(e.Action),
			Actor:      e.Actor,
			Timestamp:  e.Timestamp,
			Details:    e.Details,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// RegisterHealthRoute registers a simple /healthz liveness endpoint.
func RegisterHealthRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}
