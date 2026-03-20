package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"app/internal/api"
	"app/internal/domain"
	"app/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)


func newAuditMux(t *testing.T, ms store.MetadataStore, auditStore store.AuditStore) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	h := &api.AuditHandler{
		Store: ms,
		Audit: auditStore,
		Auth:  api.AllowAllAuthorizer{},
	}
	h.RegisterAuditRoutes(mux)
	api.RegisterHealthRoute(mux)
	return mux
}

// --- AuditingWorkflowStarter tests ---

func TestAuditingWorkflowStarter_RecordsCreateEvent(t *testing.T) {
	auditStore := store.NewInMemoryAuditStore()
	inner := &fakeWorkflowStarter{returnID: "wf-123"}
	auditing := api.NewAuditingWorkflowStarter(inner, auditStore, "tester")

	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	id, err := auditing.StartProvisionWorkflow(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "wf-123", id)

	events, err := auditStore.ListEvents(context.Background(), "t1", "p1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, domain.AuditActionCreate, events[0].Action)
	assert.Equal(t, "tester", events[0].Actor)
	assert.Equal(t, "wf-123", events[0].Details["workflow_id"])
}

func TestAuditingWorkflowStarter_DoesNotRecordOnError(t *testing.T) {
	auditStore := store.NewInMemoryAuditStore()
	inner := &fakeWorkflowStarter{returnErr: errors.New("boom")}
	auditing := api.NewAuditingWorkflowStarter(inner, auditStore, "tester")

	_, err := auditing.StartProvisionWorkflow(context.Background(), domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"})
	require.Error(t, err)

	events, err := auditStore.ListEvents(context.Background(), "t1", "p1")
	require.NoError(t, err)
	assert.Empty(t, events)
}

// --- AuditingCDCWorkflowStarter tests ---

func TestAuditingCDCWorkflowStarter_RecordsProvisionEvent(t *testing.T) {
	auditStore := store.NewInMemoryAuditStore()
	inner := &fakeCDCWorkflowStarter{returnID: "prov-wf"}
	auditing := api.NewAuditingCDCWorkflowStarter(inner, auditStore, "tester")

	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	cfg := domain.ConnectorConfig{Name: "my-connector"}
	id, err := auditing.StartProvisionCDCWorkflow(context.Background(), spec, cfg)
	require.NoError(t, err)
	assert.Equal(t, "prov-wf", id)

	events, err := auditStore.ListEvents(context.Background(), "t1", "p1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, domain.AuditActionConnectorCreate, events[0].Action)
	assert.Equal(t, "my-connector", events[0].Details["connector"])
}

func TestAuditingCDCWorkflowStarter_RecordsPauseEvent(t *testing.T) {
	auditStore := store.NewInMemoryAuditStore()
	inner := &fakeCDCWorkflowStarter{returnID: "pause-wf"}
	auditing := api.NewAuditingCDCWorkflowStarter(inner, auditStore, "tester")

	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	id, err := auditing.StartPauseConnectorWorkflow(context.Background(), spec, "my-connector")
	require.NoError(t, err)
	assert.Equal(t, "pause-wf", id)

	events, err := auditStore.ListEvents(context.Background(), "t1", "p1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, domain.AuditActionConnectorPause, events[0].Action)
	assert.Equal(t, "my-connector", events[0].Details["connector"])
}

func TestAuditingCDCWorkflowStarter_RecordsResumeEvent(t *testing.T) {
	auditStore := store.NewInMemoryAuditStore()
	inner := &fakeCDCWorkflowStarter{returnID: "resume-wf"}
	auditing := api.NewAuditingCDCWorkflowStarter(inner, auditStore, "tester")

	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	_, err := auditing.StartResumeConnectorWorkflow(context.Background(), spec, "my-connector")
	require.NoError(t, err)

	events, err := auditStore.ListEvents(context.Background(), "t1", "p1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, domain.AuditActionConnectorResume, events[0].Action)
}

func TestAuditingCDCWorkflowStarter_RecordsDecommissionEvent(t *testing.T) {
	auditStore := store.NewInMemoryAuditStore()
	inner := &fakeCDCWorkflowStarter{returnID: "decomm-wf"}
	auditing := api.NewAuditingCDCWorkflowStarter(inner, auditStore, "tester")

	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"}
	_, err := auditing.StartDeleteConnectorWorkflow(context.Background(), spec, "my-connector")
	require.NoError(t, err)

	events, err := auditStore.ListEvents(context.Background(), "t1", "p1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, domain.AuditActionConnectorDecommission, events[0].Action)
}

// --- HTTP handler tests ---

func TestListAuditEvents_HappyPath(t *testing.T) {
	ms := store.NewInMemoryMetadataStore()
	auditStore := store.NewInMemoryAuditStore()
	ctx := context.Background()

	require.NoError(t, ms.CreateTenant(ctx, domain.Tenant{ID: "t1", Name: "T1"}))
	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1", Name: "Test"}
	require.NoError(t, ms.CreatePipeline(ctx, spec))

	require.NoError(t, auditStore.RecordEvent(ctx, domain.AuditEvent{
		TenantID:   "t1",
		PipelineID: "p1",
		Action:     domain.AuditActionCreate,
		Actor:      "user@example.com",
		Timestamp:  time.Now().UTC(),
	}))

	mux := newAuditMux(t, ms, auditStore)
	w := getRequest(t, mux, "/v1/tenants/t1/pipelines/p1/audit")
	assert.Equal(t, http.StatusOK, w.Code)

	var events []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&events))
	require.Len(t, events, 1)
	assert.Equal(t, "pipeline.create", events[0]["action"])
	assert.Equal(t, "user@example.com", events[0]["actor"])
}

func TestListAuditEvents_PipelineNotFound(t *testing.T) {
	ms := store.NewInMemoryMetadataStore()
	auditStore := store.NewInMemoryAuditStore()
	ctx := context.Background()
	require.NoError(t, ms.CreateTenant(ctx, domain.Tenant{ID: "t1", Name: "T1"}))

	mux := newAuditMux(t, ms, auditStore)
	w := getRequest(t, mux, "/v1/tenants/t1/pipelines/nonexistent/audit")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestListAuditEvents_Empty(t *testing.T) {
	ms := store.NewInMemoryMetadataStore()
	auditStore := store.NewInMemoryAuditStore()
	ctx := context.Background()

	require.NoError(t, ms.CreateTenant(ctx, domain.Tenant{ID: "t1", Name: "T1"}))
	spec := domain.PipelineSpec{TenantID: "t1", PipelineID: "p1", Name: "Test"}
	require.NoError(t, ms.CreatePipeline(ctx, spec))

	mux := newAuditMux(t, ms, auditStore)
	w := getRequest(t, mux, "/v1/tenants/t1/pipelines/p1/audit")
	assert.Equal(t, http.StatusOK, w.Code)

	var events []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&events))
	assert.Empty(t, events)
}

// --- Health endpoint ---

func TestHealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	api.RegisterHealthRoute(mux)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "ok", body["status"])
}
