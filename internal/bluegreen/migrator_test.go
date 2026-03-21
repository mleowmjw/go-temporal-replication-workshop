package bluegreen_test

import (
	"context"
	"testing"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeDatabaseMigrator_ExecuteSQL_AddColumn(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	require.NoError(t, f.ExecuteSQL(ctx, []string{
		"ALTER TABLE inventory.customers ADD COLUMN display_name TEXT",
	}))
	assert.True(t, f.HasColumn("display_name"))
}

func TestFakeDatabaseMigrator_ExecuteSQL_DropColumn(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	// Add first so we can drop it.
	require.NoError(t, f.ExecuteSQL(ctx, []string{
		"ALTER TABLE inventory.customers ADD COLUMN display_name TEXT",
		"ALTER TABLE inventory.customers DROP COLUMN display_name",
	}))
	assert.False(t, f.HasColumn("display_name"))
}

func TestFakeDatabaseMigrator_ExecuteSQL_DropColumnIfExists_Missing(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()
	// Should not error even when column doesn't exist.
	require.NoError(t, f.ExecuteSQL(ctx, []string{
		"ALTER TABLE inventory.customers DROP COLUMN IF EXISTS display_name",
	}))
}

func TestFakeDatabaseMigrator_QueryCheck_KnownColumn(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	res, err := f.QueryCheck(ctx, "SELECT id, email, full_name, status FROM inventory.customers WHERE id = $1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Count)
}

func TestFakeDatabaseMigrator_QueryCheck_UnknownColumn(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	_, err := f.QueryCheck(ctx, "SELECT id, email, display_name FROM inventory.customers WHERE id = $1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "display_name")
}

func TestFakeDatabaseMigrator_QueryCheck_AfterExpand(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	plan := validPlan()
	f.ApplyExpand(plan.ExpandSQL)

	// display_name should now exist.
	_, err := f.QueryCheck(ctx, "SELECT id, email, display_name FROM inventory.customers WHERE id = $1")
	require.NoError(t, err)
	// full_name should still exist.
	_, err = f.QueryCheck(ctx, "SELECT full_name FROM inventory.customers WHERE id = $1")
	require.NoError(t, err)
}

func TestFakeDatabaseMigrator_QueryCheck_AfterContract(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	plan := validPlan()
	f.ApplyExpand(plan.ExpandSQL)
	f.ApplyContract(plan.ContractSQL)

	// full_name should be gone.
	_, err := f.QueryCheck(ctx, "SELECT full_name FROM inventory.customers WHERE id = $1")
	require.Error(t, err)
	// display_name should still exist.
	_, err = f.QueryCheck(ctx, "SELECT display_name FROM inventory.customers WHERE id = $1")
	require.NoError(t, err)
}

func TestFakeDatabaseMigrator_SetReadOnly(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	assert.False(t, f.ReadOnly)
	require.NoError(t, f.SetReadOnly(ctx, true))
	assert.True(t, f.ReadOnly)
	require.NoError(t, f.SetReadOnly(ctx, false))
	assert.False(t, f.ReadOnly)
}

func TestFakeDatabaseMigrator_QueryCountOverride(t *testing.T) {
	f := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	query := "SELECT count(*) FROM inventory.customers WHERE display_name IS NULL AND full_name IS NOT NULL"
	// By default 0 (no missing backfills).
	f.ApplyExpand(validPlan().ExpandSQL)
	res, err := f.QueryCheck(ctx, query)
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Count)

	// Simulate a problem: 5 un-backfilled rows.
	f.QueryCounts[query] = 5
	res, err = f.QueryCheck(ctx, query)
	require.NoError(t, err)
	assert.Equal(t, int64(5), res.Count)
}
