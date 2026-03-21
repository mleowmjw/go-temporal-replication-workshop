package bluegreen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWorkflowStarter is an in-memory stub for tests.
type fakeWorkflowStarter struct {
	started   []bluegreen.MigrationPlan
	approvals []string
	rollbacks []string
	startErr  error
	signalErr error
}

func (f *fakeWorkflowStarter) StartDeployment(_ context.Context, plan bluegreen.MigrationPlan) (string, error) {
	if f.startErr != nil {
		return "", f.startErr
	}
	f.started = append(f.started, plan)
	return "wf-" + plan.ID, nil
}

func (f *fakeWorkflowStarter) ApproveDeployment(_ context.Context, id string, _ bluegreen.ApprovalPayload) error {
	if f.signalErr != nil {
		return f.signalErr
	}
	f.approvals = append(f.approvals, id)
	return nil
}

func (f *fakeWorkflowStarter) RollbackDeployment(_ context.Context, id string, _ bluegreen.RollbackPayload) error {
	if f.signalErr != nil {
		return f.signalErr
	}
	f.rollbacks = append(f.rollbacks, id)
	return nil
}

func newTestHandler(t *testing.T) (*bluegreen.Handler, *bluegreen.InMemoryDeploymentStore, *fakeWorkflowStarter) {
	t.Helper()
	store := bluegreen.NewInMemoryDeploymentStore()
	wf := &fakeWorkflowStarter{}
	h := bluegreen.NewHandler(store, wf, slog.Default())
	return h, store, wf
}

func newMux(h *bluegreen.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func TestCreateDeployment_Success(t *testing.T) {
	h, store, wf := newTestHandler(t)
	mux := newMux(h)

	body, _ := json.Marshal(validPlan())
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, validPlan().ID, resp["deployment_id"])
	assert.Equal(t, "wf-"+validPlan().ID, resp["workflow_id"])

	// Deployment must be persisted.
	d, err := store.Get(context.Background(), validPlan().ID)
	require.NoError(t, err)
	assert.Equal(t, validPlan().ID, d.ID)
	assert.Len(t, wf.started, 1)
}

func TestCreateDeployment_InvalidJSON(t *testing.T) {
	h, _, _ := newTestHandler(t)
	mux := newMux(h)

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewBufferString("not-json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateDeployment_InvalidPlan(t *testing.T) {
	h, _, _ := newTestHandler(t)
	mux := newMux(h)

	bad := validPlan()
	bad.ID = ""
	body, _ := json.Marshal(bad)
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateDeployment_Duplicate(t *testing.T) {
	h, store, _ := newTestHandler(t)
	mux := newMux(h)

	// Pre-create deployment.
	require.NoError(t, store.Create(context.Background(), bluegreen.Deployment{ID: validPlan().ID, Plan: validPlan()}))

	body, _ := json.Marshal(validPlan())
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestCreateDeployment_WorkflowStartError(t *testing.T) {
	h, _, wf := newTestHandler(t)
	mux := newMux(h)
	wf.startErr = errors.New("temporal unavailable")

	body, _ := json.Marshal(validPlan())
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestGetDeployment_Success(t *testing.T) {
	h, store, _ := newTestHandler(t)
	mux := newMux(h)

	require.NoError(t, store.Create(context.Background(), bluegreen.Deployment{ID: "d1", Plan: validPlan()}))

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments/d1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var d bluegreen.Deployment
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&d))
	assert.Equal(t, "d1", d.ID)
}

func TestGetDeployment_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	mux := newMux(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/deployments/missing", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestApproveDeployment(t *testing.T) {
	h, _, wf := newTestHandler(t)
	mux := newMux(h)

	body, _ := json.Marshal(bluegreen.ApprovalPayload{Note: "LGTM"})
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/d1/approve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, []string{"d1"}, wf.approvals)
}

func TestApproveDeployment_SignalError(t *testing.T) {
	h, _, wf := newTestHandler(t)
	mux := newMux(h)
	wf.signalErr = errors.New("workflow not found")

	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/d1/approve", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRollbackDeployment(t *testing.T) {
	h, _, wf := newTestHandler(t)
	mux := newMux(h)

	body, _ := json.Marshal(bluegreen.RollbackPayload{Reason: "looks bad"})
	req := httptest.NewRequest(http.MethodPost, "/v1/deployments/d1/rollback", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, []string{"d1"}, wf.rollbacks)
}

func TestHealthz(t *testing.T) {
	h, _, _ := newTestHandler(t)
	mux := newMux(h)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
