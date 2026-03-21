package bluegreen

import (
	"context"
	"fmt"
	"strings"

	"go.temporal.io/sdk/activity"
)

// BGDependencies holds all external dependencies injected into activities.
type BGDependencies struct {
	Store    DeploymentStore
	Migrator DatabaseMigrator
	App      AppSimulator
}

// BGActivities groups all blue-green deployment activities.
type BGActivities struct {
	deps BGDependencies
}

// NewBGActivities creates a new BGActivities from the provided dependencies.
func NewBGActivities(deps BGDependencies) *BGActivities {
	return &BGActivities{deps: deps}
}

// --- Activity input/output types ---

// AppCompatCheckResult carries the blue/green query compatibility outcome.
type AppCompatCheckResult struct {
	BluePass    bool
	GreenPass   bool
	BlueErrors  []string
	GreenErrors []string
	Summary     string
}

// VerifyExpandResult holds the results of all verify queries after expand.
type VerifyExpandResult struct {
	Passed  bool
	Checks  []CheckResult
	Failure string
}

// --- Activities ---

// ValidatePlanActivity checks that the plan has all required fields.
// It runs as the first step before any human approval gate.
func (a *BGActivities) ValidatePlanActivity(ctx context.Context, plan MigrationPlan) error {
	log := activity.GetLogger(ctx)
	log.Info("validating migration plan", "id", plan.ID)
	return ValidateMigrationPlan(plan)
}

// UpdatePhaseActivity persists a phase transition to the deployment store.
func (a *BGActivities) UpdatePhaseActivity(ctx context.Context, id string, phase Phase, reason string) error {
	log := activity.GetLogger(ctx)
	log.Info("updating deployment phase", "id", id, "phase", phase, "reason", reason)
	return a.deps.Store.UpdatePhase(ctx, id, phase, reason)
}

// ExecuteExpandActivity runs the expand SQL statements (adds new structure without removing old).
func (a *BGActivities) ExecuteExpandActivity(ctx context.Context, plan MigrationPlan) error {
	log := activity.GetLogger(ctx)
	log.Info("executing expand SQL", "id", plan.ID, "statements", len(plan.ExpandSQL))
	activity.RecordHeartbeat(ctx, "executing expand SQL")
	if err := a.deps.Migrator.ExecuteSQL(ctx, plan.ExpandSQL); err != nil {
		return fmt.Errorf("expand SQL failed: %w", err)
	}
	return nil
}

// VerifyExpandActivity runs the plan's verify queries to confirm data consistency
// after expand. All queries must return the expected count (typically 0).
func (a *BGActivities) VerifyExpandActivity(ctx context.Context, plan MigrationPlan) (VerifyExpandResult, error) {
	log := activity.GetLogger(ctx)
	log.Info("verifying expand results", "id", plan.ID, "checks", len(plan.VerifyQueries))

	result := VerifyExpandResult{Passed: true}
	for _, vq := range plan.VerifyQueries {
		cr, err := a.deps.Migrator.QueryCheck(ctx, vq.SQL)
		if err != nil {
			result.Passed = false
			result.Failure = fmt.Sprintf("query %q failed: %v", vq.Name, err)
			return result, fmt.Errorf("%s", result.Failure)
		}
		cr.Name = vq.Name
		result.Checks = append(result.Checks, cr)
		if cr.Count != vq.WantCount {
			result.Passed = false
			result.Failure = fmt.Sprintf("query %q: got count %d, want %d", vq.Name, cr.Count, vq.WantCount)
			return result, fmt.Errorf("%s", result.Failure)
		}
	}
	return result, nil
}

// RunAppCompatCheckActivity runs both blue and green query sets against the
// current database schema and returns a compatibility report.
// If bluePassRequired is true and blue queries fail, an error is returned so
// the workflow can compensate immediately.
func (a *BGActivities) RunAppCompatCheckActivity(ctx context.Context, bluePassRequired bool) (AppCompatCheckResult, error) {
	log := activity.GetLogger(ctx)
	log.Info("running app compatibility check")

	compat := RunAppCompat(ctx, a.deps.App, a.deps.Migrator)
	result := AppCompatCheckResult{
		BluePass:    compat.BluePass,
		GreenPass:   compat.GreenPass,
		BlueErrors:  compat.BlueErrors,
		GreenErrors: compat.GreenErrors,
	}

	var parts []string
	if compat.BluePass {
		parts = append(parts, "blue=PASS")
	} else {
		parts = append(parts, fmt.Sprintf("blue=FAIL(%s)", strings.Join(compat.BlueErrors, "; ")))
	}
	if compat.GreenPass {
		parts = append(parts, "green=PASS")
	} else {
		parts = append(parts, fmt.Sprintf("green=FAIL(%s)", strings.Join(compat.GreenErrors, "; ")))
	}
	result.Summary = strings.Join(parts, ", ")
	log.Info("app compat result", "summary", result.Summary)

	if bluePassRequired && !compat.BluePass {
		return result, fmt.Errorf("expand broke existing app: %s", strings.Join(compat.BlueErrors, "; "))
	}
	return result, nil
}

// AcquireReadOnlyActivity sets the database to read-only mode.
// Used at the start of the cutover window to drain in-flight writes.
func (a *BGActivities) AcquireReadOnlyActivity(ctx context.Context, plan MigrationPlan) error {
	log := activity.GetLogger(ctx)
	log.Info("acquiring read-only lock", "id", plan.ID, "maxDuration", plan.EffectiveReadOnlyDuration())
	if err := a.deps.Migrator.SetReadOnly(ctx, true); err != nil {
		return fmt.Errorf("set read-only: %w", err)
	}
	return nil
}

// SwitchTrafficActivity records the traffic switch in the deployment store.
// In production this would update a load balancer or feature flag.
func (a *BGActivities) SwitchTrafficActivity(ctx context.Context, deploymentID string) error {
	log := activity.GetLogger(ctx)
	log.Info("switching traffic to green environment", "id", deploymentID)
	// Record in store as part of monitoring phase.
	return a.deps.Store.UpdatePhase(ctx, deploymentID, PhaseMonitoring, "traffic switched to green")
}

// ReleaseReadOnlyActivity removes the read-only restriction.
func (a *BGActivities) ReleaseReadOnlyActivity(ctx context.Context, plan MigrationPlan) error {
	log := activity.GetLogger(ctx)
	log.Info("releasing read-only lock", "id", plan.ID)
	if err := a.deps.Migrator.SetReadOnly(ctx, false); err != nil {
		return fmt.Errorf("release read-only: %w", err)
	}
	return nil
}

// ExecuteContractActivity runs the contract SQL statements.
// These DROP the old columns/indexes that the old app version used.
// Must only run after explicit human approval confirming the new app is healthy.
func (a *BGActivities) ExecuteContractActivity(ctx context.Context, plan MigrationPlan) error {
	log := activity.GetLogger(ctx)
	log.Info("executing contract SQL", "id", plan.ID, "statements", len(plan.ContractSQL))
	activity.RecordHeartbeat(ctx, "executing contract SQL")
	if err := a.deps.Migrator.ExecuteSQL(ctx, plan.ContractSQL); err != nil {
		return fmt.Errorf("contract SQL failed: %w", err)
	}
	return nil
}

// VerifyContractActivity checks that the green app still works after the contract phase.
func (a *BGActivities) VerifyContractActivity(ctx context.Context) (AppCompatCheckResult, error) {
	log := activity.GetLogger(ctx)
	log.Info("verifying contract: green app compatibility check")
	// After contract, green must pass; blue failure is expected/acceptable.
	return a.RunAppCompatCheckActivity(ctx, false)
}

// ExecuteRollbackActivity runs the rollback SQL to undo the expand phase.
// Called during emergency rollback or when VerifyExpand fails.
func (a *BGActivities) ExecuteRollbackActivity(ctx context.Context, plan MigrationPlan) error {
	log := activity.GetLogger(ctx)
	log.Info("executing rollback SQL", "id", plan.ID, "statements", len(plan.RollbackSQL))
	activity.RecordHeartbeat(ctx, "executing rollback SQL")
	if err := a.deps.Migrator.ExecuteSQL(ctx, plan.RollbackSQL); err != nil {
		return fmt.Errorf("rollback SQL failed: %w", err)
	}
	return nil
}
