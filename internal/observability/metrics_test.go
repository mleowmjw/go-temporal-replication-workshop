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
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

func newTestMetrics(t *testing.T) *observability.Metrics {
	t.Helper()
	m, err := observability.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.MeterProvider.Shutdown(t.Context()) })
	return m
}

func TestNew_ReturnsMetrics(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotNil(t, m.Registry)
	assert.NotNil(t, m.MeterProvider)
	assert.NotNil(t, m.Meter)
	assert.NotNil(t, m.TemporalHandler)
	assert.NotNil(t, m.PipelineOps)
	assert.NotNil(t, m.HTTPRequestDuration)
}

func TestMetrics_Handler_ServesPrometheus(t *testing.T) {
	m := newTestMetrics(t)

	// Increment the counter so there's at least one metric to emit.
	m.PipelineOps.Add(t.Context(), 1,
		otelmetric.WithAttributes(attribute.String("action", "create")),
	)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	body, err := io.ReadAll(rr.Body)
	require.NoError(t, err)

	// Should contain standard Prometheus text format markers.
	bodyStr := string(body)
	assert.True(t, strings.Contains(bodyStr, "replication_pipeline_operations_total"),
		"expected pipeline ops counter in output, got: %s", bodyStr)
}

func TestMetrics_TemporalHandler_NotNil(t *testing.T) {
	m := newTestMetrics(t)
	// Temporal handler should implement client.MetricsHandler interface.
	assert.NotNil(t, m.TemporalHandler)
	// Exercise the handler to ensure it doesn't panic.
	counter := m.TemporalHandler.WithTags(map[string]string{"tenant": "t1"}).Counter("test_counter")
	counter.Inc(1)
}
