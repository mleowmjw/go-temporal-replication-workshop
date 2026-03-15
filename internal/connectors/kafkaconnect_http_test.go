package connectors_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"app/internal/connectors"
	"app/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestServer creates an httptest.Server with a minimal Kafka Connect API stub.
func buildTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	stored := make(map[string]domain.ConnectorStatus)

	mux := http.NewServeMux()

	// POST /connectors — create
	mux.HandleFunc("POST /connectors", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string            `json:"name"`
			Config map[string]string `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if _, exists := stored[body.Name]; exists {
			w.WriteHeader(http.StatusConflict)
			return
		}
		stored[body.Name] = domain.ConnectorStatus{
			Name:  body.Name,
			State: domain.ConnectorStateRunning,
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(body)
	})

	// GET /connectors — list
	mux.HandleFunc("GET /connectors", func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(stored))
		for n := range stored {
			names = append(names, n)
		}
		_ = json.NewEncoder(w).Encode(names)
	})

	// GET /connectors/{name}/status
	mux.HandleFunc("GET /connectors/{name}/status", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s, ok := stored[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		resp := map[string]any{
			"name": s.Name,
			"connector": map[string]string{
				"state":     string(s.State),
				"worker_id": "worker:8083",
			},
			"tasks": []map[string]any{
				{"id": 0, "state": "RUNNING", "worker_id": "worker:8083"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// DELETE /connectors/{name}
	mux.HandleFunc("DELETE /connectors/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if _, ok := stored[name]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(stored, name)
		w.WriteHeader(http.StatusNoContent)
	})

	// PUT /connectors/{name}/pause
	mux.HandleFunc("PUT /connectors/{name}/pause", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s, ok := stored[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.State = domain.ConnectorStatePaused
		stored[name] = s
		w.WriteHeader(http.StatusAccepted)
	})

	// PUT /connectors/{name}/resume
	mux.HandleFunc("PUT /connectors/{name}/resume", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s, ok := stored[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.State = domain.ConnectorStateRunning
		stored[name] = s
		w.WriteHeader(http.StatusAccepted)
	})

	return httptest.NewServer(mux)
}

func TestHTTPKafkaConnectClient_HappyPath(t *testing.T) {
	srv := buildTestServer(t)
	defer srv.Close()

	c := connectors.NewHTTPKafkaConnectClient(srv.URL)
	cfg := validConnectorCfg()

	require.NoError(t, c.CreateConnector(ctx, cfg))

	names, err := c.ListConnectors(ctx)
	require.NoError(t, err)
	assert.Contains(t, names, cfg.Name)

	status, err := c.GetConnectorStatus(ctx, cfg.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateRunning, status.State)
	assert.Equal(t, cfg.Name, status.Name)
	assert.Len(t, status.Tasks, 1)

	require.NoError(t, c.PauseConnector(ctx, cfg.Name))
	status, err = c.GetConnectorStatus(ctx, cfg.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStatePaused, status.State)

	require.NoError(t, c.ResumeConnector(ctx, cfg.Name))
	status, err = c.GetConnectorStatus(ctx, cfg.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateRunning, status.State)

	require.NoError(t, c.DeleteConnector(ctx, cfg.Name))
	names, err = c.ListConnectors(ctx)
	require.NoError(t, err)
	assert.NotContains(t, names, cfg.Name)
}

func TestHTTPKafkaConnectClient_DuplicateCreate(t *testing.T) {
	srv := buildTestServer(t)
	defer srv.Close()

	c := connectors.NewHTTPKafkaConnectClient(srv.URL)
	cfg := validConnectorCfg()
	require.NoError(t, c.CreateConnector(ctx, cfg))
	err := c.CreateConnector(ctx, cfg)
	assert.ErrorContains(t, err, "already exists")
}

func TestHTTPKafkaConnectClient_GetStatusNotFound(t *testing.T) {
	srv := buildTestServer(t)
	defer srv.Close()

	c := connectors.NewHTTPKafkaConnectClient(srv.URL)
	_, err := c.GetConnectorStatus(ctx, "missing")
	assert.ErrorIs(t, err, connectors.ErrNotFound)
}

func TestHTTPKafkaConnectClient_DeleteIdempotent(t *testing.T) {
	srv := buildTestServer(t)
	defer srv.Close()

	c := connectors.NewHTTPKafkaConnectClient(srv.URL)
	// Deleting a non-existent connector returns 404 → treated as idempotent.
	require.NoError(t, c.DeleteConnector(ctx, "nonexistent"))
}

func TestHTTPKafkaConnectClient_ServerError(t *testing.T) {
	// Server that always returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := connectors.NewHTTPKafkaConnectClient(srv.URL)

	err := c.CreateConnector(ctx, validConnectorCfg())
	assert.ErrorContains(t, err, "500")

	_, err = c.ListConnectors(ctx)
	assert.ErrorContains(t, err, "500")
}
