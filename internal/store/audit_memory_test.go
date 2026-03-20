package store_test

import (
	"context"
	"testing"
	"time"

	"app/internal/domain"
	"app/internal/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditStore_RecordAndList(t *testing.T) {
	s := store.NewInMemoryAuditStore()
	ctx := context.Background()

	e1 := domain.AuditEvent{
		TenantID:   "t1",
		PipelineID: "p1",
		Action:     domain.AuditActionCreate,
		Actor:      "user@example.com",
		Timestamp:  time.Now().UTC(),
	}
	e2 := domain.AuditEvent{
		TenantID:   "t1",
		PipelineID: "p1",
		Action:     domain.AuditActionPause,
		Actor:      "user@example.com",
		Timestamp:  time.Now().UTC(),
	}
	// Different pipeline — should not appear in t1/p1 results.
	e3 := domain.AuditEvent{
		TenantID:   "t1",
		PipelineID: "p2",
		Action:     domain.AuditActionCreate,
		Actor:      "user@example.com",
		Timestamp:  time.Now().UTC(),
	}

	require.NoError(t, s.RecordEvent(ctx, e1))
	require.NoError(t, s.RecordEvent(ctx, e2))
	require.NoError(t, s.RecordEvent(ctx, e3))

	events, err := s.ListEvents(ctx, "t1", "p1")
	require.NoError(t, err)
	assert.Len(t, events, 2)
	assert.Equal(t, domain.AuditActionCreate, events[0].Action)
	assert.Equal(t, domain.AuditActionPause, events[1].Action)

	// Each recorded event should have an ID assigned.
	assert.NotEmpty(t, events[0].ID)
	assert.NotEmpty(t, events[1].ID)
}

func TestAuditStore_EmptyList(t *testing.T) {
	s := store.NewInMemoryAuditStore()
	events, err := s.ListEvents(context.Background(), "nobody", "nothing")
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestAuditStore_RequiresTenantAndPipeline(t *testing.T) {
	s := store.NewInMemoryAuditStore()
	ctx := context.Background()

	err := s.RecordEvent(ctx, domain.AuditEvent{Action: domain.AuditActionCreate})
	assert.Error(t, err)

	err = s.RecordEvent(ctx, domain.AuditEvent{TenantID: "t1", Action: domain.AuditActionCreate})
	assert.Error(t, err)
}

func TestAuditStore_AutoAssignsIDAndTimestamp(t *testing.T) {
	s := store.NewInMemoryAuditStore()
	ctx := context.Background()

	require.NoError(t, s.RecordEvent(ctx, domain.AuditEvent{
		TenantID:   "t1",
		PipelineID: "p1",
		Action:     domain.AuditActionCreate,
	}))

	events, err := s.ListEvents(ctx, "t1", "p1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.NotEmpty(t, events[0].ID)
	assert.False(t, events[0].Timestamp.IsZero())
}
