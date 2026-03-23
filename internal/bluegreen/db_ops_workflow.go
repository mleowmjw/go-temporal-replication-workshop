package bluegreen

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ErrSchemaLocked is returned by the UpdateRequestDeployment Update handler when
// another deployment is currently holding the schema lock.
var ErrSchemaLocked = temporal.NewApplicationError("schema locked", "SchemaLocked")

// databaseOpsActivityOptions are used for the external-signal activities called
// from within the coordinator (e.g. lock-timeout rollback notification).
var databaseOpsActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 15 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 3,
	},
}

// DatabaseOpsWorkflow is a per-database perpetual coordinator workflow that:
//
//   - Enforces a schema lock: at most one blue-green deployment runs at a time
//     against this database. Concurrent requests are rejected via the Update handler.
//   - Tracks all deployment operations (active + completed history).
//   - Automatically signals the active deployment to rollback if the environment
//     lock timeout expires before the deployment finishes.
//   - Continues-as-new every 24 hours to keep history size bounded, waiting for
//     any in-flight deployment to complete before recycling.
//
// Workflow ID: "db-ops-{DatabaseFingerprint(url)}" — Temporal's unique-ID guarantee
// means exactly one coordinator exists per database at all times.
func DatabaseOpsWorkflow(ctx workflow.Context, config DatabaseOpsConfig) error {
	log := workflow.GetLogger(ctx)
	log.Info("database ops coordinator started",
		"databaseID", config.DatabaseID,
		"environment", config.Environment,
		"lockTimeout", config.Environment.LockTimeout(),
	)

	// ── Workflow-local state ─────────────────────────────────────────────────
	var activeDeployment *ActiveDeployment
	var completedOps []CompletedDeployment

	// cancelActiveLock cancels the in-flight lock timer goroutine when a
	// deployment completes normally. Defaults to no-op so it is always safe to call.
	cancelActiveLock := func() {}

	// ── Query handler ────────────────────────────────────────────────────────
	if err := workflow.SetQueryHandler(ctx, QueryDatabaseOpsState, func() (DatabaseOpsState, error) {
		return DatabaseOpsState{
			DatabaseID:       config.DatabaseID,
			Environment:      config.Environment,
			ActiveDeployment: activeDeployment,
			CompletedOps:     completedOps,
		}, nil
	}); err != nil {
		return fmt.Errorf("register query handler: %w", err)
	}

	// ── Update handler: request_deployment ───────────────────────────────────
	// The validator runs before the handler; a non-nil error rejects the Update
	// without modifying workflow state. The handler runs atomically after acceptance.
	if err := workflow.SetUpdateHandlerWithOptions(
		ctx,
		UpdateRequestDeployment,
		func(updCtx workflow.Context, req DeploymentLockRequest) (DeploymentLockResponse, error) {
			now := workflow.Now(updCtx)
			activeDeployment = &ActiveDeployment{
				PlanID:     req.PlanID,
				WorkflowID: req.WorkflowID,
				StartedAt:  now,
			}

			// Start a cancellable lock-timeout goroutine.
			// If the deployment does not complete within the environment's
			// lock timeout, we automatically signal it to rollback.
			lockCtx, cancel := workflow.WithCancel(ctx)
			cancelActiveLock = cancel

			workflow.Go(lockCtx, func(gCtx workflow.Context) {
				timeout := config.Environment.LockTimeout()
				_ = workflow.NewTimer(gCtx, timeout).Get(gCtx, nil)
				if gCtx.Err() != nil {
					// Timer was cancelled — deployment completed normally.
					return
				}
				// Timer fired: send rollback signal to the deployment workflow.
				log.Warn("schema lock timeout expired — signalling deployment to rollback",
					"databaseID", config.DatabaseID,
					"planID", req.PlanID,
					"deploymentWorkflowID", req.WorkflowID,
					"timeout", timeout,
				)
			// Use gCtx (not ctx) so this blocking call is scoped to this
			// goroutine's context and doesn't conflict with the main coroutine
			// already blocking on sel.Select(ctx).
			_ = workflow.SignalExternalWorkflow(
				gCtx,
				req.WorkflowID, "",
				SignalRollback,
				RollbackPayload{Reason: fmt.Sprintf("schema lock timeout (%s) exceeded", timeout)},
			).Get(gCtx, nil)
			})

			log.Info("schema lock granted",
				"databaseID", config.DatabaseID,
				"planID", req.PlanID,
				"deploymentWorkflowID", req.WorkflowID,
			)
			return DeploymentLockResponse{Granted: true}, nil
		},
		workflow.UpdateHandlerOptions{
		Validator: func(_ workflow.Context, req DeploymentLockRequest) error {
			if activeDeployment != nil {
				// Return a clean ApplicationError so the client can detect the
				// "SchemaLocked" type reliably without chasing wrapped errors.
				return temporal.NewApplicationError(
					fmt.Sprintf("deployment %s is active", activeDeployment.PlanID),
					"SchemaLocked",
				)
			}
			return nil
		},
		},
	); err != nil {
		return fmt.Errorf("register update handler: %w", err)
	}

	// ── Channels ──────────────────────────────────────────────────────────────
	completeCh := workflow.GetSignalChannel(ctx, SignalDeploymentComplete)

	// 24h continue-as-new timer to keep history bounded.
	continueTimer := workflow.NewTimer(ctx, ContinueAsNewInterval)
	continueAsNewRequested := false

	// ── Main event loop ───────────────────────────────────────────────────────
	for {
		sel := workflow.NewSelector(ctx)

		sel.AddReceive(completeCh, func(c workflow.ReceiveChannel, _ bool) {
			var payload DeploymentCompletePayload
			c.Receive(ctx, &payload)

			log.Info("deployment complete signal received",
				"databaseID", config.DatabaseID,
				"planID", payload.PlanID,
				"phase", payload.Phase,
			)

			// Cancel the lock timer (no-op if timer already fired).
			cancelActiveLock()
			cancelActiveLock = func() {}

			completedOps = append(completedOps, CompletedDeployment{
				PlanID:      payload.PlanID,
				WorkflowID:  payload.WorkflowID,
				FinalPhase:  payload.Phase,
				CompletedAt: workflow.Now(ctx),
			})
			activeDeployment = nil
		})

		// Only add the continue-as-new timer to the selector if it hasn't fired
		// yet. If it has already fired, re-adding the consumed future would cause
		// an infinite tight loop (the future resolves immediately every iteration).
		if !continueAsNewRequested {
			sel.AddFuture(continueTimer, func(_ workflow.Future) {
				log.Info("24h continue-as-new timer fired", "databaseID", config.DatabaseID)
				continueAsNewRequested = true
			})
		}

		sel.Select(ctx)

		// Continue-as-new only when no deployment is in progress to avoid
		// dropping the active-deployment state across the history boundary.
		if continueAsNewRequested && activeDeployment == nil {
			log.Info("continuing-as-new", "databaseID", config.DatabaseID)
			return workflow.NewContinueAsNewError(ctx, DatabaseOpsWorkflow, config)
		}
	}
}
