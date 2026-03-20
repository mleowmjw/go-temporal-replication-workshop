// Package observability wires Prometheus metrics for the CDC replication platform.
//
// It provides:
//   - A shared prometheus.Registry
//   - An OTel MeterProvider backed by that registry (for Temporal SDK metrics)
//   - A Temporal client.MetricsHandler built from the OTel meter
//   - App-level counters/histograms (pipeline operations, HTTP requests)
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/client"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Metrics holds all shared observability state.
type Metrics struct {
	// Registry is the Prometheus registry. Register additional collectors here.
	Registry *prometheus.Registry

	// MeterProvider is the OTel provider; close it on shutdown.
	MeterProvider *sdkmetric.MeterProvider

	// Meter can be used to create additional OTel instruments.
	Meter metric.Meter

	// TemporalHandler is the Temporal MetricsHandler to pass to client.Options.
	TemporalHandler client.MetricsHandler

	// PipelineOps counts pipeline lifecycle actions (label: action).
	PipelineOps metric.Int64Counter

	// HTTPRequestDuration is a histogram for HTTP handler latency (labels: method, path, status).
	HTTPRequestDuration metric.Float64Histogram
}

// New creates and wires the full metrics stack.
// The caller must call MeterProvider.Shutdown on exit.
func New() (*Metrics, error) {
	reg := prometheus.NewRegistry()

	// OTel Prometheus exporter — exports into our registry.
	exporter, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	meter := mp.Meter("replication-platform")

	pipelineOps, err := meter.Int64Counter(
		"replication_pipeline_operations_total",
		metric.WithDescription("Total number of pipeline lifecycle operations"),
	)
	if err != nil {
		return nil, err
	}

	httpDuration, err := meter.Float64Histogram(
		"replication_http_request_duration_seconds",
		metric.WithDescription("HTTP request latency in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	th := temporalotel.NewMetricsHandler(temporalotel.MetricsHandlerOptions{Meter: meter})

	return &Metrics{
		Registry:            reg,
		MeterProvider:       mp,
		Meter:               meter,
		TemporalHandler:     th,
		PipelineOps:         pipelineOps,
		HTTPRequestDuration: httpDuration,
	}, nil
}

// Handler returns an HTTP handler that serves Prometheus metrics from the registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
