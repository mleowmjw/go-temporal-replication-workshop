package observability_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"app/internal/observability"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsMiddleware_RecordsRequestDuration(t *testing.T) {
	m, err := observability.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.MeterProvider.Shutdown(t.Context()) })

	handler := m.MetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/t1/pipelines", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	// Collect metrics and verify duration histogram was recorded.
	metReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metRR := httptest.NewRecorder()
	m.Handler().ServeHTTP(metRR, metReq)

	body, err := io.ReadAll(metRR.Body)
	require.NoError(t, err)
	bodyStr := string(body)
	assert.True(t,
		strings.Contains(bodyStr, "replication_http_request_duration_seconds"),
		"expected duration histogram in output, got: %s", bodyStr,
	)
}

func TestMetricsMiddleware_CapturesNonOKStatus(t *testing.T) {
	m, err := observability.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.MeterProvider.Shutdown(t.Context()) })

	handler := m.MetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}
