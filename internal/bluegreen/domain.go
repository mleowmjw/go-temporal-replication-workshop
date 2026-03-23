package bluegreen

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Phase represents the current stage of a blue-green deployment.
type Phase string

const (
	PhasePending      Phase = "pending"
	PhasePlanReview   Phase = "plan_review"
	PhaseExpanding    Phase = "expanding"
	PhaseExpandVerify Phase = "expand_verify"
	PhaseCutover      Phase = "cutover"
	PhaseMonitoring   Phase = "monitoring"
	PhaseContractWait Phase = "contract_wait"
	PhaseContracting  Phase = "contracting"
	PhaseComplete     Phase = "complete"
	PhaseRolledBack   Phase = "rolled_back"
	PhaseFailed       Phase = "failed"
)

// DefaultReadOnlyMaxDuration is the maximum time the database stays read-only during cutover.
const DefaultReadOnlyMaxDuration = 30 * time.Second

// DatabaseOpsWorkflowIDPrefix is the prefix for per-database coordinator workflow IDs.
const DatabaseOpsWorkflowIDPrefix = "db-ops-"

// UpdateRequestDeployment is the Temporal Update name used to acquire the schema lock.
const UpdateRequestDeployment = "request_deployment"

// SignalDeploymentComplete is the signal name sent by a deployment workflow to its
// parent DatabaseOpsWorkflow when it finishes (complete, rolled_back, or failed).
const SignalDeploymentComplete = "deployment_complete"

// QueryDatabaseOpsState is the Temporal query type for reading the coordinator state.
const QueryDatabaseOpsState = "database_ops_state"

// ContinueAsNewInterval is how long the DatabaseOpsWorkflow runs before continuing-as-new.
const ContinueAsNewInterval = 24 * time.Hour

// Environment identifies the deployment environment, which controls the schema lock timeout.
type Environment string

const (
	EnvDev     Environment = "dev"
	EnvStaging Environment = "staging"
	EnvProd    Environment = "prod"
)

// LockTimeout returns the maximum duration a schema lock can be held in this environment.
// dev=1m, staging=15m, prod=24h.
func (e Environment) LockTimeout() time.Duration {
	switch e {
	case EnvStaging:
		return 15 * time.Minute
	case EnvProd:
		return 24 * time.Hour
	default: // dev and anything unrecognised
		return 1 * time.Minute
	}
}

// DatabaseOpsConfig is the input to DatabaseOpsWorkflow.
type DatabaseOpsConfig struct {
	// DatabaseID is the opaque identifier derived from the connection URL fingerprint.
	DatabaseID  string
	Environment Environment
}

// ActiveDeployment describes a currently locked deployment.
type ActiveDeployment struct {
	PlanID     string
	WorkflowID string
	StartedAt  time.Time
}

// CompletedDeployment is an entry in the DatabaseOpsWorkflow history.
type CompletedDeployment struct {
	PlanID      string
	WorkflowID  string
	FinalPhase  Phase
	CompletedAt time.Time
}

// DatabaseOpsState is the query response from QueryDatabaseOpsState.
type DatabaseOpsState struct {
	DatabaseID       string
	Environment      Environment
	ActiveDeployment *ActiveDeployment
	CompletedOps     []CompletedDeployment
}

// DeploymentLockRequest is the Update payload for UpdateRequestDeployment.
type DeploymentLockRequest struct {
	PlanID     string
	WorkflowID string
}

// DeploymentLockResponse is the Update response. On success the caller may proceed
// to start the deployment workflow.
type DeploymentLockResponse struct {
	Granted bool
}

// DeploymentCompletePayload is the signal payload for SignalDeploymentComplete.
type DeploymentCompletePayload struct {
	PlanID     string
	WorkflowID string
	Phase      Phase
}

// DeploymentRequest is the input to BlueGreenDeploymentWorkflow.
// ParentWorkflowID is optional; when non-empty the workflow sends a
// SignalDeploymentComplete signal to the DatabaseOpsWorkflow on termination.
type DeploymentRequest struct {
	Plan             MigrationPlan
	ParentWorkflowID string
}

// DatabaseFingerprint derives a stable, credential-free identifier from a Postgres
// connection URL for use in the DatabaseOpsWorkflow ID.
// e.g. "postgres://user:pass@localhost:5435/appdb" → "localhost-5435-appdb"
func DatabaseFingerprint(databaseURL string) string {
	u, err := url.Parse(databaseURL)
	if err != nil || u.Host == "" {
		// Fallback: sanitise raw string.
		r := strings.NewReplacer(
			"://", "-", "/", "-", ":", "-", "@", "-", ".", "-",
		)
		return r.Replace(databaseURL)
	}
	host := u.Hostname()
	port := u.Port()
	dbname := strings.TrimPrefix(u.Path, "/")

	var parts []string
	// Replace dots in hostname with dashes for a clean ID.
	parts = append(parts, strings.ReplaceAll(host, ".", "-"))
	if port != "" {
		parts = append(parts, port)
	}
	if dbname != "" {
		parts = append(parts, dbname)
	}
	return strings.Join(parts, "-")
}

// SignalApprove is the Temporal signal name for human approval at each gate.
const SignalApprove = "approve"

// SignalRollback is the Temporal signal name for emergency rollback.
const SignalRollback = "rollback"

// QueryDeploymentState is the Temporal query type for reading deployment status.
const QueryDeploymentState = "deployment_state"

// MigrationPlan describes all SQL and metadata for a blue-green schema migration.
// ExpandSQL adds new structure while keeping old structure intact.
// ContractSQL removes old structure once all traffic is on the new app version.
// RollbackSQL undoes the expand in case of failure or emergency.
// VerifyQueries are SQL SELECT statements returning a count; expected count must be 0.
type MigrationPlan struct {
	// ID uniquely identifies this plan (used as workflow + deployment ID).
	ID string

	// Description is a human-readable summary shown at approval gates.
	Description string

	// ExpandSQL statements add new columns/indexes without removing old ones.
	ExpandSQL []string

	// ContractSQL statements remove old columns/indexes once new code is live.
	// These MUST NOT run until explicitly approved after traffic cutover.
	ContractSQL []string

	// RollbackSQL statements undo the expand phase.
	RollbackSQL []string

	// VerifyQueries are SELECT COUNT(*) checks run after expand.
	// Each query must return 0 to indicate no data inconsistency.
	VerifyQueries []VerifyQuery

	// ReadOnlyMaxDuration caps the time the DB stays read-only during cutover.
	// Defaults to DefaultReadOnlyMaxDuration if zero.
	ReadOnlyMaxDuration time.Duration
}

// VerifyQuery is a named SQL check run after the expand phase.
type VerifyQuery struct {
	Name      string
	SQL       string
	WantCount int64 // expected COUNT(*) result
}

// PhaseTransition records a phase change with timestamp and optional reason.
type PhaseTransition struct {
	Phase  Phase
	At     time.Time
	Reason string
}

// DeploymentStatus is the query response returned by QueryDeploymentState.
// It captures the full live state of the deployment directly from workflow memory.
type DeploymentStatus struct {
	ID      string
	Phase   Phase
	Plan    MigrationPlan
	History []PhaseTransition
}

// DeploymentResult is the final output of a completed or rolled-back workflow.
type DeploymentResult struct {
	DeploymentID string
	Phase        Phase
	History      []PhaseTransition
}

// ApprovalPayload carries an optional note from the human approver.
type ApprovalPayload struct {
	Note string
}

// RollbackPayload carries an optional reason for the rollback.
type RollbackPayload struct {
	Reason string
}

// ValidateMigrationPlan returns an error if the plan is not valid.
func ValidateMigrationPlan(p MigrationPlan) error {
	if p.ID == "" {
		return errors.New("plan ID is required")
	}
	if len(p.ExpandSQL) == 0 {
		return errors.New("expand SQL is required")
	}
	if len(p.ContractSQL) == 0 {
		return errors.New("contract SQL is required")
	}
	if len(p.RollbackSQL) == 0 {
		return errors.New("rollback SQL is required")
	}
	for i, vq := range p.VerifyQueries {
		if vq.SQL == "" {
			return fmt.Errorf("verify query %d: SQL is required", i)
		}
	}
	return nil
}

// EffectiveReadOnlyDuration returns the plan's read-only duration, falling back to the default.
func (p MigrationPlan) EffectiveReadOnlyDuration() time.Duration {
	if p.ReadOnlyMaxDuration <= 0 {
		return DefaultReadOnlyMaxDuration
	}
	return p.ReadOnlyMaxDuration
}
