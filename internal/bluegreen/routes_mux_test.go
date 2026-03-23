package bluegreen_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuxRouteMatching_APIAndUI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	wf := &fakeWorkflowClient{}

	api := bluegreen.NewHandler(wf, logger)
	ui, err := bluegreen.NewUIHandler(nil, wf, bluegreen.NewCustomerApp(), logger)
	require.NoError(t, err)

	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	ui.RegisterUIRoutes(mux)

	cases := []struct {
		name        string
		method      string
		path        string
		wantPattern string
	}{
		{name: "ui-root", method: http.MethodGet, path: "/", wantPattern: "GET /{$}"},
		{name: "ui-short", method: http.MethodGet, path: "/ui", wantPattern: "GET /ui"},
		{name: "ui-slash", method: http.MethodGet, path: "/ui/", wantPattern: "GET /ui/{$}"},
		{name: "ui-state", method: http.MethodGet, path: "/ui/state", wantPattern: "GET /ui/state"},
		{name: "healthz", method: http.MethodGet, path: "/healthz", wantPattern: "GET /healthz"},
		{name: "database", method: http.MethodGet, path: "/v1/database", wantPattern: "GET /v1/database"},
		{name: "deployment-get", method: http.MethodGet, path: "/v1/deployments/demo", wantPattern: "GET /v1/deployments/{id}"},
		{name: "deployment-create", method: http.MethodPost, path: "/v1/deployments", wantPattern: "POST /v1/deployments"},
		{name: "deployment-approve", method: http.MethodPost, path: "/v1/deployments/demo/approve", wantPattern: "POST /v1/deployments/{id}/approve"},
		{name: "deployment-rollback", method: http.MethodPost, path: "/v1/deployments/demo/rollback", wantPattern: "POST /v1/deployments/{id}/rollback"},
		{name: "ui-deploy", method: http.MethodPost, path: "/ui/deploy", wantPattern: "POST /ui/deploy"},
		{name: "ui-approve", method: http.MethodPost, path: "/ui/approve", wantPattern: "POST /ui/approve"},
		{name: "ui-rollback", method: http.MethodPost, path: "/ui/rollback", wantPattern: "POST /ui/rollback"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			_, pattern := mux.Handler(req)
			t.Logf("%s %s -> %s", tc.method, tc.path, pattern)
			assert.Equal(t, tc.wantPattern, pattern)
		})
	}
}

