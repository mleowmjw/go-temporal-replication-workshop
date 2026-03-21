package bluegreen

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// WorkflowStarter decouples the HTTP handler from the Temporal client.
type WorkflowStarter interface {
	// StartDeployment starts the BlueGreenDeploymentWorkflow and returns the workflow ID.
	StartDeployment(ctx context.Context, plan MigrationPlan) (string, error)

	// ApproveDeployment sends the "approve" signal to a running workflow.
	ApproveDeployment(ctx context.Context, workflowID string, payload ApprovalPayload) error

	// RollbackDeployment sends the "rollback" signal to a running workflow.
	RollbackDeployment(ctx context.Context, workflowID string, payload RollbackPayload) error
}

// Handler serves the blue-green deployment HTTP control plane.
type Handler struct {
	store    DeploymentStore
	workflow WorkflowStarter
	log      *slog.Logger
}

// NewHandler creates a new Handler.
func NewHandler(store DeploymentStore, workflow WorkflowStarter, log *slog.Logger) *Handler {
	return &Handler{store: store, workflow: workflow, log: log}
}

// RegisterRoutes mounts all blue-green routes on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/deployments", h.createDeployment)
	mux.HandleFunc("GET /v1/deployments/{id}", h.getDeployment)
	mux.HandleFunc("POST /v1/deployments/{id}/approve", h.approveDeployment)
	mux.HandleFunc("POST /v1/deployments/{id}/rollback", h.rollbackDeployment)
	mux.HandleFunc("GET /healthz", h.healthz)
}

// createDeployment POST /v1/deployments
// Body: MigrationPlan JSON. Creates a deployment record and starts the workflow.
func (h *Handler) createDeployment(w http.ResponseWriter, r *http.Request) {
	var plan MigrationPlan
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := ValidateMigrationPlan(plan); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	d := Deployment{ID: plan.ID, Plan: plan}
	if err := h.store.Create(r.Context(), d); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	workflowID, err := h.workflow.StartDeployment(r.Context(), plan)
	if err != nil {
		// Best-effort: mark deployment as failed if workflow can't start.
		_ = h.store.UpdatePhase(r.Context(), plan.ID, PhaseFailed, "workflow start failed: "+err.Error())
		writeError(w, http.StatusInternalServerError, "start workflow: "+err.Error())
		return
	}

	h.log.Info("blue-green deployment started", "id", plan.ID, "workflowID", workflowID)
	writeJSON(w, http.StatusCreated, map[string]string{
		"deployment_id": plan.ID,
		"workflow_id":   workflowID,
		"status":        string(PhasePending),
	})
}

// getDeployment GET /v1/deployments/{id}
func (h *Handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := h.store.Get(r.Context(), id)
	if errors.Is(err, ErrDeploymentNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// approveDeployment POST /v1/deployments/{id}/approve
func (h *Handler) approveDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var payload ApprovalPayload
	_ = json.NewDecoder(r.Body).Decode(&payload) // optional body

	if err := h.workflow.ApproveDeployment(r.Context(), id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "signal failed: "+err.Error())
		return
	}
	h.log.Info("approval signal sent", "id", id, "note", payload.Note)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "approved"})
}

// rollbackDeployment POST /v1/deployments/{id}/rollback
func (h *Handler) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var payload RollbackPayload
	_ = json.NewDecoder(r.Body).Decode(&payload)

	if err := h.workflow.RollbackDeployment(r.Context(), id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "signal failed: "+err.Error())
		return
	}
	h.log.Info("rollback signal sent", "id", id, "reason", payload.Reason)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rollback_requested"})
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
