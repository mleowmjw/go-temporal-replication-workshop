package bluegreen_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"app/internal/bluegreen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// dbOpsWorkflowSuite groups all DatabaseOpsWorkflow tests.
type dbOpsWorkflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite

	env *testsuite.TestWorkflowEnvironment
}

func TestDBOpsWorkflowSuite(t *testing.T) {
	suite.Run(t, new(dbOpsWorkflowSuite))
}

func (s *dbOpsWorkflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
	s.env.RegisterWorkflow(bluegreen.DatabaseOpsWorkflow)
}

func (s *dbOpsWorkflowSuite) AfterTest(_, _ string) {
	s.env.AssertExpectations(s.T())
}

// devConfig returns a DatabaseOpsConfig for dev environment (5-minute lock).
func devConfig() bluegreen.DatabaseOpsConfig {
	return bluegreen.DatabaseOpsConfig{
		DatabaseID:  "localhost-5435-appdb",
		Environment: bluegreen.EnvDev,
	}
}

// lockReqSeq generates unique update IDs to avoid the test env's deduplication
// logic treating retried update calls as duplicates of previously-rejected ones.
var lockReqSeq int

// sendLock posts an UpdateRequestDeployment update and captures the response/error
// in the provided pointers. The result is available on the NEXT event loop
// iteration (i.e., in a subsequent RegisterDelayedCallback), not immediately.
func (s *dbOpsWorkflowSuite) sendLock(planID, workflowID string, outGranted *bool, outErr *error) {
	lockReqSeq++
	updateID := fmt.Sprintf("lock-req-%d", lockReqSeq)
	uc := &testsuite.TestUpdateCallback{
		OnReject: func(err error) {
			if outErr != nil {
				*outErr = err
			}
		},
		OnAccept: func() {},
		OnComplete: func(v any, err error) {
			if err != nil {
				if outErr != nil {
					*outErr = err
				}
				return
			}
			if r, ok := v.(bluegreen.DeploymentLockResponse); ok && r.Granted {
				if outGranted != nil {
					*outGranted = true
				}
			}
		},
	}
	s.env.UpdateWorkflow(bluegreen.UpdateRequestDeployment, updateID, uc,
		bluegreen.DeploymentLockRequest{PlanID: planID, WorkflowID: workflowID})
}

// signalComplete sends a SignalDeploymentComplete signal.
func (s *dbOpsWorkflowSuite) signalComplete(planID, workflowID string, phase bluegreen.Phase) {
	s.env.SignalWorkflow(bluegreen.SignalDeploymentComplete, bluegreen.DeploymentCompletePayload{
		PlanID:     planID,
		WorkflowID: workflowID,
		Phase:      phase,
	})
}

// queryState reads the coordinator state.
func (s *dbOpsWorkflowSuite) queryState() bluegreen.DatabaseOpsState {
	val, err := s.env.QueryWorkflow(bluegreen.QueryDatabaseOpsState)
	require.NoError(s.T(), err)
	var state bluegreen.DatabaseOpsState
	require.NoError(s.T(), val.Get(&state))
	return state
}

// ─── Tests ───────────────────────────────────────────────────────────────────

// TestHappyPath verifies: lock acquired → deployment runs → lock released on completion.
//
// Important: UpdateWorkflow posts the update asynchronously. The Update handler
// (and therefore the state mutation) runs on the NEXT event loop step after the
// RegisterDelayedCallback returns. We therefore split observations across two
// consecutive callbacks at different simulated times.
func (s *dbOpsWorkflowSuite) TestHappyPath() {
	cfg := devConfig()

	var lockGranted bool

	// 100ms: verify initial state is clean, then request the lock.
	s.env.RegisterDelayedCallback(func() {
		state := s.queryState()
		assert.Nil(s.T(), state.ActiveDeployment)
		assert.Empty(s.T(), state.CompletedOps)

		s.sendLock("plan-1", "bg-deploy-plan-1", &lockGranted, nil)
	}, 100*time.Millisecond)

	// 200ms: Update handler has run by now — verify lock is held.
	s.env.RegisterDelayedCallback(func() {
		assert.True(s.T(), lockGranted, "lock must have been granted by now")
		state := s.queryState()
		require.NotNil(s.T(), state.ActiveDeployment)
		assert.Equal(s.T(), "plan-1", state.ActiveDeployment.PlanID)

		// Deployment completes successfully.
		s.signalComplete("plan-1", "bg-deploy-plan-1", bluegreen.PhaseComplete)
	}, 200*time.Millisecond)

	// 300ms: lock should be released.
	s.env.RegisterDelayedCallback(func() {
		state := s.queryState()
		assert.Nil(s.T(), state.ActiveDeployment, "lock must be released after completion")
		require.Len(s.T(), state.CompletedOps, 1)
		assert.Equal(s.T(), "plan-1", state.CompletedOps[0].PlanID)
		assert.Equal(s.T(), bluegreen.PhaseComplete, state.CompletedOps[0].FinalPhase)
	}, 300*time.Millisecond)

	// Let the 24h timer fire to cleanly continue-as-new and end the test.
	s.env.ExecuteWorkflow(bluegreen.DatabaseOpsWorkflow, cfg)

	s.True(s.env.IsWorkflowCompleted())
	err := s.env.GetWorkflowError()
	var continueErr *workflow.ContinueAsNewError
	require.True(s.T(), errors.As(err, &continueErr), "expected ContinueAsNewError, got: %v", err)
}

// TestLockRejection verifies that a second deployment is refused while the first holds the lock,
// and that the lock becomes available again once the first deployment completes.
func (s *dbOpsWorkflowSuite) TestLockRejection() {
	cfg := devConfig()

	var firstGranted bool
	var secondErr error

	// 100ms: acquire the lock for the first deployment.
	s.env.RegisterDelayedCallback(func() {
		s.sendLock("plan-a", "bg-deploy-plan-a", &firstGranted, nil)
	}, 100*time.Millisecond)

	// 200ms: first lock is active; second request must be rejected.
	s.env.RegisterDelayedCallback(func() {
		require.True(s.T(), firstGranted, "first lock must be granted")

		state := s.queryState()
		require.NotNil(s.T(), state.ActiveDeployment)

		s.sendLock("plan-b", "bg-deploy-plan-b", nil, &secondErr)
	}, 200*time.Millisecond)

	// 300ms: verify rejection was received.
	s.env.RegisterDelayedCallback(func() {
		require.Error(s.T(), secondErr, "second deployment must be rejected while lock is held")
		assert.Contains(s.T(), secondErr.Error(), "plan-a", "error must name the holder")

		// Release the first lock.
		s.signalComplete("plan-a", "bg-deploy-plan-a", bluegreen.PhaseComplete)
	}, 300*time.Millisecond)

	// 400ms: lock is free — third request for plan-b should succeed.
	var thirdGranted bool
	var thirdErr error
	s.env.RegisterDelayedCallback(func() {
		state := s.queryState()
		require.Nil(s.T(), state.ActiveDeployment, "lock must be released before requesting plan-b")

		s.sendLock("plan-b", "bg-deploy-plan-b", &thirdGranted, &thirdErr)
	}, 400*time.Millisecond)

	// 500ms: verify third grant and clean up.
	s.env.RegisterDelayedCallback(func() {
		require.NoError(s.T(), thirdErr, "plan-b lock request must not error")
		require.True(s.T(), thirdGranted, "plan-b must succeed once lock is released")
		s.signalComplete("plan-b", "bg-deploy-plan-b", bluegreen.PhaseRolledBack)
	}, 500*time.Millisecond)

	s.env.ExecuteWorkflow(bluegreen.DatabaseOpsWorkflow, cfg)

	err := s.env.GetWorkflowError()
	var continueErr *workflow.ContinueAsNewError
	require.True(s.T(), errors.As(err, &continueErr))
}

// TestLockTimeoutSendsRollbackSignal verifies that when the schema lock timer fires
// the coordinator sends a rollback signal to the deployment workflow.
//
// The test verifies the coordinator's behaviour up to and including the timeout:
//   - Lock is granted
//   - After 5 minutes (dev timeout), coordinator sends a rollback signal
//   - After the deployment signals completion, the lock is released
//
// Because SignalExternalWorkflow runs in a goroutine inside the test env,
// we let the completion happen organically via a RegisterDelayedCallback that
// fires AFTER the mock goroutine has had time to complete and post its callbacks.
func (s *dbOpsWorkflowSuite) TestLockTimeoutSendsRollbackSignal() {
	cfg := bluegreen.DatabaseOpsConfig{
		DatabaseID:  "localhost-5435-appdb",
		Environment: bluegreen.EnvDev, // 1-minute lock timeout
	}

	rollbackSignalled := false

	// Mock the rollback signal. Capture that it was called.
	s.env.OnSignalExternalWorkflow(
		mock.Anything, "bg-deploy-timeout-plan", mock.Anything,
		bluegreen.SignalRollback, mock.Anything,
	).Return(nil).Run(func(args mock.Arguments) {
		rollbackSignalled = true
	}).Once()

	// Acquire lock.
	var lockGranted bool
	s.env.RegisterDelayedCallback(func() {
		s.sendLock("timeout-plan", "bg-deploy-timeout-plan", &lockGranted, nil)
	}, 100*time.Millisecond)

	// Verify lock granted.
	s.env.RegisterDelayedCallback(func() {
		require.True(s.T(), lockGranted, "lock must be granted")
	}, 200*time.Millisecond)

	// After the lock timer fires (1 minute) and the mock goroutine completes,
	// send the deployment_complete signal. We schedule this AFTER the timeout so
	// the mock goroutine has had time to run and post its callbacks.
	s.env.RegisterDelayedCallback(func() {
		// By this point the rollback signal must have been sent.
		assert.True(s.T(), rollbackSignalled, "rollback signal must be sent on timeout")
		// Simulate the deployment acknowledging the rollback.
		s.signalComplete("timeout-plan", "bg-deploy-timeout-plan", bluegreen.PhaseRolledBack)
	}, cfg.Environment.LockTimeout()+time.Minute)

	// Verify the lock was released after completion.
	s.env.RegisterDelayedCallback(func() {
		state := s.queryState()
		assert.Nil(s.T(), state.ActiveDeployment, "lock must be released after rollback completion")
		if len(state.CompletedOps) > 0 {
			assert.Equal(s.T(), bluegreen.PhaseRolledBack, state.CompletedOps[0].FinalPhase)
		}
	}, cfg.Environment.LockTimeout()+2*time.Minute)

	s.env.ExecuteWorkflow(bluegreen.DatabaseOpsWorkflow, cfg)

	err := s.env.GetWorkflowError()
	var continueErr *workflow.ContinueAsNewError
	require.True(s.T(), errors.As(err, &continueErr), "expected ContinueAsNewError after 24h, got: %v", err)
}

// TestContinueAsNewAfter24h verifies that the coordinator continues-as-new when
// 24 hours elapse and no deployment is active.
func (s *dbOpsWorkflowSuite) TestContinueAsNewAfter24h() {
	cfg := devConfig()

	// No deployments; just let the 24h timer fire naturally.
	s.env.ExecuteWorkflow(bluegreen.DatabaseOpsWorkflow, cfg)

	s.True(s.env.IsWorkflowCompleted())
	err := s.env.GetWorkflowError()
	var continueErr *workflow.ContinueAsNewError
	require.True(s.T(), errors.As(err, &continueErr), "workflow must continue-as-new after 24h, got: %v", err)
}

// TestContinueAsNewDeferredWhileLocked verifies that continue-as-new is deferred
// until the active deployment completes, even if the 24h timer fires first.
func (s *dbOpsWorkflowSuite) TestContinueAsNewDeferredWhileLocked() {
	cfg := devConfig() // dev: 5-minute lock timeout

	// The deployment is acquired 1 minute before the 24h timer.
	// The dev lock timeout is 5 minutes, so the lock timer would fire at 24h+4m.
	// We complete the deployment at 24h+1m (before the lock timer fires) to keep
	// the test clean. However, we also mock the signal in case timing skews.
	s.env.OnSignalExternalWorkflow(mock.Anything, "bg-deploy-slow-plan", mock.Anything,
		bluegreen.SignalRollback, mock.Anything).Return(nil).Maybe()

	var lockGranted bool

	// Acquire lock 1 minute before the 24h timer fires.
	s.env.RegisterDelayedCallback(func() {
		s.sendLock("slow-plan", "bg-deploy-slow-plan", &lockGranted, nil)
	}, bluegreen.ContinueAsNewInterval-time.Minute)

	// Complete the deployment 1 minute after 24h (well before the 5-min lock timer).
	// The workflow's continue-as-new check fires when:
	//   continueAsNewRequested = true (set at 24h) AND activeDeployment = nil (set here).
	s.env.RegisterDelayedCallback(func() {
		require.True(s.T(), lockGranted, "lock must have been granted")

		state := s.queryState()
		require.NotNil(s.T(), state.ActiveDeployment,
			"deployment must still be active when 24h timer fires")

		// Release the lock — continue-as-new should trigger on next loop iteration.
		s.signalComplete("slow-plan", "bg-deploy-slow-plan", bluegreen.PhaseComplete)
	}, bluegreen.ContinueAsNewInterval+time.Minute)

	s.env.ExecuteWorkflow(bluegreen.DatabaseOpsWorkflow, cfg)

	s.True(s.env.IsWorkflowCompleted())
	err := s.env.GetWorkflowError()
	var continueErr *workflow.ContinueAsNewError
	require.True(s.T(), errors.As(err, &continueErr),
		"workflow must continue-as-new after deferred deployment completes, got: %v", err)
}
