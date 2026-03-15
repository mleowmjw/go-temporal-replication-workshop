package connectors_test

import (
	"errors"
	"testing"

	"app/internal/connectors"
	"app/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConnectorCfg() domain.ConnectorConfig {
	return domain.ConnectorConfig{
		Name: "inventory-connector",
		Config: map[string]string{
			"connector.class":   "io.debezium.connector.postgresql.PostgresConnector",
			"database.hostname": "postgres",
		},
	}
}

func TestFakeKafkaConnectClient_HappyPath(t *testing.T) {
	f := connectors.NewFakeKafkaConnectClient()
	cfg := validConnectorCfg()

	require.NoError(t, f.CreateConnector(ctx, cfg))

	names, err := f.ListConnectors(ctx)
	require.NoError(t, err)
	assert.Contains(t, names, cfg.Name)

	status, err := f.GetConnectorStatus(ctx, cfg.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateRunning, status.State)
	assert.Equal(t, cfg.Name, status.Name)

	require.NoError(t, f.DeleteConnector(ctx, cfg.Name))
	names, err = f.ListConnectors(ctx)
	require.NoError(t, err)
	assert.NotContains(t, names, cfg.Name)
}

func TestFakeKafkaConnectClient_DuplicateCreate(t *testing.T) {
	f := connectors.NewFakeKafkaConnectClient()
	cfg := validConnectorCfg()
	require.NoError(t, f.CreateConnector(ctx, cfg))
	err := f.CreateConnector(ctx, cfg)
	assert.ErrorContains(t, err, "already exists")
}

func TestFakeKafkaConnectClient_DeleteIdempotent(t *testing.T) {
	f := connectors.NewFakeKafkaConnectClient()
	require.NoError(t, f.DeleteConnector(ctx, "nonexistent"))
}

func TestFakeKafkaConnectClient_PauseResume(t *testing.T) {
	f := connectors.NewFakeKafkaConnectClient()
	cfg := validConnectorCfg()
	require.NoError(t, f.CreateConnector(ctx, cfg))

	require.NoError(t, f.PauseConnector(ctx, cfg.Name))
	status, err := f.GetConnectorStatus(ctx, cfg.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStatePaused, status.State)

	require.NoError(t, f.ResumeConnector(ctx, cfg.Name))
	status, err = f.GetConnectorStatus(ctx, cfg.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateRunning, status.State)
}

func TestFakeKafkaConnectClient_GetStatusNotFound(t *testing.T) {
	f := connectors.NewFakeKafkaConnectClient()
	_, err := f.GetConnectorStatus(ctx, "missing")
	assert.ErrorIs(t, err, connectors.ErrNotFound)
}

func TestFakeKafkaConnectClient_SetState(t *testing.T) {
	f := connectors.NewFakeKafkaConnectClient()
	cfg := validConnectorCfg()
	require.NoError(t, f.CreateConnector(ctx, cfg))

	f.SetState(cfg.Name, domain.ConnectorStateFailed)
	status, err := f.GetConnectorStatus(ctx, cfg.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateFailed, status.State)
}

func TestFakeKafkaConnectClient_InjectableErrors(t *testing.T) {
	errBoom := errors.New("boom")

	t.Run("CreateConnectorFn error", func(t *testing.T) {
		f := connectors.NewFakeKafkaConnectClient()
		f.CreateConnectorFn = func(_ domain.ConnectorConfig) error { return errBoom }
		assert.ErrorIs(t, f.CreateConnector(ctx, validConnectorCfg()), errBoom)
	})

	t.Run("GetConnectorStatusFn error", func(t *testing.T) {
		f := connectors.NewFakeKafkaConnectClient()
		f.GetConnectorStatusFn = func(_ string) (domain.ConnectorStatus, error) {
			return domain.ConnectorStatus{}, errBoom
		}
		_, err := f.GetConnectorStatus(ctx, "any")
		assert.ErrorIs(t, err, errBoom)
	})

	t.Run("DeleteConnectorFn error", func(t *testing.T) {
		f := connectors.NewFakeKafkaConnectClient()
		f.DeleteConnectorFn = func(_ string) error { return errBoom }
		assert.ErrorIs(t, f.DeleteConnector(ctx, "any"), errBoom)
	})

	t.Run("PauseConnectorFn error", func(t *testing.T) {
		f := connectors.NewFakeKafkaConnectClient()
		f.PauseConnectorFn = func(_ string) error { return errBoom }
		assert.ErrorIs(t, f.PauseConnector(ctx, "any"), errBoom)
	})

	t.Run("ResumeConnectorFn error", func(t *testing.T) {
		f := connectors.NewFakeKafkaConnectClient()
		f.ResumeConnectorFn = func(_ string) error { return errBoom }
		assert.ErrorIs(t, f.ResumeConnector(ctx, "any"), errBoom)
	})
}

func TestFakeKafkaConnectClient_ConnectorNames(t *testing.T) {
	f := connectors.NewFakeKafkaConnectClient()
	require.NoError(t, f.CreateConnector(ctx, domain.ConnectorConfig{
		Name:   "c1",
		Config: map[string]string{"connector.class": "x"},
	}))
	require.NoError(t, f.CreateConnector(ctx, domain.ConnectorConfig{
		Name:   "c2",
		Config: map[string]string{"connector.class": "x"},
	}))
	names := f.ConnectorNames()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "c1")
	assert.Contains(t, names, "c2")
}
