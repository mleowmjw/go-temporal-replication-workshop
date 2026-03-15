package connectors

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"app/internal/domain"
)

// InMemorySecretProvider resolves secrets from an in-memory map.
type InMemorySecretProvider struct {
	mu      sync.RWMutex
	secrets map[string]map[string]string
}

func NewInMemorySecretProvider() *InMemorySecretProvider {
	return &InMemorySecretProvider{secrets: make(map[string]map[string]string)}
}

func (p *InMemorySecretProvider) Set(ref string, values map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secrets[ref] = values
}

func (p *InMemorySecretProvider) Resolve(_ context.Context, secretRef string) (map[string]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.secrets[secretRef]
	if !ok {
		return nil, fmt.Errorf("secret %q not found", secretRef)
	}
	return v, nil
}

// FakeCDCProvisioner simulates CDC provisioning. Inject error functions for failure scenarios.
type FakeCDCProvisioner struct {
	mu sync.Mutex

	// Injectable failure functions — return nil for success.
	PrepareSourceFn func(spec domain.SourceSpec) error
	StartCaptureFn  func(spec domain.SourceSpec) (CaptureHandle, error)
	StopCaptureFn   func(h CaptureHandle) error

	Captures []CaptureHandle
}

func NewFakeCDCProvisioner() *FakeCDCProvisioner {
	return &FakeCDCProvisioner{}
}

func (f *FakeCDCProvisioner) PrepareSource(_ context.Context, spec domain.SourceSpec) error {
	if f.PrepareSourceFn != nil {
		return f.PrepareSourceFn(spec)
	}
	return nil
}

func (f *FakeCDCProvisioner) StartCapture(_ context.Context, spec domain.SourceSpec) (CaptureHandle, error) {
	if f.StartCaptureFn != nil {
		return f.StartCaptureFn(spec)
	}
	h := CaptureHandle{ID: fmt.Sprintf("cap-%s-%s", spec.Database, spec.Schema)}
	f.mu.Lock()
	f.Captures = append(f.Captures, h)
	f.mu.Unlock()
	return h, nil
}

func (f *FakeCDCProvisioner) StopCapture(_ context.Context, h CaptureHandle) error {
	if f.StopCaptureFn != nil {
		return f.StopCaptureFn(h)
	}
	return nil
}

// FakeStreamProvisioner simulates stream provisioning.
type FakeStreamProvisioner struct {
	mu sync.Mutex

	EnsureStreamFn func(tenant domain.TenantID, pipeline domain.PipelineID, subject string) (string, error)
	DeleteStreamFn func(streamID string) error

	Streams []string
}

func NewFakeStreamProvisioner() *FakeStreamProvisioner {
	return &FakeStreamProvisioner{}
}

func (f *FakeStreamProvisioner) EnsureStream(_ context.Context, tenant domain.TenantID, pipeline domain.PipelineID, subject string) (string, error) {
	if f.EnsureStreamFn != nil {
		return f.EnsureStreamFn(tenant, pipeline, subject)
	}
	id := fmt.Sprintf("stream-%s-%s", tenant, pipeline)
	f.mu.Lock()
	f.Streams = append(f.Streams, id)
	f.mu.Unlock()
	return id, nil
}

func (f *FakeStreamProvisioner) DeleteStream(_ context.Context, streamID string) error {
	if f.DeleteStreamFn != nil {
		return f.DeleteStreamFn(streamID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.Streams {
		if s == streamID {
			f.Streams = append(f.Streams[:i], f.Streams[i+1:]...)
			return nil
		}
	}
	return nil
}

// FakeSinkProvisioner simulates sink provisioning.
type FakeSinkProvisioner struct {
	mu sync.Mutex

	// EnsureSinkFn can be replaced per-call using a call counter for transient failure simulation.
	EnsureSinkFn func(callN int, spec domain.SinkSpec) (string, error)
	DeleteSinkFn func(sinkID string) error

	Sinks    []string
	callsN   atomic.Int64
}

func NewFakeSinkProvisioner() *FakeSinkProvisioner {
	return &FakeSinkProvisioner{}
}

func (f *FakeSinkProvisioner) EnsureSink(_ context.Context, spec domain.SinkSpec) (string, error) {
	n := int(f.callsN.Add(1))
	if f.EnsureSinkFn != nil {
		return f.EnsureSinkFn(n, spec)
	}
	id := fmt.Sprintf("sink-%s-%s", spec.Type, spec.Target)
	f.mu.Lock()
	f.Sinks = append(f.Sinks, id)
	f.mu.Unlock()
	return id, nil
}

func (f *FakeSinkProvisioner) DeleteSink(_ context.Context, sinkID string) error {
	if f.DeleteSinkFn != nil {
		return f.DeleteSinkFn(sinkID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.Sinks {
		if s == sinkID {
			f.Sinks = append(f.Sinks[:i], f.Sinks[i+1:]...)
			return nil
		}
	}
	return nil
}
