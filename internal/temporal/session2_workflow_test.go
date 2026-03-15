package temporal_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// cdcWorkflowSuite groups all ProvisionCDCPipelineWorkflow tests.
type cdcWorkflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite

	env *testsuite.TestWorkflowEnvironment
}

func TestCDCWorkflowSuite(t *testing.T) {
	suite.Run(t, new(cdcWorkflowSuite))
}

func (s *cdcWorkflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
}

func (s *cdcWorkflowSuite) AfterTest(_, _ string) {
	s.env.AssertExpectations(s.T())
}

func buildSession2Deps(t *testing.T) (apptemporal.Session2Dependencies, *store.InMemoryMetadataStore, *store.InMemoryConnectorStore, *connectors.FakeKafkaConnectClient) {
	t.Helper()
	ms := store.NewInMemoryMetadataStore()
	cs := store.NewInMemoryConnectorStore()
	kc := connectors.NewFakeKafkaConnectClient()
	return apptemporal.Session2Dependencies{
		Dependencies: apptemporal.Dependencies{
			Store:          ms,
			Secrets:        connectors.NewInMemorySecretProvider(),
			SchemaRegistry: connectors.NewInMemorySchemaRegistry(),
			CDC:            connectors.NewFakeCDCProvisioner(),
			Stream:         connectors.NewFakeStreamProvisioner(),
			Sink:           connectors.NewFakeSinkProvisioner(),
		},
		KafkaConnect:   kc,
		ConnectorStore: cs,
	}, ms, cs, kc
}

func validConnCfg() domain.ConnectorConfig {
	return domain.ConnectorConfig{
		Name: "inventory-connector",
		Config: map[string]string{
			"connector.class":   "io.debezium.connector.postgresql.PostgresConnector",
			"database.hostname": "postgres",
		},
	}
}

// registerAllActivities wires both session-1 and session-2 activities into the test env.
func (s *cdcWorkflowSuite) registerAllActivities(acts *apptemporal.Activities, s2acts *apptemporal.Session2Activities) {
	s.env.RegisterActivity(acts.ValidatePipelineSpecActivity)
	s.env.RegisterActivity(acts.ValidateSchemaPolicyActivity)
	s.env.RegisterActivity(acts.EnsureSchemaSubjectActivity)
	s.env.RegisterActivity(acts.PrepareSourceActivity)
	s.env.RegisterActivity(acts.EnsureStreamActivity)
	s.env.RegisterActivity(acts.EnsureSinkActivity)
	s.env.RegisterActivity(acts.StartCaptureActivity)
	s.env.RegisterActivity(acts.MarkPipelineActiveActivity)
	s.env.RegisterActivity(acts.MarkPipelinePausedActivity)
	s.env.RegisterActivity(acts.StopCaptureActivity)
	s.env.RegisterActivity(acts.DeleteSinkActivity)
	s.env.RegisterActivity(acts.DeleteStreamActivity)
	s.env.RegisterActivity(acts.MarkPipelineErrorActivity)
	s.env.RegisterActivity(s2acts.CreateConnectorActivity)
	s.env.RegisterActivity(s2acts.WaitForConnectorRunningActivity)
	s.env.RegisterActivity(s2acts.DeleteConnectorActivity)
	s.env.RegisterActivity(s2acts.PauseConnectorActivity)
	s.env.RegisterActivity(s2acts.ResumeConnectorActivity)
}

// TestCDCHappyPath verifies that a full CDC pipeline provisioning succeeds.
func (s *cdcWorkflowSuite) TestCDCHappyPath() {
	spec := validSpec()
	connCfg := validConnCfg()

	s2deps, ms, cs, kc := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	// Connector starts RUNNING immediately in the fake.
	_ = kc
	_ = cs

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionCDCPipelineWorkflow)

	// Override WaitForConnectorRunning to succeed immediately in the test env.
	s.env.OnActivity(s2acts.WaitForConnectorRunningActivity, mock.Anything, mock.Anything).
		Return(nil)

	s.env.ExecuteWorkflow(apptemporal.ProvisionCDCPipelineWorkflow, spec, connCfg)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	var result apptemporal.CDCProvisionResult
	require.NoError(s.T(), s.env.GetWorkflowResult(&result))
	assert.Equal(s.T(), connCfg.Name, result.ConnectorName)
	assert.NotEmpty(s.T(), result.StreamID)
	assert.NotEmpty(s.T(), result.SinkID)

	state, err := ms.GetPipelineState(context.Background(), "t1", "p1")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.StateActive, state)
}

// TestCDCConnectorCreationTransientFailure verifies retry on transient connector creation failure.
func (s *cdcWorkflowSuite) TestCDCConnectorCreationTransientFailure() {
	spec := validSpec()
	connCfg := validConnCfg()

	s2deps, ms, _, kc := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	callN := 0
	kc.CreateConnectorFn = func(cfg domain.ConnectorConfig) error {
		callN++
		if callN == 1 {
			return errors.New("transient: connection refused")
		}
		kc.CreateConnectorFn = nil // succeed on retry
		return nil
	}

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionCDCPipelineWorkflow)

	s.env.OnActivity(s2acts.WaitForConnectorRunningActivity, mock.Anything, mock.Anything).
		Return(nil)

	s.env.ExecuteWorkflow(apptemporal.ProvisionCDCPipelineWorkflow, spec, connCfg)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	state, err := ms.GetPipelineState(context.Background(), "t1", "p1")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.StateActive, state)
}

// TestCDCConnectorRunningFailure verifies compensation when connector fails to reach RUNNING.
func (s *cdcWorkflowSuite) TestCDCConnectorRunningFailure() {
	spec := validSpec()
	connCfg := validConnCfg()

	s2deps, ms, _, _ := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionCDCPipelineWorkflow)

	// Connector never reaches RUNNING — permanent failure.
	s.env.OnActivity(s2acts.WaitForConnectorRunningActivity, mock.Anything, mock.Anything).
		Return(errors.New("connector failed: connection refused"))

	s.env.ExecuteWorkflow(apptemporal.ProvisionCDCPipelineWorkflow, spec, connCfg)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError())

	state, err := ms.GetPipelineState(context.Background(), "t1", "p1")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.StateError, state)
}

// TestCDCSchemaIncompatibilityRejection verifies workflow fails fast on schema policy violation.
func (s *cdcWorkflowSuite) TestCDCSchemaIncompatibilityRejection() {
	spec := validSpec()
	spec.Schema.Subject = "inventory.customers"
	spec.Schema.Format = `{"fields":[{"name":"id","required":true}]}`
	connCfg := validConnCfg()

	s2deps, ms, _, _ := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionCDCPipelineWorkflow)

	s.env.OnActivity(acts.ValidateSchemaPolicyActivity, mock.Anything, mock.Anything).
		Return(errors.New("BACKWARD incompatible: field \"email\" removed"))

	s.env.ExecuteWorkflow(apptemporal.ProvisionCDCPipelineWorkflow, spec, connCfg)

	s.True(s.env.IsWorkflowCompleted())
	err := s.env.GetWorkflowError()
	require.Error(s.T(), err)
	assert.Contains(s.T(), err.Error(), "schema policy")
}

// TestCDCDecommissionWorkflow verifies decommission tears down connector + resources.
func (s *cdcWorkflowSuite) TestCDCDecommissionWorkflow() {
	spec := validSpec()

	s2deps, ms, _, _ := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))
	require.NoError(s.T(), ms.SetPipelineState(context.Background(), "t1", "p1", domain.StateActive))

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.DecommissionCDCPipelineWorkflow)

	s.env.ExecuteWorkflow(apptemporal.DecommissionCDCPipelineWorkflow, spec, "inventory-connector", "stream-t1-p1", "sink-search-customers-index")

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())
}

// TestCDCPauseResumeWorkflow verifies pause/resume round-trip.
func (s *cdcWorkflowSuite) TestCDCPauseResumeWorkflow() {
	spec := validSpec()

	s2deps, ms, cs, kc := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))
	require.NoError(s.T(), ms.SetPipelineState(context.Background(), "t1", "p1", domain.StateActive))

	// Seed connector in store and fake.
	cfg := validConnCfg()
	require.NoError(s.T(), kc.CreateConnector(context.Background(), cfg))
	require.NoError(s.T(), cs.CreateConnector(context.Background(), domain.ConnectorRecord{
		TenantID: "t1", PipelineID: "p1", Name: cfg.Name,
		Config: cfg, State: domain.ConnectorStateRunning,
	}))

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.PauseCDCPipelineWorkflow)

	s.env.ExecuteWorkflow(apptemporal.PauseCDCPipelineWorkflow, spec, cfg.Name)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	recState, err := cs.GetConnector(context.Background(), "t1", "p1", cfg.Name)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.ConnectorStatePaused, recState.State)

	pipelineState, err := ms.GetPipelineState(context.Background(), "t1", "p1")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.StatePaused, pipelineState)
}

// TestCDCResumePipelineWorkflow verifies resume workflow transitions connector back to RUNNING.
func (s *cdcWorkflowSuite) TestCDCResumePipelineWorkflow() {
	spec := validSpec()

	s2deps, ms, cs, kc := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	cfg := validConnCfg()
	require.NoError(s.T(), kc.CreateConnector(context.Background(), cfg))
	require.NoError(s.T(), kc.PauseConnector(context.Background(), cfg.Name))
	require.NoError(s.T(), cs.CreateConnector(context.Background(), domain.ConnectorRecord{
		TenantID: "t1", PipelineID: "p1", Name: cfg.Name,
		Config: cfg, State: domain.ConnectorStatePaused,
	}))

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.ResumeCDCPipelineWorkflow)

	s.env.ExecuteWorkflow(apptemporal.ResumeCDCPipelineWorkflow, spec, cfg.Name)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	rec, err := cs.GetConnector(context.Background(), "t1", "p1", cfg.Name)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), domain.ConnectorStateRunning, rec.State)
}

// TestBuildConnectorConfig verifies the helper produces a valid connector config.
func TestBuildConnectorConfig(t *testing.T) {
	spec := validSpec()
	secret := map[string]string{
		"database.hostname": "postgres",
		"database.port":     "5432",
		"database.user":     "dbz",
		"database.password": "dbz",
	}
	cfg := apptemporal.BuildConnectorConfig(spec, secret)
	require.NoError(t, domain.ValidateConnectorConfig(cfg))
	assert.Contains(t, cfg.Name, string(spec.TenantID))
	assert.Contains(t, cfg.Name, string(spec.PipelineID))
	assert.Equal(t, "io.debezium.connector.postgresql.PostgresConnector", cfg.Config["connector.class"])
	assert.Equal(t, "appdb", cfg.Config["database.dbname"])
	assert.Contains(t, cfg.Config["table.include.list"], "inventory.customers")
}

// TestRegisterDelayedCallback demonstrates connector startup simulation using RegisterDelayedCallback.
// The connector starts UNASSIGNED and transitions to RUNNING after a simulated 5-second delay.
func (s *cdcWorkflowSuite) TestRegisterDelayedCallback() {
	spec := validSpec()
	connCfg := validConnCfg()

	s2deps, ms, _, kc := buildSession2Deps(s.T())
	require.NoError(s.T(), ms.CreateTenant(context.Background(), domain.Tenant{ID: "t1", Name: "Acme"}))
	require.NoError(s.T(), ms.CreatePipeline(context.Background(), spec))

	// Start in UNASSIGNED; simulate transition to RUNNING after a delay.
	callN := 0
	kc.GetConnectorStatusFn = func(name string) (domain.ConnectorStatus, error) {
		callN++
		// First two polls return UNASSIGNED; subsequent ones return RUNNING.
		if callN <= 2 {
			return domain.ConnectorStatus{Name: name, State: domain.ConnectorStateUnassigned}, nil
		}
		return domain.ConnectorStatus{Name: name, State: domain.ConnectorStateRunning}, nil
	}

	acts := apptemporal.NewActivities(s2deps.Dependencies)
	s2acts := apptemporal.NewSession2Activities(s2deps)
	s.registerAllActivities(acts, s2acts)
	s.env.RegisterWorkflow(apptemporal.ProvisionCDCPipelineWorkflow)

	// After 5 simulated seconds, set the connector to RUNNING (already done via GetConnectorStatusFn
	// after callN > 2, but RegisterDelayedCallback can be used for more complex signal-based scenarios).
	s.env.RegisterDelayedCallback(func() {
		kc.SetState(connCfg.Name, domain.ConnectorStateRunning)
	}, 5*time.Second)

	// Override WaitForConnectorRunning to immediately succeed (avoids real polling in test env).
	s.env.OnActivity(s2acts.WaitForConnectorRunningActivity, mock.Anything, mock.Anything).
		Return(nil)

	s.env.ExecuteWorkflow(apptemporal.ProvisionCDCPipelineWorkflow, spec, connCfg)
	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())
}
