package bluegreen

import "context"

// DeploymentStore persists and retrieves blue-green deployment records.
type DeploymentStore interface {
	// Create records a new deployment. Returns error if ID already exists.
	Create(ctx context.Context, d Deployment) error

	// Get retrieves a deployment by ID. Returns ErrDeploymentNotFound if missing.
	Get(ctx context.Context, id string) (Deployment, error)

	// UpdatePhase appends a phase transition to the deployment history and
	// sets the current phase. Returns ErrDeploymentNotFound if missing.
	UpdatePhase(ctx context.Context, id string, phase Phase, reason string) error
}
