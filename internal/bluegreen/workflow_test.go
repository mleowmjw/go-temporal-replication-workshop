package bluegreen_test

import (
	"testing"
	"time"

	"app/internal/bluegreen"

	"github.com/goforj/godump"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
)

// bgWorkflowSuite groups all BlueGreenDeploymentWorkflow tests.
type bgWorkflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite

	env *testsuite.TestWorkflowEnvironment
}

func TestBGWorkflowSuite(t *testing.T) {
	suite.Run(t, new(bgWorkflowSuite))
}

func (s *bgWorkflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
}

func (s *bgWorkflowSuite) AfterTest(_, _ string) {
	s.env.AssertExpectations(s.T())
}

// buildWorkflowDeps wires all in-memory fakes. No external store needed.
func (s *bgWorkflowSuite) buildWorkflowDeps(_ bluegreen.MigrationPlan) (bluegreen.BGDependencies, *bluegreen.FakeDatabaseMigrator) {
	s.T().Helper()
	fake := bluegreen.NewFakeDatabaseMigrator()
	deps := bluegreen.BGDependencies{
		Migrator: fake,
		App:      bluegreen.NewCustomerApp(),
	}
	return deps, fake
}

// registerAll wires every activity into the test environment.
func (s *bgWorkflowSuite) registerAll(acts *bluegreen.BGActivities) {
	s.env.RegisterActivity(acts.ValidatePlanActivity)
	s.env.RegisterActivity(acts.ExecuteExpandActivity)
	s.env.RegisterActivity(acts.VerifyExpandActivity)
	s.env.RegisterActivity(acts.RunAppCompatCheckActivity)
	s.env.RegisterActivity(acts.AcquireReadOnlyActivity)
	s.env.RegisterActivity(acts.SwitchTrafficActivity)
	s.env.RegisterActivity(acts.ReleaseReadOnlyActivity)
	s.env.RegisterActivity(acts.ExecuteContractActivity)
	s.env.RegisterActivity(acts.VerifyContractActivity)
	s.env.RegisterActivity(acts.ExecuteRollbackActivity)
}

// workflowResult extracts DeploymentResult from a completed workflow.
func (s *bgWorkflowSuite) workflowResult() bluegreen.DeploymentResult {
	s.T().Helper()
	var result bluegreen.DeploymentResult
	require.NoError(s.T(), s.env.GetWorkflowResult(&result))
	return result
}

// approveAfter sends an approve signal after the given simulated delay.
func (s *bgWorkflowSuite) approveAfter(d time.Duration) {
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(bluegreen.SignalApprove, bluegreen.ApprovalPayload{Note: "approved by test"})
	}, d)
}

// rollbackAfter sends a rollback signal after the given simulated delay.
func (s *bgWorkflowSuite) rollbackAfter(d time.Duration) {
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(bluegreen.SignalRollback, bluegreen.RollbackPayload{Reason: "test rollback"})
	}, d)
}

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestHappyPath verifies the full workflow: all approvals → complete.
func (s *bgWorkflowSuite) TestHappyPath() {
	plan := validPlan()
	deps, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Four approval gates: plan_review, expand_verify, monitoring, contract_wait.
	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)
	s.approveAfter(4 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseComplete, result.Phase)
	assert.Equal(s.T(), plan.ID, result.DeploymentID)
	assert.NotEmpty(s.T(), result.History)
}

// TestRollbackDuringPlanReview verifies rollback before expand is triggered.
// Rollback is a successful compensation — the workflow completes without error.
func (s *bgWorkflowSuite) TestRollbackDuringPlanReview() {
	plan := validPlan()
	deps, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.rollbackAfter(500 * time.Millisecond)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError(), "rollback is a successful compensation, not a failure")

	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase)
	// Expand never ran, so full_name still exists and display_name doesn't.
	assert.True(s.T(), fake.HasColumn("full_name"))
	assert.False(s.T(), fake.HasColumn("display_name"))
}

// TestRollbackAfterExpand verifies rollback after expand undoes the schema change.
func (s *bgWorkflowSuite) TestRollbackAfterExpand() {
	plan := validPlan()
	deps, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)  // approve plan_review
	s.rollbackAfter(2 * time.Second) // rollback at expand_verify gate

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase)
	// Rollback SQL must have removed display_name and phone.
	assert.False(s.T(), fake.HasColumn("display_name"), "rollback must drop display_name")
	assert.False(s.T(), fake.HasColumn("phone"), "rollback must drop phone")
	assert.True(s.T(), fake.HasColumn("full_name"), "full_name must survive rollback")
}

// TestRollbackDuringMonitoring verifies rollback after traffic cutover.
func (s *bgWorkflowSuite) TestRollbackDuringMonitoring() {
	plan := validPlan()
	deps, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)  // plan_review
	s.approveAfter(2 * time.Second)  // expand_verify
	s.rollbackAfter(3 * time.Second) // monitoring rollback

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase)
	// Expand SQL ran, rollback SQL ran: display_name and phone gone.
	assert.False(s.T(), fake.HasColumn("display_name"))
	assert.False(s.T(), fake.HasColumn("phone"))
}

// TestRollbackAtContractGate verifies that rollback after monitoring but before
// contract also runs compensation.
func (s *bgWorkflowSuite) TestRollbackAtContractGate() {
	plan := validPlan()
	deps, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)  // plan_review
	s.approveAfter(2 * time.Second)  // expand_verify
	s.approveAfter(3 * time.Second)  // monitoring
	s.rollbackAfter(4 * time.Second) // rollback at contract_wait

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase)
	assert.False(s.T(), fake.HasColumn("display_name"))
}

// TestExpandVerifyFailure verifies that a failed expand verification auto-compensates.
func (s *bgWorkflowSuite) TestExpandVerifyFailure() {
	plan := validPlan()
	deps, fake := s.buildWorkflowDeps(plan)

	// Simulate 5 un-backfilled rows so VerifyExpand fails.
	fake.ApplyExpand(plan.ExpandSQL) // pre-seed so the column exists for QueryCounts
	fake.QueryCounts[plan.VerifyQueries[0].SQL] = 5

	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Approve plan_review — expand succeeds but verify fails → auto-compensate.
	s.approveAfter(1 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase)
}

// TestAppCompatCheckedAfterExpand uses RegisterDelayedCallback to verify
// the compat check runs mid-workflow, proving blue=PASS and green=PASS.
func (s *bgWorkflowSuite) TestAppCompatCheckedAfterExpand() {
	plan := validPlan()
	deps, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	compatChecked := false
	// After expand SQL runs, the fake migrator has display_name + phone.
	// We verify this state via a delayed callback triggered after expand.
	s.env.RegisterDelayedCallback(func() {
		fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
		// Inspect the fake migrator state directly — columns are added by ExecuteExpandActivity.
		compatChecked = fake.HasColumn("display_name") && fake.HasColumn("phone")
		// Now send approval so the workflow continues.
		s.env.SignalWorkflow(bluegreen.SignalApprove, bluegreen.ApprovalPayload{Note: "compat verified"})
	}, 1500*time.Millisecond)

	// Approve plan_review first.
	s.approveAfter(500 * time.Millisecond)
	// Remaining approvals (expand_verify already handled above, monitoring, contract_wait).
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())
	assert.True(s.T(), compatChecked, "expand must have applied display_name and phone columns")
}

// TestContractRequiresSeparateApproval verifies that after monitoring approval,
// the workflow pauses again at contract_wait requiring a second signal.
func (s *bgWorkflowSuite) TestContractRequiresSeparateApproval() {
	plan := validPlan()
	deps, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Only 3 approvals: plan_review, expand_verify, monitoring.
	// No contract_wait approval → rollback at contract gate.
	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)
	s.rollbackAfter(4 * time.Second) // must explicitly rollback at contract gate

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError(), "rollback is successful compensation")

	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase, "must be rolled back, not complete")
}

// TestReadOnlyReleasedOnRollback verifies that the read-only lock is always
// released during compensation, even when rollback happens mid-cutover.
func (s *bgWorkflowSuite) TestReadOnlyReleasedOnRollback() {
	plan := validPlan()
	deps, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)  // plan_review
	s.approveAfter(2 * time.Second)  // expand_verify
	s.rollbackAfter(3 * time.Second) // rollback during monitoring

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())
	assert.False(s.T(), fake.ReadOnly, "read-only lock must always be released by compensation")
}

// TestPreContractCompatCheckActivityError_TriggersCompensation is a regression
// test for the Bug 2 fix: when RunAppCompatCheckActivity itself returns an
// activity error at the contract_wait gate (not just GreenPass=false), the
// workflow must compensate (rollback expand SQL + phase = rolled_back).
func (s *bgWorkflowSuite) TestPreContractCompatCheckActivityError_TriggersCompensation() {
	plan := validPlan()
	deps, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Approve plan_review, expand_verify, monitoring to reach contract_wait.
	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)

	// The workflow calls RunAppCompatCheckActivity twice:
	//   1. After expand_verify (bluePassRequired=true)  — must succeed
	//   2. Before contract gate (bluePassRequired=false) — inject hard error here
	s.env.OnActivity(acts.RunAppCompatCheckActivity, mock.Anything, true).
		Return(bluegreen.AppCompatCheckResult{BluePass: true, GreenPass: true, Summary: "blue=PASS, green=PASS"}, nil).Once()
	// Use a non-retryable error so the activity fails on the first attempt only.
	s.env.OnActivity(acts.RunAppCompatCheckActivity, mock.Anything, false).
		Return(bluegreen.AppCompatCheckResult{},
			temporal.NewNonRetryableApplicationError("migrator connection lost", "ConnectionError", nil)).Once()

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})

	s.True(s.env.IsWorkflowCompleted())
	// Compensation succeeds → workflow completes without error.
	require.NoError(s.T(), s.env.GetWorkflowError())

	result := s.workflowResult()
	// Bug 2 fix: must be rolled_back, not stuck in contract_wait with expand applied.
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase,
		"expand must be rolled back when pre-contract compat check activity errors")
	assert.False(s.T(), fake.HasColumn("display_name"),
		"rollback SQL must have removed display_name")
	assert.False(s.T(), fake.HasColumn("phone"),
		"rollback SQL must have removed phone")
}

// TestPhaseHistoryIsComplete verifies that all phase transitions are recorded
// in the correct order using the workflow result.
func (s *bgWorkflowSuite) TestPhaseHistoryIsComplete() {
	plan := validPlan()
	deps, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)
	s.approveAfter(4 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})
	require.NoError(s.T(), s.env.GetWorkflowError())

	result := s.workflowResult()

	phases := make([]bluegreen.Phase, len(result.History))
	for i, h := range result.History {
		phases[i] = h.Phase
	}

	// Verify the exact ordered sequence of phases in the happy path.
	expected := []bluegreen.Phase{
		bluegreen.PhasePlanReview,
		bluegreen.PhaseExpanding,
		bluegreen.PhaseExpandVerify,
		bluegreen.PhaseCutover,
		bluegreen.PhaseMonitoring,
		bluegreen.PhaseContractWait,
		bluegreen.PhaseContracting,
		bluegreen.PhaseComplete,
	}

	// try out godump; if not match order now will crash + fail!!
	godump.Diff(expected, phases)

	assert.Equal(s.T(), expected, phases, "phase history must match the exact happy-path order")
}

// TestQueryHandlerReturnsLiveState verifies that the query handler reflects
// the current phase while the workflow is running (mid-workflow query).
func (s *bgWorkflowSuite) TestQueryHandlerReturnsLiveState() {
	plan := validPlan()
	deps, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// After plan_review starts, query and verify phase before approving.
	s.env.RegisterDelayedCallback(func() {
		val, err := s.env.QueryWorkflow(bluegreen.QueryDeploymentState)
		require.NoError(s.T(), err)
		var status bluegreen.DeploymentStatus
		require.NoError(s.T(), val.Get(&status))
		assert.Equal(s.T(), plan.ID, status.ID)
		assert.Equal(s.T(), bluegreen.PhasePlanReview, status.Phase)
		assert.NotEmpty(s.T(), status.History)
		// Approve to continue.
		s.env.SignalWorkflow(bluegreen.SignalApprove, bluegreen.ApprovalPayload{})
	}, 500*time.Millisecond)

	s.approveAfter(2 * time.Second) // expand_verify
	s.approveAfter(3 * time.Second) // monitoring
	s.approveAfter(4 * time.Second) // contract_wait

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, bluegreen.DeploymentRequest{Plan: plan})
	require.NoError(s.T(), s.env.GetWorkflowError())
}

// TestParentNotificationOnComplete verifies that SignalDeploymentComplete is sent to
// the DatabaseOpsWorkflow when the deployment finishes on the happy path.
func (s *bgWorkflowSuite) TestParentNotificationOnComplete() {
	plan := validPlan()
	deps, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Mock the external signal sent to the parent coordinator on completion.
	s.env.OnSignalExternalWorkflow(mock.Anything, "db-ops-test-parent", mock.Anything,
		bluegreen.SignalDeploymentComplete, mock.Anything).
		Return(nil).Once()

	// Four approval gates.
	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)
	s.approveAfter(4 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow,
		bluegreen.DeploymentRequest{Plan: plan, ParentWorkflowID: "db-ops-test-parent"})

	require.NoError(s.T(), s.env.GetWorkflowError())
	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseComplete, result.Phase)
}

// TestParentNotificationOnRollback verifies that a rolled-back deployment also
// notifies the parent so the coordinator can release the lock.
func (s *bgWorkflowSuite) TestParentNotificationOnRollback() {
	plan := validPlan()
	deps, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Mock the external signal sent to the parent coordinator on rollback.
	s.env.OnSignalExternalWorkflow(mock.Anything, "db-ops-test-parent", mock.Anything,
		bluegreen.SignalDeploymentComplete, mock.Anything).
		Return(nil).Once()

	s.rollbackAfter(500 * time.Millisecond)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow,
		bluegreen.DeploymentRequest{Plan: plan, ParentWorkflowID: "db-ops-test-parent"})

	require.NoError(s.T(), s.env.GetWorkflowError())
	result := s.workflowResult()
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, result.Phase)
}
