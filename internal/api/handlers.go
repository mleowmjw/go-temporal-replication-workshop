package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"app/internal/domain"
	"app/internal/store"
)

// WorkflowStarter abstracts kicking off a Temporal workflow so handlers don't
// import the Temporal client directly (keeps tests simple).
type WorkflowStarter interface {
	StartProvisionWorkflow(ctx context.Context, spec domain.PipelineSpec) (string, error)
}

// Handler holds dependencies for all HTTP handlers.
type Handler struct {
	Store     store.MetadataStore
	Workflows WorkflowStarter
	Auth      Authorizer
	Log       *slog.Logger
}

// NewHandler creates a Handler with defaults applied.
func NewHandler(s store.MetadataStore, wf WorkflowStarter, auth Authorizer, log *slog.Logger) *Handler {
	if auth == nil {
		auth = AllowAllAuthorizer{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Handler{Store: s, Workflows: wf, Auth: auth, Log: log}
}

// RegisterRoutes wires all routes into mux using Go 1.22 method+path patterns.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/tenants", h.createTenant)
	mux.HandleFunc("POST /v1/tenants/{tenant}/pipelines", h.createPipeline)
	mux.HandleFunc("GET /v1/tenants/{tenant}/pipelines/{pipeline}", h.getPipeline)
}

// --- Request / Response types ---

type createTenantRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Tier string `json:"tier"`
}

type createTenantResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Tier string `json:"tier"`
}

type createPipelineRequest struct {
	PipelineID string            `json:"pipeline_id"`
	Name       string            `json:"name"`
	Source     domain.SourceSpec `json:"source"`
	Sink       domain.SinkSpec   `json:"sink"`
	Schema     domain.SchemaSpec `json:"schema"`
	Ops        domain.OpsSpec    `json:"ops"`
}

type createPipelineResponse struct {
	TenantID   string `json:"tenant_id"`
	PipelineID string `json:"pipeline_id"`
	WorkflowID string `json:"workflow_id"`
	State      string `json:"state"`
}

type getPipelineResponse struct {
	TenantID   string `json:"tenant_id"`
	PipelineID string `json:"pipeline_id"`
	Name       string `json:"name"`
	State      string `json:"state"`
}

// --- Handlers ---

func (h *Handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	tenant := domain.Tenant{ID: domain.TenantID(req.ID), Name: req.Name, Tier: req.Tier}
	if err := domain.ValidateTenant(tenant); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.Store.CreateTenant(r.Context(), tenant); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, createTenantResponse{ID: req.ID, Name: req.Name, Tier: req.Tier})
}

func (h *Handler) createPipeline(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))

	if err := h.Auth.Authorize(r, tenantID, "pipeline:create"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	var req createPipelineRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	spec := domain.PipelineSpec{
		TenantID:   tenantID,
		PipelineID: domain.PipelineID(req.PipelineID),
		Name:       req.Name,
		Source:     req.Source,
		Sink:       req.Sink,
		Schema:     req.Schema,
		Ops:        req.Ops,
	}

	if err := domain.ValidatePipelineSpec(spec); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.Store.CreatePipeline(r.Context(), spec); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	workflowID, err := h.Workflows.StartProvisionWorkflow(r.Context(), spec)
	if err != nil {
		h.Log.Error("failed to start provision workflow", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start provisioning")
		return
	}

	writeJSON(w, http.StatusAccepted, createPipelineResponse{
		TenantID:   string(tenantID),
		PipelineID: req.PipelineID,
		WorkflowID: workflowID,
		State:      string(domain.StatePending),
	})
}

func (h *Handler) getPipeline(w http.ResponseWriter, r *http.Request) {
	tenantID := domain.TenantID(r.PathValue("tenant"))
	pipelineID := domain.PipelineID(r.PathValue("pipeline"))

	if err := h.Auth.Authorize(r, tenantID, "pipeline:get"); err != nil {
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

	state, err := h.Store.GetPipelineState(r.Context(), tenantID, pipelineID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, getPipelineResponse{
		TenantID:   string(spec.TenantID),
		PipelineID: string(spec.PipelineID),
		Name:       spec.Name,
		State:      string(state),
	})
}

// --- Helpers ---

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
