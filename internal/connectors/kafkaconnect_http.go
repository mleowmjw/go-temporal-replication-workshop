package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"app/internal/domain"
)

// HTTPKafkaConnectClient calls the real Kafka Connect REST API.
type HTTPKafkaConnectClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewHTTPKafkaConnectClient creates a client targeting the given base URL
// (e.g. "http://localhost:8083").
func NewHTTPKafkaConnectClient(baseURL string) *HTTPKafkaConnectClient {
	return &HTTPKafkaConnectClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *HTTPKafkaConnectClient) CreateConnector(ctx context.Context, cfg domain.ConnectorConfig) error {
	body := map[string]any{
		"name":   cfg.Name,
		"config": cfg.Config,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal connector config: %w", err)
	}
	resp, err := c.doRequest(ctx, http.MethodPost, "/connectors", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("connector %q already exists", cfg.Name)
	}
	if resp.StatusCode != http.StatusCreated {
		return c.responseError("create connector", resp)
	}
	return nil
}

// kcConnectorStatusResponse maps the Kafka Connect status JSON response.
type kcConnectorStatusResponse struct {
	Name      string `json:"name"`
	Connector struct {
		State    string `json:"state"`
		WorkerID string `json:"worker_id"`
	} `json:"connector"`
	Tasks []struct {
		ID       int    `json:"id"`
		State    string `json:"state"`
		WorkerID string `json:"worker_id"`
		Trace    string `json:"trace,omitempty"`
	} `json:"tasks"`
}

func (c *HTTPKafkaConnectClient) GetConnectorStatus(ctx context.Context, name string) (domain.ConnectorStatus, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, connectorPath(name, "/status"), nil)
	if err != nil {
		return domain.ConnectorStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return domain.ConnectorStatus{}, fmt.Errorf("connector %q: %w", name, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return domain.ConnectorStatus{}, c.responseError("get connector status", resp)
	}
	var raw kcConnectorStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return domain.ConnectorStatus{}, fmt.Errorf("decode connector status: %w", err)
	}
	tasks := make([]domain.TaskStatus, len(raw.Tasks))
	for i, t := range raw.Tasks {
		tasks[i] = domain.TaskStatus{ID: t.ID, State: t.State, Trace: t.Trace}
	}
	return domain.ConnectorStatus{
		Name:     raw.Name,
		State:    domain.ConnectorState(raw.Connector.State),
		WorkerID: raw.Connector.WorkerID,
		Tasks:    tasks,
	}, nil
}

func (c *HTTPKafkaConnectClient) DeleteConnector(ctx context.Context, name string) error {
	resp, err := c.doRequest(ctx, http.MethodDelete, connectorPath(name, ""), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // idempotent
	}
	if resp.StatusCode != http.StatusNoContent {
		return c.responseError("delete connector", resp)
	}
	return nil
}

func (c *HTTPKafkaConnectClient) ListConnectors(ctx context.Context) ([]string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/connectors", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.responseError("list connectors", resp)
	}
	var names []string
	if err := json.NewDecoder(resp.Body).Decode(&names); err != nil {
		return nil, fmt.Errorf("decode connectors list: %w", err)
	}
	return names, nil
}

func (c *HTTPKafkaConnectClient) PauseConnector(ctx context.Context, name string) error {
	resp, err := c.doRequest(ctx, http.MethodPut, connectorPath(name, "/pause"), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return c.responseError("pause connector", resp)
	}
	return nil
}

func (c *HTTPKafkaConnectClient) ResumeConnector(ctx context.Context, name string) error {
	resp, err := c.doRequest(ctx, http.MethodPut, connectorPath(name, "/resume"), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return c.responseError("resume connector", resp)
	}
	return nil
}

func (c *HTTPKafkaConnectClient) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	return resp, nil
}

func (c *HTTPKafkaConnectClient) responseError(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s: HTTP %d: %s", op, resp.StatusCode, string(b))
}

func connectorPath(name, suffix string) string {
	return "/connectors/" + url.PathEscape(name) + suffix
}
