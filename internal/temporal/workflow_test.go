package temporal_test

import (
	"context"
	"errors"
	"testing"

	"app/internal/connectors"
	"app/internal/domain"
	"app/internal/store"
	apptemporal "app/internal/temporal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/testsuite"
)

// workflowSuite groups all ProvisionPipelineWorkflow tests.
type workflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite

	env *testsuite.TestWorkflowEnvironment
}

func TestWorkflowSuite(t *testing.T) {
	suite.Run(t, new(workflowSuite))
}

func (s *workflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
}

func (s *workflowSuite) AfterTest(_, _ string) {
	s.env.AssertExpectations(s.T())
}

// buildDeps creates a full set of in-memory dependencies for tests.
func buildDeps(t *testing.T) (apptemporal.Dependencies, *store.InMemoryMetadataStore) {
	t.Helper()
	ms := store.NewInMemoryMetadataStore()
	return apptemporal.Dependencies{
		Store:          ms,
		Secrets:        connectors.NewInMemorySecretProvider(),
		SchemaRegistry: connectors.NewInMemorySchemaRegistry(),
		CDC:            connectors.NewFakeCDCProvisioner(),
		Stream:         connectors.NewFakeStreamProvisioner(),
		Sink:           connectors.NewFakeSinkProvisioner(),
	}, ms
}

func validSpec() domain.PipelineSpec {
	return domain.PipelineSpec{
		TenantID:   "t1",
		PipelineID: "p1",
		Name:       "inventory-search",
		Source: domain.SourceSpec{
			Type:      domain.SourcePostgres,
			SecretRef: "pg-secret",
			Database:  "appdb",
			Schema:    "inventory",
			Tables:    []string{"inventory.customers"},
		},
		Sink: domain.SinkSpec{
			Type:   domain.SinkSearch,
			Target: "customers-index",
		},
		Schema: domain.SchemaSpec{
			Compatibility: domain.CompatBackward,
		},
	}
}

// registerActivities registers all activity methods into the test environment.
func (s *workflowSuite) registerActivities(acts *apptemporal.Activities) {
	s.env.RegisterActivity(acts.ValidatePipelineSpecActivity)
	s.env.RegisterActivity(acts.ValidateSchemaPolicyActivity)
	s.env.RegisterActivity(acts.EnsureSchemaSubjectActivity)
	s.env.RegisterActivity(acts.PrepareSourceActivity)
	s.env.RegisterActivity(acts.EnsureStreamActivity)
	s.env.RegisterActivity(acts.EnsureSinkActivity)
	s.env.RegisterActivity(acts.StartCaptureActivity)
	s.env.RegisterActivity(acts.MarkPipelineActiveActivity)
	s.env.RegisterActivity(acts.StopCaptureActivity)
	s.env.RegisterActivity(acts.DeleteSinkActivity)
	s.env.RegisterActivity(acts.DeleteStreamActivity)
	s.env.RegisterActivity(acts.MarkPipelineErrorActivity)
}

// TestHappyPath verifies full provisioning succeeds and pipeline ends up ACTIVE.
func (s *workflowSuite) TestHappyPath() {
	spec := validSpec()
	deps, ms := buildDeps(s.T())

	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	acts := apptemporal.NewActivities(deps)
	s.registerActivities(acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionPipelineWorkflow)

	s.env.ExecuteWorkflow(apptemporal.ProvisionPipelineWorkflow, spec)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	var result domain.ProvisionResult
	require.NoError(s.T(), s.env.GetWorkflowResult(&result))
	assert.NotEmpty(s.T(), result.StreamID, "stream should be provisioned")
	assert.NotEmpty(s.T(), result.SinkID, "sink should be provisioned")
	assert.NotEmpty(s.T(), result.CaptureID, "capture should be started")

	state, err := ms.GetPipelineState(context.Background(), "t1", "p1")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.StateActive, state)
}

// TestTransientFailureWithRetry verifies that a transient sink failure retries and eventually succeeds.
func (s *workflowSuite) TestTransientFailureWithRetry() {
	spec := validSpec()
	deps, ms := buildDeps(s.T())

	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	// Fail the first call, succeed on second.
	callN := 0
	fakeSink := deps.Sink.(*connectors.FakeSinkProvisioner)
	fakeSink.EnsureSinkFn = func(n int, sp domain.SinkSpec) (string, error) {
		callN++
		if callN == 1 {
			return "", errors.New("transient: connection refused")
		}
		return "sink-search-customers-index", nil
	}

	acts := apptemporal.NewActivities(deps)
	s.registerActivities(acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionPipelineWorkflow)

	s.env.ExecuteWorkflow(apptemporal.ProvisionPipelineWorkflow, spec)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	state, err := ms.GetPipelineState(context.Background(), "t1", "p1")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.StateActive, state)
}

// TestPermanentFailureWithCompensation verifies cleanup runs when StartCapture fails permanently.
func (s *workflowSuite) TestPermanentFailureWithCompensation() {
	spec := validSpec()
	deps, ms := buildDeps(s.T())

	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	fakeStream := deps.Stream.(*connectors.FakeStreamProvisioner)
	fakeSink := deps.Sink.(*connectors.FakeSinkProvisioner)
	fakeCDC := deps.CDC.(*connectors.FakeCDCProvisioner)

	// StartCapture always fails.
	fakeCDC.StartCaptureFn = func(_ domain.SourceSpec) (connectors.CaptureHandle, error) {
		return connectors.CaptureHandle{}, errors.New("permanent: CDC engine unavailable")
	}

	acts := apptemporal.NewActivities(deps)
	s.registerActivities(acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionPipelineWorkflow)

	s.env.ExecuteWorkflow(apptemporal.ProvisionPipelineWorkflow, spec)

	s.True(s.env.IsWorkflowCompleted())
	// Workflow should return an error.
	assert.Error(s.T(), s.env.GetWorkflowError())

	// Compensation: sink and stream should be cleaned up.
	assert.Empty(s.T(), fakeSink.Sinks, "sink should be deleted during compensation")
	assert.Empty(s.T(), fakeStream.Streams, "stream should be deleted during compensation")

	// Pipeline state should be ERROR.
	state, err := ms.GetPipelineState(context.Background(), "t1", "p1")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.StateError, state)
}

// TestSchemaIncompatibilityRejection verifies the workflow fails fast on schema policy violation.
func (s *workflowSuite) TestSchemaIncompatibilityRejection() {
	spec := validSpec()
	spec.Schema.Subject = "inventory.customers"
	spec.Schema.Format = `{"fields":[{"name":"id","required":true}]}`

	deps, ms := buildDeps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	// Pre-register a schema so compatibility is checked on update.
	_, err := deps.SchemaRegistry.Register(
		context.Background(),
		spec.Schema.Subject,
		`{"fields":[{"name":"id","required":true},{"name":"email","required":true}]}`,
		domain.CompatBackward,
	)
	require.NoError(s.T(), err)

	// The new schema removes "email" — BACKWARD incompatible.
	// We simulate this by making ValidateSchemaPolicyActivity return an error.
	acts := apptemporal.NewActivities(deps)
	s.registerActivities(acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionPipelineWorkflow)

	// Override ValidateSchemaPolicyActivity to return an incompatibility error.
	s.env.OnActivity(acts.ValidateSchemaPolicyActivity, mock.Anything, mock.Anything).
		Return(errors.New("BACKWARD incompatible: field \"email\" removed"))

	s.env.ExecuteWorkflow(apptemporal.ProvisionPipelineWorkflow, spec)

	s.True(s.env.IsWorkflowCompleted())
	err = s.env.GetWorkflowError()
	require.Error(s.T(), err)
	assert.Contains(s.T(), err.Error(), "schema policy")
}
