package bluegreen_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"app/internal/bluegreen"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/goforj/godump"
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

// buildWorkflowDeps wires all in-memory fakes and seeds the deployment in the store.
func (s *bgWorkflowSuite) buildWorkflowDeps(plan bluegreen.MigrationPlan) (bluegreen.BGDependencies, *bluegreen.InMemoryDeploymentStore, *bluegreen.FakeDatabaseMigrator) {
	s.T().Helper()
	store := bluegreen.NewInMemoryDeploymentStore()
	fake := bluegreen.NewFakeDatabaseMigrator()
	deps := bluegreen.BGDependencies{
		Store:    store,
		Migrator: fake,
		App:      bluegreen.NewCustomerApp(),
	}
	require.NoError(s.T(), store.Create(context.Background(), bluegreen.Deployment{
		ID:   plan.ID,
		Plan: plan,
	}))
	return deps, store, fake
}

// registerAll wires every activity into the test environment.
func (s *bgWorkflowSuite) registerAll(acts *bluegreen.BGActivities) {
	s.env.RegisterActivity(acts.ValidatePlanActivity)
	s.env.RegisterActivity(acts.UpdatePhaseActivity)
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
// App compat is verified at each gate (blue=PASS, green=PASS after expand).
func (s *bgWorkflowSuite) TestHappyPath() {
	plan := validPlan()
	deps, store, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Four approval gates: plan_review, expand_verify, monitoring, contract_wait.
	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)
	s.approveAfter(4 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())

	var result bluegreen.DeploymentResult
	require.NoError(s.T(), s.env.GetWorkflowResult(&result))
	assert.Equal(s.T(), bluegreen.PhaseComplete, result.Phase)

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), bluegreen.PhaseComplete, got.Phase)
}

// TestRollbackDuringPlanReview verifies rollback before expand is triggered.
func (s *bgWorkflowSuite) TestRollbackDuringPlanReview() {
	plan := validPlan()
	deps, store, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.rollbackAfter(500 * time.Millisecond)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError(), "rolled-back workflow should return error")

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, got.Phase)
	// Expand never ran, so full_name still exists and display_name doesn't.
	assert.True(s.T(), fake.HasColumn("full_name"))
	assert.False(s.T(), fake.HasColumn("display_name"))
}

// TestRollbackAfterExpand verifies rollback after expand undoes the schema change.
func (s *bgWorkflowSuite) TestRollbackAfterExpand() {
	plan := validPlan()
	deps, store, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Approve plan_review, then rollback during expand_verify gate.
	s.approveAfter(1 * time.Second)  // approve plan_review
	s.rollbackAfter(2 * time.Second) // rollback at expand_verify gate

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError())

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, got.Phase)

	// Rollback SQL must have removed display_name and phone.
	assert.False(s.T(), fake.HasColumn("display_name"), "rollback must drop display_name")
	assert.False(s.T(), fake.HasColumn("phone"), "rollback must drop phone")
	assert.True(s.T(), fake.HasColumn("full_name"), "full_name must survive rollback")
}

// TestRollbackDuringMonitoring verifies rollback after traffic cutover.
func (s *bgWorkflowSuite) TestRollbackDuringMonitoring() {
	plan := validPlan()
	deps, store, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Approve plan_review, expand_verify, then rollback during monitoring.
	s.approveAfter(1 * time.Second)  // plan_review
	s.approveAfter(2 * time.Second)  // expand_verify
	s.rollbackAfter(3 * time.Second) // monitoring rollback

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError())

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, got.Phase)

	// Expand SQL ran, rollback SQL ran: display_name and phone gone.
	assert.False(s.T(), fake.HasColumn("display_name"))
	assert.False(s.T(), fake.HasColumn("phone"))
}

// TestRollbackAtContractGate verifies that rollback after monitoring but before
// contract also runs compensation.
func (s *bgWorkflowSuite) TestRollbackAtContractGate() {
	plan := validPlan()
	deps, store, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)  // plan_review
	s.approveAfter(2 * time.Second)  // expand_verify
	s.approveAfter(3 * time.Second)  // monitoring
	s.rollbackAfter(4 * time.Second) // rollback at contract_wait

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError())

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, got.Phase)
	assert.False(s.T(), fake.HasColumn("display_name"))
}

// TestExpandVerifyFailure verifies that a failed expand verification auto-compensates.
func (s *bgWorkflowSuite) TestExpandVerifyFailure() {
	plan := validPlan()
	deps, store, fake := s.buildWorkflowDeps(plan)

	// Simulate 5 un-backfilled rows so VerifyExpand fails.
	fake.ApplyExpand(plan.ExpandSQL) // pre-seed so the column exists for QueryCounts
	fake.QueryCounts[plan.VerifyQueries[0].SQL] = 5

	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Approve plan_review — expand succeeds but verify fails → auto-compensate.
	s.approveAfter(1 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError())

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, got.Phase)
}

// TestAppCompatCheckedAfterExpand uses RegisterDelayedCallback to verify
// the compat check runs mid-workflow, proving blue=PASS and green=PASS.
func (s *bgWorkflowSuite) TestAppCompatCheckedAfterExpand() {
	plan := validPlan()
	deps, _, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	compatChecked := false
	// After expand SQL runs, the fake migrator has display_name + phone.
	// We verify this state via a delayed callback triggered after expand.
	s.env.RegisterDelayedCallback(func() {
		fake := deps.Migrator.(*bluegreen.FakeDatabaseMigrator)
		ctx := context.Background()
		app := bluegreen.NewCustomerApp()
		result := bluegreen.RunAppCompat(ctx, app, fake)
		compatChecked = result.BluePass && result.GreenPass
		// Now send approval so the workflow continues.
		s.env.SignalWorkflow(bluegreen.SignalApprove, bluegreen.ApprovalPayload{Note: "compat verified"})
	}, 1500*time.Millisecond)

	// Approve plan_review first.
	s.approveAfter(500 * time.Millisecond)
	// Remaining approvals (expand_verify already handled above, monitoring, contract_wait).
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	require.NoError(s.T(), s.env.GetWorkflowError())
	assert.True(s.T(), compatChecked, "app compat check (blue=PASS, green=PASS) must have run during workflow")
}

// TestContractRequiresSeparateApproval verifies that after monitoring approval,
// the workflow pauses again at contract_wait requiring a second signal.
func (s *bgWorkflowSuite) TestContractRequiresSeparateApproval() {
	plan := validPlan()
	deps, store, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	// Only 3 approvals: plan_review, expand_verify, monitoring.
	// No contract_wait approval → rollback at contract gate.
	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)
	s.rollbackAfter(4 * time.Second) // must explicitly rollback at contract gate

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError(), "missing contract approval → rollback")

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, got.Phase, "must be rolled back, not complete")
}

// TestReadOnlyReleasedOnRollback verifies that the read-only lock is always
// released during compensation, even when rollback happens mid-cutover.
func (s *bgWorkflowSuite) TestReadOnlyReleasedOnRollback() {
	plan := validPlan()
	deps, _, fake := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)  // plan_review
	s.approveAfter(2 * time.Second)  // expand_verify
	s.rollbackAfter(3 * time.Second) // rollback during monitoring

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	assert.Error(s.T(), s.env.GetWorkflowError())
	assert.False(s.T(), fake.ReadOnly, "read-only lock must always be released by compensation")
}

// TestPreContractCompatCheckActivityError_TriggersCompensation is a regression
// test for the Bug 2 fix: when RunAppCompatCheckActivity itself returns an
// activity error at the contract_wait gate (not just GreenPass=false), the
// workflow must compensate (rollback expand SQL + mark rolled_back) rather than
// returning an error that leaves the database in an expanded-but-stuck state.
func (s *bgWorkflowSuite) TestPreContractCompatCheckActivityError_TriggersCompensation() {
	plan := validPlan()
	deps, store, fake := s.buildWorkflowDeps(plan)
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
	// We mock both calls explicitly; call 2 simulates a worker crash / DB unavailable.
	s.env.OnActivity(acts.RunAppCompatCheckActivity, mock.Anything, true).
		Return(bluegreen.AppCompatCheckResult{BluePass: true, GreenPass: true, Summary: "blue=PASS, green=PASS"}, nil).Once()
	// Use a non-retryable error so the activity fails on the first attempt only.
	// A plain errors.New would be retried (MaximumAttempts:3), exhausting the
	// .Once() mock on attempt 2 and causing a "called over 1 times" panic.
	s.env.OnActivity(acts.RunAppCompatCheckActivity, mock.Anything, false).
		Return(bluegreen.AppCompatCheckResult{},
			temporal.NewNonRetryableApplicationError("migrator connection lost", "ConnectionError", nil)).Once()

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)

	s.True(s.env.IsWorkflowCompleted())
	require.Error(s.T(), s.env.GetWorkflowError(), "workflow must fail when compat check errors")
	assert.Contains(s.T(), s.env.GetWorkflowError().Error(), "pre-contract compat check error")

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)
	// Bug 2 fix: must be rolled_back, not stuck in contract_wait with expand applied.
	assert.Equal(s.T(), bluegreen.PhaseRolledBack, got.Phase,
		"expand must be rolled back when pre-contract compat check activity errors")
	assert.False(s.T(), fake.HasColumn("display_name"),
		"rollback SQL must have removed display_name")
	assert.False(s.T(), fake.HasColumn("phone"),
		"rollback SQL must have removed phone")
}

// TestPhaseHistoryIsComplete verifies that all phase transitions are recorded.
func (s *bgWorkflowSuite) TestPhaseHistoryIsComplete() {
	plan := validPlan()
	deps, store, _ := s.buildWorkflowDeps(plan)
	acts := bluegreen.NewBGActivities(deps)
	s.registerAll(acts)
	s.env.RegisterWorkflow(bluegreen.BlueGreenDeploymentWorkflow)

	s.approveAfter(1 * time.Second)
	s.approveAfter(2 * time.Second)
	s.approveAfter(3 * time.Second)
	s.approveAfter(4 * time.Second)

	s.env.ExecuteWorkflow(bluegreen.BlueGreenDeploymentWorkflow, plan)
	require.NoError(s.T(), s.env.GetWorkflowError())

	got, err := store.Get(context.Background(), plan.ID)
	require.NoError(s.T(), err)

	phases := make([]bluegreen.Phase, len(got.History))
	for i, h := range got.History {
		phases[i] = h.Phase
	}

	godump.Dump(phases)
	fmt.Println("<<<<<< spew BELOW >>>>>>>")
	spew.Dump(phases)

	// Verify key phases appear in the history.
	// Below not correct; does not catch wrong orering .. <<TODO>>
	assert.Contains(s.T(), phases, bluegreen.PhasePlanReview)
	assert.Contains(s.T(), phases, bluegreen.PhaseExpanding)
	assert.Contains(s.T(), phases, bluegreen.PhaseExpandVerify)
	assert.Contains(s.T(), phases, bluegreen.PhaseCutover)
	assert.Contains(s.T(), phases, bluegreen.PhaseMonitoring)
	assert.Contains(s.T(), phases, bluegreen.PhaseContractWait)
	assert.Contains(s.T(), phases, bluegreen.PhaseContracting)
	assert.Contains(s.T(), phases, bluegreen.PhaseComplete)
}
