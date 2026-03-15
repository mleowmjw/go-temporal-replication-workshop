package temporal_test

import (
	"errors"
	"testing"
	"time"

	"app/internal/connectors"
	"app/internal/domain"
	"app/internal/store"
	apptemporal "app/internal/temporal"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSession2Activities(t *testing.T) (*apptemporal.Session2Activities, *store.InMemoryMetadataStore, *store.InMemoryConnectorStore, *connectors.FakeKafkaConnectClient) {
	t.Helper()
	ms := store.NewInMemoryMetadataStore()
	cs := store.NewInMemoryConnectorStore()
	kc := connectors.NewFakeKafkaConnectClient()
	deps := apptemporal.Session2Dependencies{
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
	}
	return apptemporal.NewSession2Activities(deps), ms, cs, kc
}

func validConnectorInput() apptemporal.CreateConnectorActivityInput {
	return apptemporal.CreateConnectorActivityInput{
		Spec: domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		Config: domain.ConnectorConfig{
			Name: "inventory-connector",
			Config: map[string]string{
				"connector.class":   "io.debezium.connector.postgresql.PostgresConnector",
				"database.hostname": "postgres",
			},
		},
	}
}

func TestCreateConnectorActivity(t *testing.T) {
	acts, _, cs, kc := newSession2Activities(t)
	in := validConnectorInput()

	require.NoError(t, acts.CreateConnectorActivity(actCtx, in))

	// Connector should exist in Kafka Connect fake.
	names := kc.ConnectorNames()
	assert.Contains(t, names, in.Config.Name)

	// Connector should be recorded in the store.
	rec, err := cs.GetConnector(actCtx, "t1", "p1", in.Config.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateUnassigned, rec.State)
}

func TestCreateConnectorActivity_InvalidConfig(t *testing.T) {
	acts, _, _, _ := newSession2Activities(t)
	in := apptemporal.CreateConnectorActivityInput{
		Spec:   domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		Config: domain.ConnectorConfig{Name: ""}, // invalid: no name
	}
	assert.ErrorContains(t, acts.CreateConnectorActivity(actCtx, in), "connector name")
}

func TestCreateConnectorActivity_KafkaConnectError(t *testing.T) {
	acts, _, _, kc := newSession2Activities(t)
	kc.CreateConnectorFn = func(_ domain.ConnectorConfig) error {
		return errors.New("kafka connect unavailable")
	}
	assert.ErrorContains(t, acts.CreateConnectorActivity(actCtx, validConnectorInput()), "create connector")
}

func TestWaitForConnectorRunningActivity_AlreadyRunning(t *testing.T) {
	acts, _, _, kc := newSession2Activities(t)
	in := validConnectorInput()

	// Create connector via activity (registers in both kc (RUNNING) and store (UNASSIGNED)).
	require.NoError(t, acts.CreateConnectorActivity(actCtx, in))

	// The fake starts connectors in RUNNING state; confirm.
	kc.SetState(in.Config.Name, domain.ConnectorStateRunning)

	waitIn := apptemporal.WaitForConnectorRunningActivityInput{
		Spec:          domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		ConnectorName: in.Config.Name,
		PollInterval:  1 * time.Millisecond,
		Timeout:       5 * time.Second,
	}
	// WaitForConnectorRunningActivity should find RUNNING and update the store itself.
	require.NoError(t, acts.WaitForConnectorRunningActivity(actCtx, waitIn))
}

func TestWaitForConnectorRunningActivity_FailedConnector(t *testing.T) {
	acts, _, _, kc := newSession2Activities(t)

	// Connector reports FAILED immediately.
	kc.GetConnectorStatusFn = func(name string) (domain.ConnectorStatus, error) {
		return domain.ConnectorStatus{
			Name:  name,
			State: domain.ConnectorStateFailed,
			Tasks: []domain.TaskStatus{{ID: 0, State: "FAILED", Trace: "connection refused"}},
		}, nil
	}

	in := apptemporal.WaitForConnectorRunningActivityInput{
		Spec:          domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		ConnectorName: "inventory-connector",
		PollInterval:  1 * time.Millisecond,
		Timeout:       5 * time.Second,
	}
	err := acts.WaitForConnectorRunningActivity(actCtx, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed")
}

func TestWaitForConnectorRunningActivity_Timeout(t *testing.T) {
	acts, _, _, kc := newSession2Activities(t)

	// Connector stays UNASSIGNED forever.
	kc.GetConnectorStatusFn = func(name string) (domain.ConnectorStatus, error) {
		return domain.ConnectorStatus{Name: name, State: domain.ConnectorStateUnassigned}, nil
	}

	in := apptemporal.WaitForConnectorRunningActivityInput{
		Spec:          domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		ConnectorName: "inventory-connector",
		PollInterval:  1 * time.Millisecond,
		Timeout:       5 * time.Millisecond,
	}
	err := acts.WaitForConnectorRunningActivity(actCtx, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not reach RUNNING")
}

func TestDeleteConnectorActivity(t *testing.T) {
	acts, _, cs, kc := newSession2Activities(t)
	in := validConnectorInput()
	require.NoError(t, acts.CreateConnectorActivity(actCtx, in))

	deleteIn := apptemporal.DeleteConnectorActivityInput{
		Spec:          in.Spec,
		ConnectorName: in.Config.Name,
	}
	require.NoError(t, acts.DeleteConnectorActivity(actCtx, deleteIn))

	assert.NotContains(t, kc.ConnectorNames(), in.Config.Name)

	recs, err := cs.ListConnectors(actCtx, "t1", "p1")
	require.NoError(t, err)
	assert.Empty(t, recs)
}

func TestDeleteConnectorActivity_KafkaConnectError(t *testing.T) {
	acts, _, _, kc := newSession2Activities(t)
	kc.DeleteConnectorFn = func(_ string) error {
		return errors.New("kafka connect unavailable")
	}
	in := apptemporal.DeleteConnectorActivityInput{
		Spec:          domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		ConnectorName: "any",
	}
	assert.ErrorContains(t, acts.DeleteConnectorActivity(actCtx, in), "delete connector")
}

func TestPauseConnectorActivity_KafkaConnectError(t *testing.T) {
	acts, _, _, kc := newSession2Activities(t)
	kc.PauseConnectorFn = func(_ string) error {
		return errors.New("kafka connect unavailable")
	}
	in := apptemporal.PauseConnectorActivityInput{
		Spec:          domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		ConnectorName: "any",
	}
	assert.ErrorContains(t, acts.PauseConnectorActivity(actCtx, in), "pause connector")
}

func TestResumeConnectorActivity_KafkaConnectError(t *testing.T) {
	acts, _, _, kc := newSession2Activities(t)
	kc.ResumeConnectorFn = func(_ string) error {
		return errors.New("kafka connect unavailable")
	}
	in := apptemporal.ResumeConnectorActivityInput{
		Spec:          domain.PipelineSpec{TenantID: "t1", PipelineID: "p1"},
		ConnectorName: "any",
	}
	assert.ErrorContains(t, acts.ResumeConnectorActivity(actCtx, in), "resume connector")
}

func TestPauseConnectorActivity(t *testing.T) {
	acts, _, cs, kc := newSession2Activities(t)
	in := validConnectorInput()
	require.NoError(t, acts.CreateConnectorActivity(actCtx, in))
	// Ensure the connector store entry exists with Unassigned first, then set Running
	require.NoError(t, cs.UpdateConnectorState(actCtx, "t1", "p1", in.Config.Name, domain.ConnectorStateRunning))
	kc.SetState(in.Config.Name, domain.ConnectorStateRunning)

	pauseIn := apptemporal.PauseConnectorActivityInput{
		Spec:          in.Spec,
		ConnectorName: in.Config.Name,
	}
	require.NoError(t, acts.PauseConnectorActivity(actCtx, pauseIn))

	rec, err := cs.GetConnector(actCtx, "t1", "p1", in.Config.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStatePaused, rec.State)
}

func TestResumeConnectorActivity(t *testing.T) {
	acts, _, cs, kc := newSession2Activities(t)
	in := validConnectorInput()
	require.NoError(t, acts.CreateConnectorActivity(actCtx, in))
	require.NoError(t, cs.UpdateConnectorState(actCtx, "t1", "p1", in.Config.Name, domain.ConnectorStatePaused))
	kc.SetState(in.Config.Name, domain.ConnectorStatePaused)

	resumeIn := apptemporal.ResumeConnectorActivityInput{
		Spec:          in.Spec,
		ConnectorName: in.Config.Name,
	}
	require.NoError(t, acts.ResumeConnectorActivity(actCtx, resumeIn))

	rec, err := cs.GetConnector(actCtx, "t1", "p1", in.Config.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateRunning, rec.State)
}
