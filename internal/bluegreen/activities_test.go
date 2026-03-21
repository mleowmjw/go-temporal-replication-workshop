package bluegreen_test

import (
	"errors"
	"testing"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// buildDeps wires up in-memory fake dependencies (no store needed).
func buildDeps(t *testing.T) bluegreen.BGDependencies {
	t.Helper()
	return bluegreen.BGDependencies{
		Migrator: bluegreen.NewFakeDatabaseMigrator(),
		App:      bluegreen.NewCustomerApp(),
	}
}

// activityEnv sets up a Temporal activity test environment.
func activityEnv(t *testing.T) *testsuite.TestActivityEnvironment {
	t.Helper()
	var ts testsuite.WorkflowTestSuite
	return ts.NewTestActivityEnvironment()
}

func TestValidatePlanActivity_Valid(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.ValidatePlanActivity)

	_, err := env.ExecuteActivity(acts.ValidatePlanActivity, validPlan())
	require.NoError(t, err)
}

func TestValidatePlanActivity_Invalid(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.ValidatePlanActivity)

	bad := validPlan()
	bad.ID = ""
	_, err := env.ExecuteActivity(acts.ValidatePlanActivity, bad)
	require.Error(t, err)
}

func TestExecuteExpandActivity(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.ExecuteExpandActivity)

	_, err := env.ExecuteActivity(acts.ExecuteExpandActivity, validPlan())
	require.NoError(t, err)

	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	assert.True(t, fake.HasColumn("display_name"))
	assert.True(t, fake.HasColumn("phone"))
	assert.True(t, fake.HasColumn("full_name"), "expand must NOT drop full_name")
}

func TestExecuteExpandActivity_Failure(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	fake.ExecuteSQLFn = func(_ []string) error {
		return errors.New("DB connection refused")
	}
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.ExecuteExpandActivity)

	_, err := env.ExecuteActivity(acts.ExecuteExpandActivity, validPlan())
	require.Error(t, err)
}

func TestVerifyExpandActivity_Pass(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.VerifyExpandActivity)

	// Apply expand first so verify queries work.
	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	fake.ApplyExpand(validPlan().ExpandSQL)

	val, err := env.ExecuteActivity(acts.VerifyExpandActivity, validPlan())
	require.NoError(t, err)

	var result bluegreen.VerifyExpandResult
	require.NoError(t, val.Get(&result))
	assert.True(t, result.Passed)
}

func TestVerifyExpandActivity_Fail_WrongCount(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.VerifyExpandActivity)

	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	fake.ApplyExpand(validPlan().ExpandSQL)

	// Simulate 3 un-backfilled rows.
	plan := validPlan()
	fake.QueryCounts[plan.VerifyQueries[0].SQL] = 3

	_, err := env.ExecuteActivity(acts.VerifyExpandActivity, plan)
	require.Error(t, err)
}

func TestRunAppCompatCheckActivity_AfterExpand(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.RunAppCompatCheckActivity)

	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	fake.ApplyExpand(validPlan().ExpandSQL)

	val, err := env.ExecuteActivity(acts.RunAppCompatCheckActivity, false)
	require.NoError(t, err)

	var result bluegreen.AppCompatCheckResult
	require.NoError(t, val.Get(&result))
	assert.True(t, result.BluePass, "blue must pass after expand: %v", result.BlueErrors)
	assert.True(t, result.GreenPass, "green must pass after expand: %v", result.GreenErrors)
	assert.Contains(t, result.Summary, "blue=PASS")
	assert.Contains(t, result.Summary, "green=PASS")
}

func TestRunAppCompatCheckActivity_BlueRequired_BreaksOnExpand(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	fake.ExecuteSQLFn = func(stmts []string) error {
		_ = stmts
		return nil
	}
	// Override: mark full_name as gone.
	fake.QueryCheckFn = func(query string) (bluegreen.CheckResult, error) {
		if contains(query, "full_name") {
			return bluegreen.CheckResult{}, errors.New(`column "full_name" not found`)
		}
		return bluegreen.CheckResult{}, nil
	}

	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.RunAppCompatCheckActivity)

	_, err := env.ExecuteActivity(acts.RunAppCompatCheckActivity, true /* bluePassRequired */)
	require.Error(t, err, "should error when blue breaks and bluePassRequired=true")
}

func TestAcquireAndReleaseReadOnly(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.AcquireReadOnlyActivity)
	env.RegisterActivity(acts.ReleaseReadOnlyActivity)
	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)

	_, err := env.ExecuteActivity(acts.AcquireReadOnlyActivity, validPlan())
	require.NoError(t, err)
	assert.True(t, fake.ReadOnly)

	_, err = env.ExecuteActivity(acts.ReleaseReadOnlyActivity, validPlan())
	require.NoError(t, err)
	assert.False(t, fake.ReadOnly)
}

func TestSwitchTrafficActivity(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.SwitchTrafficActivity)

	// SwitchTrafficActivity is now a pure side-effect placeholder; just verify it succeeds.
	_, err := env.ExecuteActivity(acts.SwitchTrafficActivity, "d1")
	require.NoError(t, err)
}

func TestExecuteContractActivity(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.ExecuteContractActivity)

	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	fake.ApplyExpand(validPlan().ExpandSQL)

	_, err := env.ExecuteActivity(acts.ExecuteContractActivity, validPlan())
	require.NoError(t, err)
	assert.False(t, fake.HasColumn("full_name"), "contract must drop full_name")
}

func TestExecuteRollbackActivity(t *testing.T) {
	env := activityEnv(t)
	deps := buildDeps(t)
	acts := bluegreen.NewBGActivities(deps)
	env.RegisterActivity(acts.ExecuteRollbackActivity)

	fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
	fake.ApplyExpand(validPlan().ExpandSQL)

	_, err := env.ExecuteActivity(acts.ExecuteRollbackActivity, validPlan())
	require.NoError(t, err)
	assert.False(t, fake.HasColumn("display_name"), "rollback must drop display_name")
	assert.False(t, fake.HasColumn("phone"), "rollback must drop phone")
}

// contains is a helper to check substring presence.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
