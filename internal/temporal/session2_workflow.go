package temporal

import (
	"fmt"
	"time"

	"app/internal/connectors"
	"app/internal/domain"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// CDCProvisionResult extends domain.ProvisionResult with connector metadata.
type CDCProvisionResult struct {
	domain.ProvisionResult
	ConnectorName string
}

// ProvisionCDCPipelineWorkflow orchestrates a full Debezium CDC pipeline provisioning:
// validate → schema → source → stream → connector → wait-running → sink → active.
// On failure after resource creation it compensates in reverse order.
func ProvisionCDCPipelineWorkflow(ctx workflow.Context, spec domain.PipelineSpec, connCfg domain.ConnectorConfig) (CDCProvisionResult, error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)

	log := workflow.GetLogger(ctx)
	log.Info("provisioning CDC pipeline", "tenant", spec.TenantID, "pipeline", spec.PipelineID, "connector", connCfg.Name)

	var acts *Activities
	var s2acts *Session2Activities

	var result CDCProvisionResult

	// Step 1: Validate spec.
	if err := workflow.ExecuteActivity(ctx, acts.ValidatePipelineSpecActivity, spec).Get(ctx, nil); err != nil {
		return result, fmt.Errorf("validate spec: %w", err)
	}

	// Step 2: Validate schema policy.
	policyIn := ValidateSchemaPolicyActivityInput{Spec: spec, Schema: spec.Schema.Format}
	if err := workflow.ExecuteActivity(ctx, acts.ValidateSchemaPolicyActivity, policyIn).Get(ctx, nil); err != nil {
		return result, fmt.Errorf("schema policy: %w", err)
	}

	// Step 3: Register schema subject.
	if err := workflow.ExecuteActivity(ctx, acts.EnsureSchemaSubjectActivity, spec).Get(ctx, &result.SchemaID); err != nil {
		return result, fmt.Errorf("ensure schema: %w", err)
	}

	// Step 4: Prepare source.
	if err := workflow.ExecuteActivity(ctx, acts.PrepareSourceActivity, spec).Get(ctx, nil); err != nil {
		return result, compensateCDC(ctx, result, spec, fmt.Errorf("prepare source: %w", err))
	}

	// Step 5: Ensure stream.
	if err := workflow.ExecuteActivity(ctx, acts.EnsureStreamActivity, spec).Get(ctx, &result.StreamID); err != nil {
		return result, compensateCDC(ctx, result, spec, fmt.Errorf("ensure stream: %w", err))
	}

	// Step 6: Create Debezium connector via Kafka Connect.
	createConnIn := CreateConnectorActivityInput{Spec: spec, Config: connCfg}
	if err := workflow.ExecuteActivity(ctx, s2acts.CreateConnectorActivity, createConnIn).Get(ctx, nil); err != nil {
		return result, compensateCDC(ctx, result, spec, fmt.Errorf("create connector: %w", err))
	}
	result.ConnectorName = connCfg.Name

	// Step 7: Wait for connector to reach RUNNING.
	waitIn := WaitForConnectorRunningActivityInput{
		Spec:          spec,
		ConnectorName: connCfg.Name,
		PollInterval:  3 * time.Second,
		Timeout:       5 * time.Minute,
	}
	waitOpts := workflow.ActivityOptions{
		// Long-running poll; heartbeat keeps it alive.
		StartToCloseTimeout: 6 * time.Minute,
		HeartbeatTimeout:    15 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, waitOpts), s2acts.WaitForConnectorRunningActivity, waitIn).Get(ctx, nil); err != nil {
		return result, compensateCDC(ctx, result, spec, fmt.Errorf("wait connector running: %w", err))
	}

	// Step 8: Ensure sink.
	if err := workflow.ExecuteActivity(ctx, acts.EnsureSinkActivity, spec).Get(ctx, &result.SinkID); err != nil {
		return result, compensateCDC(ctx, result, spec, fmt.Errorf("ensure sink: %w", err))
	}

	// Step 9: Mark pipeline ACTIVE.
	markIn := MarkPipelineActiveActivityInput{
		Spec:      spec,
		SchemaID:  result.SchemaID,
		StreamID:  result.StreamID,
		SinkID:    result.SinkID,
		CaptureID: result.ConnectorName,
	}
	if err := workflow.ExecuteActivity(ctx, acts.MarkPipelineActiveActivity, markIn).Get(ctx, nil); err != nil {
		return result, compensateCDC(ctx, result, spec, fmt.Errorf("mark active: %w", err))
	}

	log.Info("CDC pipeline provisioned", "tenant", spec.TenantID, "pipeline", spec.PipelineID)
	return result, nil
}

// PauseCDCPipelineWorkflow stops the Debezium connector and marks the pipeline PAUSED.
func PauseCDCPipelineWorkflow(ctx workflow.Context, spec domain.PipelineSpec, connectorName string) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)

	var s2acts *Session2Activities
	var acts *Activities

	in := PauseConnectorActivityInput{Spec: spec, ConnectorName: connectorName}
	if err := workflow.ExecuteActivity(ctx, s2acts.PauseConnectorActivity, in).Get(ctx, nil); err != nil {
		return fmt.Errorf("pause connector: %w", err)
	}
	if err := workflow.ExecuteActivity(ctx, acts.MarkPipelinePausedActivity, spec).Get(ctx, nil); err != nil {
		return fmt.Errorf("mark pipeline paused: %w", err)
	}
	return nil
}

// ResumeCDCPipelineWorkflow resumes the Debezium connector and marks the pipeline ACTIVE.
func ResumeCDCPipelineWorkflow(ctx workflow.Context, spec domain.PipelineSpec, connectorName string) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)

	var s2acts *Session2Activities
	var acts *Activities

	in := ResumeConnectorActivityInput{Spec: spec, ConnectorName: connectorName}
	if err := workflow.ExecuteActivity(ctx, s2acts.ResumeConnectorActivity, in).Get(ctx, nil); err != nil {
		return fmt.Errorf("resume connector: %w", err)
	}
	markIn := MarkPipelineActiveActivityInput{Spec: spec}
	if err := workflow.ExecuteActivity(ctx, acts.MarkPipelineActiveActivity, markIn).Get(ctx, nil); err != nil {
		return fmt.Errorf("mark pipeline active: %w", err)
	}
	return nil
}

// DecommissionCDCPipelineWorkflow tears down all CDC pipeline resources.
func DecommissionCDCPipelineWorkflow(ctx workflow.Context, spec domain.PipelineSpec, connectorName string, streamID string, sinkID string) error {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions)

	var s2acts *Session2Activities
	var acts *Activities

	deleteConnIn := DeleteConnectorActivityInput{Spec: spec, ConnectorName: connectorName}
	if err := workflow.ExecuteActivity(ctx, s2acts.DeleteConnectorActivity, deleteConnIn).Get(ctx, nil); err != nil {
		return fmt.Errorf("delete connector: %w", err)
	}
	if sinkID != "" {
		if err := workflow.ExecuteActivity(ctx, acts.DeleteSinkActivity, sinkID).Get(ctx, nil); err != nil {
			return fmt.Errorf("delete sink: %w", err)
		}
	}
	if streamID != "" {
		if err := workflow.ExecuteActivity(ctx, acts.DeleteStreamActivity, streamID).Get(ctx, nil); err != nil {
			return fmt.Errorf("delete stream: %w", err)
		}
	}
	if err := workflow.ExecuteActivity(ctx, acts.MarkPipelineErrorActivity, spec).Get(ctx, nil); err != nil {
		return fmt.Errorf("mark decommissioned: %w", err)
	}
	return nil
}

// compensateCDC runs cleanup for CDC resources in reverse creation order.
func compensateCDC(ctx workflow.Context, res CDCProvisionResult, spec domain.PipelineSpec, provisionErr error) error {
	log := workflow.GetLogger(ctx)
	log.Error("CDC provisioning failed; running compensation", "error", provisionErr.Error())

	compOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	}
	compCtx := workflow.WithActivityOptions(ctx, compOpts)

	var s2acts *Session2Activities
	var acts *Activities

	if res.ConnectorName != "" {
		deleteIn := DeleteConnectorActivityInput{Spec: spec, ConnectorName: res.ConnectorName}
		_ = workflow.ExecuteActivity(compCtx, s2acts.DeleteConnectorActivity, deleteIn).Get(compCtx, nil)
	}
	if res.SinkID != "" {
		_ = workflow.ExecuteActivity(compCtx, acts.DeleteSinkActivity, res.SinkID).Get(compCtx, nil)
	}
	if res.StreamID != "" {
		_ = workflow.ExecuteActivity(compCtx, acts.DeleteStreamActivity, res.StreamID).Get(compCtx, nil)
	}
	_ = workflow.ExecuteActivity(compCtx, acts.MarkPipelineErrorActivity, spec).Get(compCtx, nil)

	return provisionErr
}

// BuildConnectorConfig builds a default Debezium Postgres connector config from a PipelineSpec.
// This is a convenience helper for tests and wiring; production callers may supply their own.
func BuildConnectorConfig(spec domain.PipelineSpec, connectSecret map[string]string) domain.ConnectorConfig {
	name := fmt.Sprintf("dbz-%s-%s", spec.TenantID, spec.PipelineID)
	cfg := map[string]string{
		"connector.class":               "io.debezium.connector.postgresql.PostgresConnector",
		"topic.prefix":                  fmt.Sprintf("workshop.%s", spec.TenantID),
		"plugin.name":                   "pgoutput",
		"snapshot.mode":                 "initial",
		"publication.autocreate.mode":   "disabled",
		"include.schema.changes":        "false",
	}
	// Merge source connection details from the resolved secret.
	for k, v := range connectSecret {
		cfg[k] = v
	}
	// Source spec overrides.
	if spec.Source.Database != "" {
		cfg["database.dbname"] = spec.Source.Database
	}
	if len(spec.Source.Tables) > 0 {
		tbl := ""
		for i, t := range spec.Source.Tables {
			if i > 0 {
				tbl += ","
			}
			tbl += t
		}
		cfg["table.include.list"] = tbl
	}
	return domain.ConnectorConfig{Name: name, Config: cfg}
}

// ensure interface compliance at compile time.
var _ connectors.KafkaConnectClient = (*connectors.FakeKafkaConnectClient)(nil)
