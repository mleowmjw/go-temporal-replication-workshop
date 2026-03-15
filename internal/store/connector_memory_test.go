package store_test

import (
	"testing"

	"app/internal/domain"
	"app/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedConnector() domain.ConnectorRecord {
	return domain.ConnectorRecord{
		TenantID:   "t1",
		PipelineID: "p1",
		Name:       "inventory-connector",
		Config: domain.ConnectorConfig{
			Name:   "inventory-connector",
			Config: map[string]string{"connector.class": "io.debezium.connector.postgresql.PostgresConnector"},
		},
		State: domain.ConnectorStateUnassigned,
	}
}

func TestInMemoryConnectorStore_Create(t *testing.T) {
	s := store.NewInMemoryConnectorStore()
	rec := seedConnector()

	require.NoError(t, s.CreateConnector(ctx, rec))

	got, err := s.GetConnector(ctx, rec.TenantID, rec.PipelineID, rec.Name)
	require.NoError(t, err)
	assert.Equal(t, rec.Name, got.Name)
	assert.Equal(t, rec.State, got.State)
}

func TestInMemoryConnectorStore_DuplicateCreate(t *testing.T) {
	s := store.NewInMemoryConnectorStore()
	rec := seedConnector()
	require.NoError(t, s.CreateConnector(ctx, rec))
	assert.Error(t, s.CreateConnector(ctx, rec))
}

func TestInMemoryConnectorStore_GetNotFound(t *testing.T) {
	s := store.NewInMemoryConnectorStore()
	_, err := s.GetConnector(ctx, "t1", "p1", "missing")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestInMemoryConnectorStore_UpdateState(t *testing.T) {
	s := store.NewInMemoryConnectorStore()
	rec := seedConnector()
	require.NoError(t, s.CreateConnector(ctx, rec))

	require.NoError(t, s.UpdateConnectorState(ctx, rec.TenantID, rec.PipelineID, rec.Name, domain.ConnectorStateRunning))

	got, err := s.GetConnector(ctx, rec.TenantID, rec.PipelineID, rec.Name)
	require.NoError(t, err)
	assert.Equal(t, domain.ConnectorStateRunning, got.State)
}

func TestInMemoryConnectorStore_UpdateStateNotFound(t *testing.T) {
	s := store.NewInMemoryConnectorStore()
	err := s.UpdateConnectorState(ctx, "t1", "p1", "missing", domain.ConnectorStateRunning)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestInMemoryConnectorStore_Delete(t *testing.T) {
	s := store.NewInMemoryConnectorStore()
	rec := seedConnector()
	require.NoError(t, s.CreateConnector(ctx, rec))

	require.NoError(t, s.DeleteConnector(ctx, rec.TenantID, rec.PipelineID, rec.Name))

	_, err := s.GetConnector(ctx, rec.TenantID, rec.PipelineID, rec.Name)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestInMemoryConnectorStore_DeleteIdempotent(t *testing.T) {
	s := store.NewInMemoryConnectorStore()
	require.NoError(t, s.DeleteConnector(ctx, "t1", "p1", "nonexistent"))
}

func TestInMemoryConnectorStore_List(t *testing.T) {
	s := store.NewInMemoryConnectorStore()

	r1 := seedConnector()
	r2 := seedConnector()
	r2.Name = "second-connector"

	// Different pipeline — should not appear in list for p1.
	r3 := seedConnector()
	r3.PipelineID = "p2"

	require.NoError(t, s.CreateConnector(ctx, r1))
	require.NoError(t, s.CreateConnector(ctx, r2))
	require.NoError(t, s.CreateConnector(ctx, r3))

	recs, err := s.ListConnectors(ctx, "t1", "p1")
	require.NoError(t, err)
	assert.Len(t, recs, 2)

	recs2, err := s.ListConnectors(ctx, "t1", "p2")
	require.NoError(t, err)
	assert.Len(t, recs2, 1)
}
