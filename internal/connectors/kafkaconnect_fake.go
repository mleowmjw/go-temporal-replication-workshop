package connectors

import (
	"context"
	"fmt"
	"sync"

	"app/internal/domain"
)

// FakeKafkaConnectClient is an in-memory implementation of KafkaConnectClient for tests.
// All injectable Fn fields default to happy-path behaviour when nil.
type FakeKafkaConnectClient struct {
	mu sync.RWMutex

	// Injectable failure overrides — return nil/empty for default success.
	CreateConnectorFn    func(cfg domain.ConnectorConfig) error
	GetConnectorStatusFn func(name string) (domain.ConnectorStatus, error)
	DeleteConnectorFn    func(name string) error
	ListConnectorsFn     func() ([]string, error)
	PauseConnectorFn     func(name string) error
	ResumeConnectorFn    func(name string) error

	// Internal state.
	connectors map[string]domain.ConnectorStatus
}

// NewFakeKafkaConnectClient creates a client that starts RUNNING on creation.
func NewFakeKafkaConnectClient() *FakeKafkaConnectClient {
	return &FakeKafkaConnectClient{
		connectors: make(map[string]domain.ConnectorStatus),
	}
}

func (f *FakeKafkaConnectClient) CreateConnector(_ context.Context, cfg domain.ConnectorConfig) error {
	if f.CreateConnectorFn != nil {
		return f.CreateConnectorFn(cfg)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.connectors[cfg.Name]; ok {
		return fmt.Errorf("connector %q already exists", cfg.Name)
	}
	f.connectors[cfg.Name] = domain.ConnectorStatus{
		Name:     cfg.Name,
		State:    domain.ConnectorStateRunning,
		WorkerID: "fake-worker:8083",
		Tasks:    []domain.TaskStatus{{ID: 0, State: "RUNNING"}},
	}
	return nil
}

func (f *FakeKafkaConnectClient) GetConnectorStatus(_ context.Context, name string) (domain.ConnectorStatus, error) {
	if f.GetConnectorStatusFn != nil {
		return f.GetConnectorStatusFn(name)
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	s, ok := f.connectors[name]
	if !ok {
		return domain.ConnectorStatus{}, fmt.Errorf("connector %q: %w", name, ErrNotFound)
	}
	return s, nil
}

func (f *FakeKafkaConnectClient) DeleteConnector(_ context.Context, name string) error {
	if f.DeleteConnectorFn != nil {
		return f.DeleteConnectorFn(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.connectors, name)
	return nil
}

func (f *FakeKafkaConnectClient) ListConnectors(_ context.Context) ([]string, error) {
	if f.ListConnectorsFn != nil {
		return f.ListConnectorsFn()
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	names := make([]string, 0, len(f.connectors))
	for n := range f.connectors {
		names = append(names, n)
	}
	return names, nil
}

func (f *FakeKafkaConnectClient) PauseConnector(_ context.Context, name string) error {
	if f.PauseConnectorFn != nil {
		return f.PauseConnectorFn(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.connectors[name]
	if !ok {
		return fmt.Errorf("connector %q: %w", name, ErrNotFound)
	}
	s.State = domain.ConnectorStatePaused
	f.connectors[name] = s
	return nil
}

func (f *FakeKafkaConnectClient) ResumeConnector(_ context.Context, name string) error {
	if f.ResumeConnectorFn != nil {
		return f.ResumeConnectorFn(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.connectors[name]
	if !ok {
		return fmt.Errorf("connector %q: %w", name, ErrNotFound)
	}
	s.State = domain.ConnectorStateRunning
	f.connectors[name] = s
	return nil
}

// SetState lets tests directly set a connector's state for controlled scenarios.
func (f *FakeKafkaConnectClient) SetState(name string, state domain.ConnectorState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.connectors[name]
	s.State = state
	f.connectors[name] = s
}

// ConnectorNames returns the current set of connector names (for assertions).
func (f *FakeKafkaConnectClient) ConnectorNames() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	names := make([]string, 0, len(f.connectors))
	for n := range f.connectors {
		names = append(names, n)
	}
	return names
}
