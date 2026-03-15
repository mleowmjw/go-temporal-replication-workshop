package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"app/internal/api"
	"app/internal/domain"
	"app/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCDCWorkflowStarter records calls and returns configurable results.
type fakeCDCWorkflowStarter struct {
	returnID  string
	returnErr error
}

func (f *fakeCDCWorkflowStarter) StartProvisionCDCWorkflow(_ context.Context, _ domain.PipelineSpec, _ domain.ConnectorConfig) (string, error) {
	if f.returnErr != nil {
		return "", f.returnErr
	}
	id := f.returnID
	if id == "" {
		id = "cdc-workflow-id"
	}
	return id, nil
}

func (f *fakeCDCWorkflowStarter) StartDeleteConnectorWorkflow(_ context.Context, _ domain.PipelineSpec, _ string) (string, error) {
	return f.returnID, f.returnErr
}

func (f *fakeCDCWorkflowStarter) StartPauseConnectorWorkflow(_ context.Context, _ domain.PipelineSpec, _ string) (string, error) {
	return f.returnID, f.returnErr
}

func (f *fakeCDCWorkflowStarter) StartResumeConnectorWorkflow(_ context.Context, _ domain.PipelineSpec, _ string) (string, error) {
	return f.returnID, f.returnErr
}

func newConnectorTestHandler(t *testing.T) (*api.ConnectorHandler, *store.InMemoryMetadataStore, *store.InMemoryConnectorStore, *fakeCDCWorkflowStarter) {
	t.Helper()
	ms := store.NewInMemoryMetadataStore()
	cs := store.NewInMemoryConnectorStore()
	wf := &fakeCDCWorkflowStarter{}
	h := &api.ConnectorHandler{
		Store:          ms,
		ConnectorStore: cs,
		CDCWorkflows:   wf,
		Auth:           api.AllowAllAuthorizer{},
	}
	return h, ms, cs, wf
}

func newConnectorMux(h *api.ConnectorHandler) *http.ServeMux {
	mux := http.NewServeMux()
	h.RegisterConnectorRoutes(mux)
	return mux
}

func seedPipelineForConnector(t *testing.T, ms *store.InMemoryMetadataStore) domain.PipelineSpec {
	t.Helper()
	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	spec := domain.PipelineSpec{
		TenantID:   "t1",
		PipelineID: "p1",
		Name:       "inventory-search",
		Source: domain.SourceSpec{
			Type: domain.SourcePostgres, SecretRef: "s", Tables: []string{"inventory.customers"},
		},
		Sink: domain.SinkSpec{Type: domain.SinkSearch, Target: "customers-index"},
	}
	require.NoError(t, ms.CreatePipeline(context.Background(), spec))
	return spec
}

func validConnectorBody() map[string]any {
	return map[string]any{
		"connector_config": map[string]any{
			"name": "inventory-connector",
			"config": map[string]string{
				"connector.class":   "io.debezium.connector.postgresql.PostgresConnector",
				"database.hostname": "postgres",
			},
		},
	}
}

// --- Create connector ---

func TestCreateConnector_Success(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	mux := newConnectorMux(h)

	b, _ := json.Marshal(validConnectorBody())
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/p1/connectors", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "t1", resp["tenant_id"])
	assert.Equal(t, "p1", resp["pipeline_id"])
	assert.Equal(t, "inventory-connector", resp["connector_name"])
	assert.NotEmpty(t, resp["workflow_id"])
}

func TestCreateConnector_InvalidConfig(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	mux := newConnectorMux(h)

	bad := map[string]any{
		"connector_config": map[string]any{
			"name":   "",
			"config": map[string]string{},
		},
	}
	b, _ := json.Marshal(bad)
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/p1/connectors", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateConnector_PipelineNotFound(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	mux := newConnectorMux(h)

	b, _ := json.Marshal(validConnectorBody())
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/missing/connectors", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestCreateConnector_WorkflowFailure(t *testing.T) {
	h, ms, _, wf := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	wf.returnErr = errors.New("temporal down")
	mux := newConnectorMux(h)

	b, _ := json.Marshal(validConnectorBody())
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/p1/connectors", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// --- List connectors ---

func TestListConnectors(t *testing.T) {
	h, ms, cs, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)

	require.NoError(t, cs.CreateConnector(context.Background(), domain.ConnectorRecord{
		TenantID: "t1", PipelineID: "p1", Name: "c1",
		Config: domain.ConnectorConfig{Name: "c1", Config: map[string]string{"connector.class": "x"}},
		State:  domain.ConnectorStateRunning,
	}))
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t1/pipelines/p1/connectors", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var items []map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "c1", items[0]["name"])
	assert.Equal(t, "RUNNING", items[0]["state"])
}

// --- Get connector ---

func TestGetConnector_Found(t *testing.T) {
	h, ms, cs, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)

	require.NoError(t, cs.CreateConnector(context.Background(), domain.ConnectorRecord{
		TenantID: "t1", PipelineID: "p1", Name: "c1",
		Config: domain.ConnectorConfig{Name: "c1", Config: map[string]string{"connector.class": "x"}},
		State:  domain.ConnectorStateRunning,
	}))
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t1/pipelines/p1/connectors/c1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "c1", resp["connector_name"])
	assert.Equal(t, "RUNNING", resp["state"])
}

func TestGetConnector_NotFound(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t1/pipelines/p1/connectors/missing", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Delete connector ---

func TestDeleteConnector(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodDelete, "/v1/tenants/t1/pipelines/p1/connectors/c1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)
}

// --- Pause / resume ---

func TestPauseConnector(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/p1/connectors/c1/pause", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestResumeConnector(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/p1/connectors/c1/resume", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusAccepted, w.Code)
}

func TestDeleteConnector_PipelineNotFound(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodDelete, "/v1/tenants/t1/pipelines/missing/connectors/c1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPauseConnector_PipelineNotFound(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/missing/connectors/c1/pause", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestResumeConnector_PipelineNotFound(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	require.NoError(t, ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/missing/connectors/c1/resume", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestListConnectors_Empty(t *testing.T) {
	h, ms, _, _ := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	mux := newConnectorMux(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t1/pipelines/p1/connectors", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var items []map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
	assert.Empty(t, items)
}

func TestConnectorWorkflowErrors(t *testing.T) {
	h, ms, _, wf := newConnectorTestHandler(t)
	seedPipelineForConnector(t, ms)
	wf.returnErr = errors.New("workflow error")
	mux := newConnectorMux(h)

	t.Run("delete returns 500 on workflow error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/v1/tenants/t1/pipelines/p1/connectors/c1", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("pause returns 500 on workflow error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/p1/connectors/c1/pause", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("resume returns 500 on workflow error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/pipelines/p1/connectors/c1/resume", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}
