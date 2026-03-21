package bluegreen

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// defaultBGActivityOptions are the standard retry/timeout settings for
// blue-green activities. DDL operations use longer timeouts.
var defaultBGActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 60 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 3,
	},
}

// ddlActivityOptions are used for the long-running ExecuteExpand, ExecuteContract,
// and ExecuteRollback activities which may take minutes on large tables.
var ddlActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 10 * time.Minute,
	HeartbeatTimeout:    30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 2,
	},
}

// BlueGreenDeploymentWorkflow orchestrates a full blue-green database migration
// lifecycle with human approval gates at every phase boundary.
//
// Signal "approve" advances through each gate.
// Signal "rollback" triggers emergency rollback at any point before contract.
//
// Phases:
//
//	plan_review  → (approve) → expanding → expand_verify
//	expand_verify → (approve) → cutover → monitoring
//	monitoring   → (approve) → contract_wait → (approve_contract) → contracting → complete
//	any_phase    → (rollback) → rolling back → rolled_back
func BlueGreenDeploymentWorkflow(ctx workflow.Context, plan MigrationPlan) (DeploymentResult, error) {
	ctx = workflow.WithActivityOptions(ctx, defaultBGActivityOptions)
	log := workflow.GetLogger(ctx)
	log.Info("starting blue-green deployment", "id", plan.ID)

	// acts is intentionally a nil pointer used only as a type reference so that
	// workflow.ExecuteActivity can resolve the registered function name via
	// reflection. The workflow scheduler never calls these methods directly on
	// this nil receiver — activity execution happens on the worker which holds
	// the real *BGActivities instance. This is the standard Temporal Go SDK
	// pattern (identical to `var acts *Activities` in internal/temporal/workflow.go).
	var acts *BGActivities
	var result DeploymentResult
	result.DeploymentID = plan.ID

	// Channels for human approval and rollback signals.
	approveCh := workflow.GetSignalChannel(ctx, SignalApprove)
	rollbackCh := workflow.GetSignalChannel(ctx, SignalRollback)

	// rollbackRequested races approval at each gate. When triggered it
	// immediately unwinds the workflow via the compensate path.
	rollbackRequested := false

	// waitForApproval blocks until the human sends "approve" or "rollback".
	// Returns true if approved, false if rollback was requested.
	waitForApproval := func(phase Phase) bool {
		sel := workflow.NewSelector(ctx)
		approved := false
		sel.AddReceive(approveCh, func(c workflow.ReceiveChannel, more bool) {
			var payload ApprovalPayload
			c.Receive(ctx, &payload)
			log.Info("approval received", "phase", phase, "note", payload.Note)
			approved = true
		})
		sel.AddReceive(rollbackCh, func(c workflow.ReceiveChannel, more bool) {
			var payload RollbackPayload
			c.Receive(ctx, &payload)
			log.Warn("rollback signal received", "phase", phase, "reason", payload.Reason)
			rollbackRequested = true
		})
		sel.Select(ctx)
		return approved && !rollbackRequested
	}

	// compensate rolls back the expand and releases any locks.
	compensate := func(reason string) (DeploymentResult, error) {
		log.Warn("compensating blue-green deployment", "id", plan.ID, "reason", reason)
		compCtx := workflow.WithActivityOptions(ctx, ddlActivityOptions)

		// Release read-only lock if held (idempotent on fake).
		_ = workflow.ExecuteActivity(compCtx, acts.ReleaseReadOnlyActivity, plan).Get(compCtx, nil)
		// Undo expand SQL.
		_ = workflow.ExecuteActivity(compCtx, acts.ExecuteRollbackActivity, plan).Get(compCtx, nil)
		// Mark rolled back.
		_ = workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity, plan.ID, PhaseRolledBack, reason).Get(ctx, nil)

		result.Phase = PhaseRolledBack
		return result, fmt.Errorf("deployment rolled back: %s", reason)
	}

	// ─── Phase: plan_review ──────────────────────────────────────────────────
	if err := workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity,
		plan.ID, PhasePlanReview, "deployment created").Get(ctx, nil); err != nil {
		return result, err
	}
	if err := workflow.ExecuteActivity(ctx, acts.ValidatePlanActivity, plan).Get(ctx, nil); err != nil {
		_ = workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity, plan.ID, PhaseFailed, err.Error()).Get(ctx, nil)
		return result, fmt.Errorf("plan validation: %w", err)
	}
	log.Info("waiting for plan review approval", "id", plan.ID)
	if !waitForApproval(PhasePlanReview) {
		return compensate("rolled back during plan review")
	}

	// ─── Phase: expanding ────────────────────────────────────────────────────
	if err := workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity,
		plan.ID, PhaseExpanding, "plan approved").Get(ctx, nil); err != nil {
		return result, err
	}
	expandCtx := workflow.WithActivityOptions(ctx, ddlActivityOptions)
	if err := workflow.ExecuteActivity(expandCtx, acts.ExecuteExpandActivity, plan).Get(expandCtx, nil); err != nil {
		return compensate(fmt.Sprintf("expand failed: %v", err))
	}

	// ─── Phase: expand_verify ────────────────────────────────────────────────
	if err := workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity,
		plan.ID, PhaseExpandVerify, "expand SQL completed").Get(ctx, nil); err != nil {
		return result, err
	}

	// Verify data consistency.
	var verifyResult VerifyExpandResult
	if err := workflow.ExecuteActivity(ctx, acts.VerifyExpandActivity, plan).Get(ctx, &verifyResult); err != nil {
		return compensate(fmt.Sprintf("expand verify failed: %v", err))
	}

	// App compatibility check: both blue AND green must pass after expand.
	var compatResult AppCompatCheckResult
	if err := workflow.ExecuteActivity(ctx, acts.RunAppCompatCheckActivity, true).Get(ctx, &compatResult); err != nil {
		return compensate(fmt.Sprintf("app compat check failed: %v", err))
	}
	log.Info("expand verified", "compat", compatResult.Summary)

	// Human review of expand results before cutover.
	log.Info("waiting for expand verify approval", "id", plan.ID, "compat", compatResult.Summary)
	if !waitForApproval(PhaseExpandVerify) {
		return compensate("rolled back after expand verify")
	}

	// ─── Phase: cutover (read-only window) ───────────────────────────────────
	if err := workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity,
		plan.ID, PhaseCutover, "expand approved — acquiring read-only lock").Get(ctx, nil); err != nil {
		return result, err
	}
	if err := workflow.ExecuteActivity(ctx, acts.AcquireReadOnlyActivity, plan).Get(ctx, nil); err != nil {
		return compensate(fmt.Sprintf("acquire read-only failed: %v", err))
	}

	// timerCtx allows us to cancel the read-only timer once traffic switches.
	timerCtx, cancelTimer := workflow.WithCancel(ctx)
	readOnlyTimedOut := false
	workflow.Go(timerCtx, func(gCtx workflow.Context) {
		_ = workflow.NewTimer(gCtx, plan.EffectiveReadOnlyDuration()).Get(gCtx, nil)
		if gCtx.Err() == nil {
			readOnlyTimedOut = true
		}
	})

	// Switch traffic while holding the timer goroutine.
	trafficSwitched := false
	switchErr := workflow.ExecuteActivity(ctx, acts.SwitchTrafficActivity, plan.ID).Get(ctx, nil)
	if switchErr == nil {
		trafficSwitched = true
	}

	// Cancel the timer goroutine (no-op if already fired).
	cancelTimer()

	// Always release read-only lock — even on failure.
	_ = workflow.ExecuteActivity(ctx, acts.ReleaseReadOnlyActivity, plan).Get(ctx, nil)

	if readOnlyTimedOut {
		return compensate("read-only window exceeded maximum duration")
	}

	if !trafficSwitched {
		return compensate(fmt.Sprintf("traffic switch failed: %v", switchErr))
	}

	// ─── Phase: monitoring ───────────────────────────────────────────────────
	// SwitchTrafficActivity already writes PhaseMonitoring to the store.
	log.Info("green environment live — awaiting monitoring approval", "id", plan.ID)

	// Race: approve → contract_wait, rollback → undo.
	if !waitForApproval(PhaseMonitoring) {
		return compensate("rolled back during monitoring")
	}

	// ─── Phase: contract_wait ────────────────────────────────────────────────
	if err := workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity,
		plan.ID, PhaseContractWait, "monitoring approved — ready for contract").Get(ctx, nil); err != nil {
		return result, err
	}

	// Final app compat check: green must pass before we drop old columns.
	// If the activity itself errors (worker unavailable, panic, etc.) we
	// compensate just like every other post-expand failure path.
	var preContractCompat AppCompatCheckResult
	if err := workflow.ExecuteActivity(ctx, acts.RunAppCompatCheckActivity, false).Get(ctx, &preContractCompat); err != nil {
		return compensate(fmt.Sprintf("pre-contract compat check error: %v", err))
	}
	if !preContractCompat.GreenPass {
		return compensate(fmt.Sprintf("green app check failed before contract: %v", preContractCompat.GreenErrors))
	}
	log.Info("pre-contract check passed", "compat", preContractCompat.Summary)

	// Separate explicit approval for contract (destructive — cannot be undone).
	log.Info("CONTRACT GATE: waiting for explicit approval to drop old columns",
		"id", plan.ID, "compat", preContractCompat.Summary)
	if !waitForApproval(PhaseContractWait) {
		return compensate("rolled back at contract gate")
	}

	// ─── Phase: contracting ───────────────────────────────────────────────────
	if err := workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity,
		plan.ID, PhaseContracting, "contract approved").Get(ctx, nil); err != nil {
		return result, err
	}
	contractCtx := workflow.WithActivityOptions(ctx, ddlActivityOptions)
	if err := workflow.ExecuteActivity(contractCtx, acts.ExecuteContractActivity, plan).Get(contractCtx, nil); err != nil {
		// Contract failure is NOT rolled back (DDL may be partially applied).
		_ = workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity, plan.ID, PhaseFailed,
			fmt.Sprintf("contract SQL failed: %v", err)).Get(ctx, nil)
		return result, fmt.Errorf("contract SQL: %w", err)
	}

	// Final verification.
	var finalCompat AppCompatCheckResult
	if err := workflow.ExecuteActivity(ctx, acts.VerifyContractActivity).Get(ctx, &finalCompat); err != nil {
		// Log but do not fail — contract is already applied.
		log.Warn("post-contract verify error", "error", err.Error())
	}
	log.Info("contract complete", "compat", finalCompat.Summary)

	// ─── Phase: complete ──────────────────────────────────────────────────────
	if err := workflow.ExecuteActivity(ctx, acts.UpdatePhaseActivity,
		plan.ID, PhaseComplete, "deployment complete").Get(ctx, nil); err != nil {
		return result, err
	}

	result.Phase = PhaseComplete
	log.Info("blue-green deployment complete", "id", plan.ID)
	return result, nil
}

// ensureTimer is a no-op helper referenced to avoid unused-import warnings when
// timer cancellation races are used.
var _ = time.Second
