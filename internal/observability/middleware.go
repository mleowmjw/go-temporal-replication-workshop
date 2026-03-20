package observability

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// MetricsMiddleware returns an HTTP middleware that records request duration
// per handler invocation. Labels: method, path (Go 1.22 pattern), status.
func (m *Metrics) MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		elapsed := time.Since(start).Seconds()
		attrs := otelmetric.WithAttributes(
			attribute.String("method", r.Method),
			attribute.String("path", r.Pattern),
			attribute.String("status", strconv.Itoa(rw.statusCode)),
		)
		m.HTTPRequestDuration.Record(context.Background(), elapsed, attrs)
	})
}
