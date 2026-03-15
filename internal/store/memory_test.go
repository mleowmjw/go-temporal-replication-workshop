package store_test

import (
	"context"
	"testing"

	"app/internal/domain"
	"app/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctx = context.Background()

func TestInMemoryMetadataStore_Tenant(t *testing.T) {
	s := store.NewInMemoryMetadataStore()
	tenant := domain.Tenant{ID: "t1", Name: "Acme", Tier: "standard"}

	require.NoError(t, s.CreateTenant(ctx, tenant))

	got, err := s.GetTenant(ctx, "t1")
	require.NoError(t, err)
	assert.Equal(t, tenant, got)

	t.Run("duplicate create returns error", func(t *testing.T) {
		assert.Error(t, s.CreateTenant(ctx, tenant))
	})

	t.Run("get non-existent returns ErrNotFound", func(t *testing.T) {
		_, err := s.GetTenant(ctx, "missing")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

func seedPipeline() domain.PipelineSpec {
	return domain.PipelineSpec{
		TenantID:   "t1",
		PipelineID: "p1",
		Name:       "test-pipeline",
		Source: domain.SourceSpec{
			Type:      domain.SourcePostgres,
			SecretRef: "pg-secret",
			Tables:    []string{"inventory.customers"},
		},
		Sink: domain.SinkSpec{
			Type:   domain.SinkSearch,
			Target: "customers-index",
		},
	}
}

func TestInMemoryMetadataStore_Pipeline(t *testing.T) {
	s := store.NewInMemoryMetadataStore()
	spec := seedPipeline()

	require.NoError(t, s.CreatePipeline(ctx, spec))

	got, err := s.GetPipeline(ctx, spec.TenantID, spec.PipelineID)
	require.NoError(t, err)
	assert.Equal(t, spec, got)

	t.Run("initial state is PENDING", func(t *testing.T) {
		state, err := s.GetPipelineState(ctx, spec.TenantID, spec.PipelineID)
		require.NoError(t, err)
		assert.Equal(t, domain.StatePending, state)
	})

	t.Run("set and get state", func(t *testing.T) {
		require.NoError(t, s.SetPipelineState(ctx, spec.TenantID, spec.PipelineID, domain.StateActive))
		state, err := s.GetPipelineState(ctx, spec.TenantID, spec.PipelineID)
		require.NoError(t, err)
		assert.Equal(t, domain.StateActive, state)
	})

	t.Run("update pipeline", func(t *testing.T) {
		updated := spec
		updated.Name = "updated-name"
		require.NoError(t, s.UpdatePipeline(ctx, updated))
		got, err := s.GetPipeline(ctx, spec.TenantID, spec.PipelineID)
		require.NoError(t, err)
		assert.Equal(t, "updated-name", got.Name)
	})

	t.Run("duplicate create returns error", func(t *testing.T) {
		assert.Error(t, s.CreatePipeline(ctx, spec))
	})

	t.Run("get non-existent returns ErrNotFound", func(t *testing.T) {
		_, err := s.GetPipeline(ctx, "t1", "missing")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("set state on non-existent returns ErrNotFound", func(t *testing.T) {
		err := s.SetPipelineState(ctx, "t1", "missing", domain.StateActive)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}
