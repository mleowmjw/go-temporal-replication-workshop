package bluegreen

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrDeploymentNotFound is returned when a deployment ID does not exist.
var ErrDeploymentNotFound = errors.New("deployment not found")

// InMemoryDeploymentStore is a thread-safe in-memory implementation of DeploymentStore.
type InMemoryDeploymentStore struct {
	mu          sync.RWMutex
	deployments map[string]Deployment
}

// NewInMemoryDeploymentStore creates an empty store.
func NewInMemoryDeploymentStore() *InMemoryDeploymentStore {
	return &InMemoryDeploymentStore{
		deployments: make(map[string]Deployment),
	}
}

func (s *InMemoryDeploymentStore) Create(ctx context.Context, d Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.deployments[d.ID]; exists {
		return fmt.Errorf("deployment %q already exists", d.ID)
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	if d.Phase == "" {
		d.Phase = PhasePending
	}
	s.deployments[d.ID] = d
	return nil
}

func (s *InMemoryDeploymentStore) Get(ctx context.Context, id string) (Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.deployments[id]
	if !ok {
		return Deployment{}, ErrDeploymentNotFound
	}
	return d, nil
}

func (s *InMemoryDeploymentStore) UpdatePhase(ctx context.Context, id string, phase Phase, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deployments[id]
	if !ok {
		return ErrDeploymentNotFound
	}
	d.Phase = phase
	d.History = append(d.History, PhaseTransition{
		Phase:  phase,
		At:     time.Now(),
		Reason: reason,
	})
	if phase == PhaseComplete || phase == PhaseRolledBack || phase == PhaseFailed {
		now := time.Now()
		d.CompletedAt = &now
	}
	s.deployments[id] = d
	return nil
}
