package bluegreen_test

import (
	"context"
	"testing"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryDeploymentStore_CreateAndGet(t *testing.T) {
	s := bluegreen.NewInMemoryDeploymentStore()
	ctx := context.Background()

	d := bluegreen.Deployment{
		ID:   "deploy-1",
		Plan: validPlan(),
	}
	require.NoError(t, s.Create(ctx, d))

	got, err := s.Get(ctx, "deploy-1")
	require.NoError(t, err)
	assert.Equal(t, "deploy-1", got.ID)
	assert.Equal(t, bluegreen.PhasePending, got.Phase)
	assert.False(t, got.CreatedAt.IsZero())
}

func TestInMemoryDeploymentStore_DuplicateCreate(t *testing.T) {
	s := bluegreen.NewInMemoryDeploymentStore()
	ctx := context.Background()

	d := bluegreen.Deployment{ID: "deploy-1", Plan: validPlan()}
	require.NoError(t, s.Create(ctx, d))
	err := s.Create(ctx, d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestInMemoryDeploymentStore_GetNotFound(t *testing.T) {
	s := bluegreen.NewInMemoryDeploymentStore()
	_, err := s.Get(context.Background(), "missing")
	require.ErrorIs(t, err, bluegreen.ErrDeploymentNotFound)
}

func TestInMemoryDeploymentStore_UpdatePhase(t *testing.T) {
	s := bluegreen.NewInMemoryDeploymentStore()
	ctx := context.Background()

	d := bluegreen.Deployment{ID: "deploy-1", Plan: validPlan()}
	require.NoError(t, s.Create(ctx, d))

	require.NoError(t, s.UpdatePhase(ctx, "deploy-1", bluegreen.PhasePlanReview, "created"))
	require.NoError(t, s.UpdatePhase(ctx, "deploy-1", bluegreen.PhaseExpanding, "approved by alice"))

	got, err := s.Get(ctx, "deploy-1")
	require.NoError(t, err)
	assert.Equal(t, bluegreen.PhaseExpanding, got.Phase)
	assert.Len(t, got.History, 2)
	assert.Equal(t, bluegreen.PhasePlanReview, got.History[0].Phase)
	assert.Equal(t, "created", got.History[0].Reason)
	assert.Equal(t, bluegreen.PhaseExpanding, got.History[1].Phase)
}

func TestInMemoryDeploymentStore_UpdatePhase_SetsCompletedAt(t *testing.T) {
	s := bluegreen.NewInMemoryDeploymentStore()
	ctx := context.Background()

	d := bluegreen.Deployment{ID: "deploy-1", Plan: validPlan()}
	require.NoError(t, s.Create(ctx, d))
	require.NoError(t, s.UpdatePhase(ctx, "deploy-1", bluegreen.PhaseComplete, "done"))

	got, err := s.Get(ctx, "deploy-1")
	require.NoError(t, err)
	assert.NotNil(t, got.CompletedAt)
}

func TestInMemoryDeploymentStore_UpdatePhase_NotFound(t *testing.T) {
	s := bluegreen.NewInMemoryDeploymentStore()
	err := s.UpdatePhase(context.Background(), "missing", bluegreen.PhaseExpanding, "")
	require.ErrorIs(t, err, bluegreen.ErrDeploymentNotFound)
}
