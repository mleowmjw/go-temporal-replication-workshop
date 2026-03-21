package bluegreen

import (
	"errors"
	"fmt"
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
const DefaultReadOnlyMaxDuration = 5 * time.Minute

// SignalApprove is the Temporal signal name for human approval at each gate.
const SignalApprove = "approve"

// SignalRollback is the Temporal signal name for emergency rollback.
const SignalRollback = "rollback"

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
	Name        string
	SQL         string
	WantCount   int64 // expected COUNT(*) result
}

// PhaseTransition records a phase change with timestamp and optional reason.
type PhaseTransition struct {
	Phase     Phase
	At        time.Time
	Reason    string
}

// Deployment tracks the full lifecycle of one blue-green deployment.
type Deployment struct {
	ID      string
	Plan    MigrationPlan
	Phase   Phase
	History []PhaseTransition

	// AppCompatResult is populated after expand verification.
	AppCompatResult *AppCompatResult

	CreatedAt   time.Time
	CompletedAt *time.Time
}

// DeploymentResult is the final output of a successful deployment workflow.
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
