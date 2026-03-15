package api

import (
	"context"
	"errors"
	"net/http"

	"app/internal/domain"
	"app/internal/store"
)

// CDCWorkflowStarter abstracts starting CDC-specific Temporal workflows.
type CDCWorkflowStarter interface {
	StartProvisionCDCWorkflow(ctx context.Context, spec domain.PipelineSpec, cfg domain.ConnectorConfig) (string, error)
	StartDeleteConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error)
	StartPauseConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error)
	StartResumeConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error)
}

// ConnectorHandler handles HTTP requests for connector operations.
type ConnectorHandler struct {
	Store          store.MetadataStore
	ConnectorStore store.ConnectorStore
	CDCWorkflows   CDCWorkflowStarter
	Auth           Authorizer
}

// RegisterConnectorRoutes wires connector routes into the given mux.
func (h *ConnectorHandler) RegisterConnectorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/tenants/{tenant}/pipelines/{pipeline}/connectors", h.createConnector)
	mux.HandleFunc("GET /v1/tenants/{tenant}/pipelines/{pipeline}/connectors", h.listConnectors)
	mux.HandleFunc("GET /v1/tenants/{tenant}/pipelines/{pipeline}/connectors/{connector}", h.getConnector)
	mux.HandleFunc("DELETE /v1/tenants/{tenant}/pipelines/{pipeline}/connectors/{connector}", h.deleteConnector)
	mux.HandleFunc("POST /v1/tenants/{tenant}/pipelines/{pipeline}/connectors/{connector}/pause", h.pauseConnector)
	mux.HandleFunc("POST /v1/tenants/{tenant}/pipelines/{pipeline}/connectors/{connector}/resume", h.resumeConnector)
}

// --- Request / response types ---

type createConnectorRequest struct {
	ConnectorConfig domain.ConnectorConfig `json:"connector_config"`
}

type createConnectorResponse struct {
	TenantID      string `json:"tenant_id"`
	PipelineID    string `json:"pipeline_id"`
	ConnectorName string `json:"connector_name"`
	WorkflowID    string `json:"workflow_id"`
}

type connectorListItem struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type connectorDetailResponse struct {
	TenantID      string `json:"tenant_id"`
	PipelineID    string `json:"pipeline_id"`
	ConnectorName string `json:"connector_name"`
	State         string `json:"state"`
}

// --- Handlers ---

func (h *ConnectorHandler) createConnector(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))
	pipelineID := domain.PipelineID(r.PathValue("pipeline"))

	if err := h.Auth.Authorize(r, tenantID, "connector:create"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	var req createConnectorRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := domain.ValidateConnectorConfig(req.ConnectorConfig); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	spec, err := h.Store.GetPipeline(r.Context(), tenantID, pipelineID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	workflowID, err := h.CDCWorkflows.StartProvisionCDCWorkflow(r.Context(), spec, req.ConnectorConfig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start CDC provisioning: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, createConnectorResponse{
		TenantID:      string(tenantID),
		PipelineID:    string(pipelineID),
		ConnectorName: req.ConnectorConfig.Name,
		WorkflowID:    workflowID,
	})
}

func (h *ConnectorHandler) listConnectors(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))
	pipelineID := domain.PipelineID(r.PathValue("pipeline"))

	if err := h.Auth.Authorize(r, tenantID, "connector:list"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	recs, err := h.ConnectorStore.ListConnectors(r.Context(), tenantID, pipelineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]connectorListItem, len(recs))
	for i, rec := range recs {
		items[i] = connectorListItem{Name: rec.Name, State: string(rec.State)}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *ConnectorHandler) getConnector(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))
	pipelineID := domain.PipelineID(r.PathValue("pipeline"))
	connectorName := r.PathValue("connector")

	if err := h.Auth.Authorize(r, tenantID, "connector:get"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	rec, err := h.ConnectorStore.GetConnector(r.Context(), tenantID, pipelineID, connectorName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, connectorDetailResponse{
		TenantID:      string(tenantID),
		PipelineID:    string(pipelineID),
		ConnectorName: rec.Name,
		State:         string(rec.State),
	})
}

func (h *ConnectorHandler) deleteConnector(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))
	pipelineID := domain.PipelineID(r.PathValue("pipeline"))
	connectorName := r.PathValue("connector")

	if err := h.Auth.Authorize(r, tenantID, "connector:delete"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	spec, err := h.Store.GetPipeline(r.Context(), tenantID, pipelineID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if _, err := h.CDCWorkflows.StartDeleteConnectorWorkflow(r.Context(), spec, connectorName); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start delete workflow: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *ConnectorHandler) pauseConnector(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))
	pipelineID := domain.PipelineID(r.PathValue("pipeline"))
	connectorName := r.PathValue("connector")

	if err := h.Auth.Authorize(r, tenantID, "connector:pause"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	spec, err := h.Store.GetPipeline(r.Context(), tenantID, pipelineID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if _, err := h.CDCWorkflows.StartPauseConnectorWorkflow(r.Context(), spec, connectorName); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start pause workflow: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *ConnectorHandler) resumeConnector(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))
	pipelineID := domain.PipelineID(r.PathValue("pipeline"))
	connectorName := r.PathValue("connector")

	if err := h.Auth.Authorize(r, tenantID, "connector:resume"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	spec, err := h.Store.GetPipeline(r.Context(), tenantID, pipelineID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if _, err := h.CDCWorkflows.StartResumeConnectorWorkflow(r.Context(), spec, connectorName); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start resume workflow: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusAccepted)
}
