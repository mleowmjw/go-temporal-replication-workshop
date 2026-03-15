package temporal

import (
	"fmt"
	"time"

	"app/internal/connectors"
	"app/internal/domain"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// defaultActivityOptions provides sensible retry defaults for all activities.
var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 3,
	},
}

// ProvisionPipelineWorkflow orchestrates the full provisioning sequence for a pipeline.
// On any unrecoverable failure after resources have been created, it compensates by
// tearing down the created resources in reverse order.
func ProvisionPipelineWorkflow(ctx workflow.Context, spec domain.PipelineSpec) (domain.ProvisionResult, error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)

	log := workflow.GetLogger(ctx)
	log.Info("provisioning pipeline", "tenant", spec.TenantID, "pipeline", spec.PipelineID)

	var acts *Activities // used only as a type reference for activity registration

	var result domain.ProvisionResult

	// Step 1: Validate spec.
	if err := workflow.ExecuteActivity(ctx, acts.ValidatePipelineSpecActivity, spec).Get(ctx, nil); err != nil {
		return result, fmt.Errorf("validate spec: %w", err)
	}

	// Step 2: Validate schema policy (pre-flight compat check).
	policyIn := ValidateSchemaPolicyActivityInput{Spec: spec, Schema: spec.Schema.Format}
	if err := workflow.ExecuteActivity(ctx, acts.ValidateSchemaPolicyActivity, policyIn).Get(ctx, nil); err != nil {
		return result, fmt.Errorf("schema policy: %w", err)
	}

	// Step 3: Register schema subject.
	if err := workflow.ExecuteActivity(ctx, acts.EnsureSchemaSubjectActivity, spec).Get(ctx, &result.SchemaID); err != nil {
		return result, fmt.Errorf("ensure schema: %w", err)
	}

	// Step 4: Prepare source (logical replication prerequisites).
	if err := workflow.ExecuteActivity(ctx, acts.PrepareSourceActivity, spec).Get(ctx, nil); err != nil {
		return result, compensate(ctx, result, spec, fmt.Errorf("prepare source: %w", err))
	}

	// Step 5: Ensure stream.
	if err := workflow.ExecuteActivity(ctx, acts.EnsureStreamActivity, spec).Get(ctx, &result.StreamID); err != nil {
		return result, compensate(ctx, result, spec, fmt.Errorf("ensure stream: %w", err))
	}

	// Step 6: Ensure sink.
	if err := workflow.ExecuteActivity(ctx, acts.EnsureSinkActivity, spec).Get(ctx, &result.SinkID); err != nil {
		return result, compensate(ctx, result, spec, fmt.Errorf("ensure sink: %w", err))
	}

	// Step 7: Start capture.
	var captureHandle connectors.CaptureHandle
	captureIn := StartCaptureActivityInput{Spec: spec}
	if err := workflow.ExecuteActivity(ctx, acts.StartCaptureActivity, captureIn).Get(ctx, &captureHandle); err != nil {
		return result, compensate(ctx, result, spec, fmt.Errorf("start capture: %w", err))
	}
	result.CaptureID = captureHandle.ID

	// Step 8: Mark pipeline ACTIVE.
	markIn := MarkPipelineActiveActivityInput{
		Spec:      spec,
		SchemaID:  result.SchemaID,
		StreamID:  result.StreamID,
		SinkID:    result.SinkID,
		CaptureID: result.CaptureID,
	}
	if err := workflow.ExecuteActivity(ctx, acts.MarkPipelineActiveActivity, markIn).Get(ctx, nil); err != nil {
		return result, compensate(ctx, result, spec, fmt.Errorf("mark active: %w", err))
	}

	log.Info("pipeline provisioned successfully", "tenant", spec.TenantID, "pipeline", spec.PipelineID)
	return result, nil
}

// compensate runs cleanup activities in reverse creation order and marks the pipeline ERROR.
// It returns the original provisioning error wrapped with compensation context.
func compensate(ctx workflow.Context, res domain.ProvisionResult, spec domain.PipelineSpec, provisionErr error) error {
	log := workflow.GetLogger(ctx)
	log.Error("provisioning failed; running compensation", "error", provisionErr.Error())

	// Use a detached context so compensation runs even if the parent ctx is cancelled.
	compensationCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var acts *Activities

	if res.CaptureID != "" {
		h := connectors.CaptureHandle{ID: res.CaptureID}
		_ = workflow.ExecuteActivity(compensationCtx, acts.StopCaptureActivity, h).Get(compensationCtx, nil)
	}
	if res.SinkID != "" {
		_ = workflow.ExecuteActivity(compensationCtx, acts.DeleteSinkActivity, res.SinkID).Get(compensationCtx, nil)
	}
	if res.StreamID != "" {
		_ = workflow.ExecuteActivity(compensationCtx, acts.DeleteStreamActivity, res.StreamID).Get(compensationCtx, nil)
	}

	// Best-effort: mark pipeline ERROR.
	_ = workflow.ExecuteActivity(compensationCtx, acts.MarkPipelineErrorActivity, spec).Get(compensationCtx, nil)

	return provisionErr
}
