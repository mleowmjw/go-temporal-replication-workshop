package bluegreen_test

import (
	"context"
	"testing"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppCompat_OriginalSchema verifies that on the original schema:
//   - Blue app queries PASS  (uses full_name which exists)
//   - Green app queries FAIL (uses display_name which does not yet exist)
//
// This proves the migration is actually needed.
func TestAppCompat_OriginalSchema(t *testing.T) {
	app := bluegreen.NewCustomerApp()
	db := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	result := bluegreen.RunAppCompat(ctx, app, db)

	assert.True(t, result.BluePass, "blue app must work on original schema; errors: %v", result.BlueErrors)
	assert.False(t, result.GreenPass, "green app must fail on original schema (display_name missing)")
	require.NotEmpty(t, result.GreenErrors)
}

// TestAppCompat_AfterExpand verifies that after expand:
//   - Blue app queries PASS  (full_name still exists)
//   - Green app queries PASS (display_name + phone now exist)
//
// This is the core blue-green guarantee: BOTH versions coexist safely.
func TestAppCompat_AfterExpand(t *testing.T) {
	app := bluegreen.NewCustomerApp()
	db := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	plan := validPlan()
	db.ApplyExpand(plan.ExpandSQL)

	result := bluegreen.RunAppCompat(ctx, app, db)

	assert.True(t, result.BluePass, "blue app must still work after expand; errors: %v", result.BlueErrors)
	assert.True(t, result.GreenPass, "green app must work after expand; errors: %v", result.GreenErrors)
}

// TestAppCompat_AfterContract verifies that after contract:
//   - Blue app queries FAIL  (full_name has been dropped)
//   - Green app queries PASS (display_name + phone still exist)
//
// This is the expected final state: old app can no longer run,
// traffic is 100% on the new version.
func TestAppCompat_AfterContract(t *testing.T) {
	app := bluegreen.NewCustomerApp()
	db := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	plan := validPlan()
	db.ApplyExpand(plan.ExpandSQL)
	db.ApplyContract(plan.ContractSQL)

	result := bluegreen.RunAppCompat(ctx, app, db)

	assert.False(t, result.BluePass, "blue app must fail after contract (full_name gone)")
	require.NotEmpty(t, result.BlueErrors)
	assert.True(t, result.GreenPass, "green app must still work after contract; errors: %v", result.GreenErrors)
}

// TestAppCompat_BlueQueriesExpectColumns documents exactly which columns
// each blue query needs, confirming the schema prerequisites.
func TestAppCompat_BlueQueriesExpectColumns(t *testing.T) {
	app := bluegreen.NewCustomerApp()
	db := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	for _, q := range app.BlueQueries() {
		_, err := db.QueryCheck(ctx, q.SQL)
		assert.NoError(t, err, "blue query %q should pass on original schema", q.Name)
	}
}

// TestAppCompat_GreenQueriesNeedExpand documents that each green query
// fails individually on the original schema.
func TestAppCompat_GreenQueriesNeedExpand(t *testing.T) {
	app := bluegreen.NewCustomerApp()
	db := bluegreen.NewFakeDatabaseMigrator()
	ctx := context.Background()

	// At least one green query must fail -- display_name or phone is missing.
	failCount := 0
	for _, q := range app.GreenQueries() {
		if _, err := db.QueryCheck(ctx, q.SQL); err != nil {
			failCount++
		}
	}
	assert.Greater(t, failCount, 0, "some green queries should fail on original schema")
}

// TestAppCompat_GreenWritesNewColumns verifies that the green InsertCustomer
// writes display_name and phone (the new schema shape).
// The expand SQL's UPDATE backfills display_name from full_name for all
// existing rows; new green-app inserts only need to write the new columns
// since full_name is nullable.
func TestAppCompat_GreenWritesNewColumns(t *testing.T) {
	app := bluegreen.NewCustomerApp()
	found := false
	for _, q := range app.GreenQueries() {
		if q.Name == "InsertCustomer" {
			assert.NotContains(t, q.SQL, "full_name", "green InsertCustomer must not reference old full_name column")
			assert.Contains(t, q.SQL, "display_name", "green InsertCustomer must write display_name")
			assert.Contains(t, q.SQL, "phone", "green InsertCustomer must write phone")
			found = true
		}
	}
	assert.True(t, found, "GreenQueries must include InsertCustomer")
}
