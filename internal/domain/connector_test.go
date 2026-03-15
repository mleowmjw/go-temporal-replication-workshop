package domain_test

import (
	"testing"

	"app/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConnectorConfig() domain.ConnectorConfig {
	return domain.ConnectorConfig{
		Name: "inventory-connector",
		Config: map[string]string{
			"connector.class":   "io.debezium.connector.postgresql.PostgresConnector",
			"database.hostname": "postgres",
		},
	}
}

func TestValidateConnectorConfig(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		require.NoError(t, domain.ValidateConnectorConfig(validConnectorConfig()))
	})

	t.Run("missing name", func(t *testing.T) {
		c := validConnectorConfig()
		c.Name = ""
		assert.ErrorContains(t, domain.ValidateConnectorConfig(c), "connector name")
	})

	t.Run("empty config map", func(t *testing.T) {
		c := validConnectorConfig()
		c.Config = nil
		assert.ErrorContains(t, domain.ValidateConnectorConfig(c), "connector config must not be empty")
	})

	t.Run("missing connector.class", func(t *testing.T) {
		c := validConnectorConfig()
		delete(c.Config, "connector.class")
		assert.ErrorContains(t, domain.ValidateConnectorConfig(c), "connector.class")
	})
}

func TestConnectorStateConstants(t *testing.T) {
	assert.Equal(t, domain.ConnectorState("RUNNING"), domain.ConnectorStateRunning)
	assert.Equal(t, domain.ConnectorState("PAUSED"), domain.ConnectorStatePaused)
	assert.Equal(t, domain.ConnectorState("FAILED"), domain.ConnectorStateFailed)
	assert.Equal(t, domain.ConnectorState("UNASSIGNED"), domain.ConnectorStateUnassigned)
	assert.Equal(t, domain.ConnectorState("STOPPED"), domain.ConnectorStateStopped)
}
