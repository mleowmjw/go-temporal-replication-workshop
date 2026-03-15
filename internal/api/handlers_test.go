package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"app/internal/api"
	"app/internal/domain"
	"app/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// denyAllAuthorizer rejects every request.
type denyAllAuthorizer struct{}

func (*denyAllAuthorizer) Authorize(_ *http.Request, _ domain.TenantID, _ string) error {
	return errors.New("forbidden")
}

// fakeWorkflowStarter records calls for assertions.
type fakeWorkflowStarter struct {
	lastSpec   domain.PipelineSpec
	returnID   string
	returnErr  error
}

func (f *fakeWorkflowStarter) StartProvisionWorkflow(_ context.Context, spec domain.PipelineSpec) (string, error) {
	f.lastSpec = spec
	if f.returnErr != nil {
		return "", f.returnErr
	}
	id := "workflow-" + string(spec.PipelineID)
	if f.returnID != "" {
		id = f.returnID
	}
	return id, nil
}

func newTestHandler(t *testing.T) (*api.Handler, *store.InMemoryMetadataStore, *fakeWorkflowStarter) {
	t.Helper()
	ms := store.NewInMemoryMetadataStore()
	wf := &fakeWorkflowStarter{}
	h := api.NewHandler(ms, wf, api.AllowAllAuthorizer{}, slog.Default())
	return h, ms, wf
}

func newMux(h *api.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func postJSON(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func getRequest(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// --- Tenant tests ---

func TestCreateTenant(t *testing.T) {
	h, _, _ := newTestHandler(t)
	mux := newMux(h)

	t.Run("success", func(t *testing.T) {
		w := postJSON(t, mux, "/v1/tenants", map[string]string{
			"id": "t1", "name": "Acme", "tier": "standard",
		})
		assert.Equal(t, http.StatusCreated, w.Code)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "t1", resp["id"])
	})

	t.Run("duplicate returns 409", func(t *testing.T) {
		w := postJSON(t, mux, "/v1/tenants", map[string]string{
			"id": "t1", "name": "Acme", "tier": "standard",
		})
		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("invalid body returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/tenants", bytes.NewBufferString("not-json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing id returns 400", func(t *testing.T) {
		w := postJSON(t, mux, "/v1/tenants", map[string]string{"name": "Acme"})
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// --- Pipeline tests ---

func validPipelineBody() map[string]any {
	return map[string]any{
		"pipeline_id": "p1",
		"name":        "inventory-search",
		"source": map[string]any{
			"type":       "postgres",
			"secret_ref": "pg-secret",
			"tables":     []string{"inventory.customers"},
		},
		"sink": map[string]any{
			"type":   "search",
			"target": "customers-index",
		},
	}
}

func TestCreatePipeline(t *testing.T) {
	h, ms, wf := newTestHandler(t)
	mux := newMux(h)

	// Pre-create tenant.
	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))

	t.Run("success returns 202", func(t *testing.T) {
		w := postJSON(t, mux, "/v1/tenants/t1/pipelines", validPipelineBody())
		assert.Equal(t, http.StatusAccepted, w.Code)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "t1", resp["tenant_id"])
		assert.Equal(t, "p1", resp["pipeline_id"])
		assert.NotEmpty(t, resp["workflow_id"])
		assert.Equal(t, "t1", string(wf.lastSpec.TenantID))
	})

	t.Run("duplicate pipeline returns 409", func(t *testing.T) {
		w := postJSON(t, mux, "/v1/tenants/t1/pipelines", validPipelineBody())
		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("invalid spec returns 400", func(t *testing.T) {
		bad := map[string]any{"pipeline_id": "p99", "name": "x"}
		w := postJSON(t, mux, "/v1/tenants/t1/pipelines", bad)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestGetPipeline(t *testing.T) {
	h, ms, _ := newTestHandler(t)
	mux := newMux(h)

	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	spec := domain.PipelineSpec{
		TenantID:   "t1",
		PipelineID: "p1",
		Name:       "test",
		Source: domain.SourceSpec{
			Type: domain.SourcePostgres, SecretRef: "s", Tables: []string{"t"},
		},
		Sink: domain.SinkSpec{Type: domain.SinkSearch, Target: "idx"},
	}
	require.NoError(t, ms.CreatePipeline(context.Background(), spec))

	t.Run("found returns 200 with state", func(t *testing.T) {
		w := getRequest(t, mux, "/v1/tenants/t1/pipelines/p1")
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, "p1", resp["pipeline_id"])
		assert.Equal(t, string(domain.StatePending), resp["state"])
	})

	t.Run("not found returns 404", func(t *testing.T) {
		w := getRequest(t, mux, "/v1/tenants/t1/pipelines/missing")
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestGetPipeline_AuthFailure(t *testing.T) {
	ms := store.NewInMemoryMetadataStore()
	wf := &fakeWorkflowStarter{}
	// Use a deny-all authorizer.
	h := api.NewHandler(ms, wf, &denyAllAuthorizer{}, slog.Default())
	mux := newMux(h)
	w := getRequest(t, mux, "/v1/tenants/t1/pipelines/p1")
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestCreatePipeline_WorkflowFailure(t *testing.T) {
	h, ms, wf := newTestHandler(t)
	mux := newMux(h)

	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))

	wf.returnErr = errors.New("temporal unavailable")
	wf.returnID = ""

	w := postJSON(t, mux, "/v1/tenants/t1/pipelines", validPipelineBody())
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestNewHandler_NilDefaults(t *testing.T) {
	ms := store.NewInMemoryMetadataStore()
	wf := &fakeWorkflowStarter{}
	// Passing nil for auth and log should not panic.
	h := api.NewHandler(ms, wf, nil, nil)
	mux := newMux(h)

	// Tenant creation should still work.
	w := postJSON(t, mux, "/v1/tenants", map[string]string{"id": "x1", "name": "X"})
	assert.Equal(t, http.StatusCreated, w.Code)
}
