package bluegreen

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// ErrDeploymentAlreadyExists is returned by WorkflowClient.StartDeployment when
// a workflow with the same deployment ID is already running.
var ErrDeploymentAlreadyExists = errors.New("deployment already exists")

// ErrSchemaCurrentlyLocked is returned by WorkflowClient.StartDeployment when
// the DatabaseOpsWorkflow rejects the lock request because another deployment
// is already active on this database.
var ErrSchemaCurrentlyLocked = errors.New("schema lock held by another deployment")

// WorkflowClient decouples the HTTP handler from the Temporal client.
// All deployment state is read by querying the running workflow directly.
type WorkflowClient interface {
	// StartDeployment acquires the schema lock via DatabaseOpsWorkflow, then
	// starts the BlueGreenDeploymentWorkflow. Returns the deployment workflow ID.
	// Returns ErrDeploymentAlreadyExists if a workflow with the same plan ID is running.
	// Returns ErrSchemaCurrentlyLocked if another deployment holds the schema lock.
	StartDeployment(ctx context.Context, plan MigrationPlan) (string, error)

	// ApproveDeployment sends the "approve" signal to a running workflow.
	ApproveDeployment(ctx context.Context, deploymentID string, payload ApprovalPayload) error

	// RollbackDeployment sends the "rollback" signal to a running workflow.
	RollbackDeployment(ctx context.Context, deploymentID string, payload RollbackPayload) error

	// GetDeploymentStatus queries the workflow for its current phase and history.
	// Returns ErrDeploymentNotFound if no workflow with that ID exists.
	GetDeploymentStatus(ctx context.Context, deploymentID string) (DeploymentStatus, error)

	// GetDatabaseOpsState queries the DatabaseOpsWorkflow for the coordinator state.
	// Returns ErrDeploymentNotFound if the coordinator workflow has not been started.
	GetDatabaseOpsState(ctx context.Context) (DatabaseOpsState, error)
}

// ErrDeploymentNotFound is returned by WorkflowClient.GetDeploymentStatus when
// no workflow with the given deployment ID exists.
var ErrDeploymentNotFound = errors.New("deployment not found")

// Handler serves the blue-green deployment HTTP control plane.
type Handler struct {
	client WorkflowClient
	log    *slog.Logger
}

// NewHandler creates a new Handler.
func NewHandler(client WorkflowClient, log *slog.Logger) *Handler {
	return &Handler{client: client, log: log}
}

// RegisterRoutes mounts all blue-green routes on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	routes := []string{
		"POST /v1/deployments",
		"GET /v1/deployments/{id}",
		"POST /v1/deployments/{id}/approve",
		"POST /v1/deployments/{id}/rollback",
		"GET /v1/database",
		"GET /healthz",
	}
	h.log.Info("registering API routes", "count", len(routes), "routes", routes)

	mux.HandleFunc("POST /v1/deployments", h.createDeployment)
	mux.HandleFunc("GET /v1/deployments/{id}", h.getDeployment)
	mux.HandleFunc("POST /v1/deployments/{id}/approve", h.approveDeployment)
	mux.HandleFunc("POST /v1/deployments/{id}/rollback", h.rollbackDeployment)
	mux.HandleFunc("GET /v1/database", h.getDatabaseOpsState)
	mux.HandleFunc("GET /healthz", h.healthz)

	h.log.Info("API routes registered")
}

// createDeployment POST /v1/deployments
// Body: MigrationPlan JSON. Starts the BlueGreenDeploymentWorkflow.
// Duplicate detection is handled by Temporal's workflow-ID uniqueness guarantee.
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

	workflowID, err := h.client.StartDeployment(r.Context(), plan)
	if err != nil {
		if errors.Is(err, ErrDeploymentAlreadyExists) {
			writeError(w, http.StatusConflict, "deployment already exists: "+plan.ID)
			return
		}
		if errors.Is(err, ErrSchemaCurrentlyLocked) {
			writeError(w, http.StatusConflict, "schema lock held by another deployment")
			return
		}
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
// Queries the running (or completed) Temporal workflow for current state.
func (h *Handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, err := h.client.GetDeploymentStatus(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "deployment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// approveDeployment POST /v1/deployments/{id}/approve
func (h *Handler) approveDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var payload ApprovalPayload
	_ = json.NewDecoder(r.Body).Decode(&payload) // optional body

	if err := h.client.ApproveDeployment(r.Context(), id, payload); err != nil {
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

	if err := h.client.RollbackDeployment(r.Context(), id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "signal failed: "+err.Error())
		return
	}
	h.log.Info("rollback signal sent", "id", id, "reason", payload.Reason)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rollback_requested"})
}

// getDatabaseOpsState GET /v1/database
// Returns the DatabaseOpsWorkflow coordinator state (active lock, history).
func (h *Handler) getDatabaseOpsState(w http.ResponseWriter, r *http.Request) {
	state, err := h.client.GetDatabaseOpsState(r.Context())
	if err != nil {
		if errors.Is(err, ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "database ops coordinator not started")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
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
