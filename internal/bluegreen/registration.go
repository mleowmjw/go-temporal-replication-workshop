package bluegreen

import "go.temporal.io/sdk/worker"

const (
	// TaskQueue is the Temporal task queue for blue-green deployment workers.
	TaskQueue = "blue-green-deployments"

	// WorkerBuildID identifies this binary version for Temporal worker versioning.
	WorkerBuildID = "blue-green-v1"
)

// RegisterBlueGreenWorker registers the workflow and all activities with the worker.
func RegisterBlueGreenWorker(w worker.Worker, acts *BGActivities) {
	w.RegisterWorkflow(BlueGreenDeploymentWorkflow)

	w.RegisterActivity(acts.ValidatePlanActivity)
	w.RegisterActivity(acts.ExecuteExpandActivity)
	w.RegisterActivity(acts.VerifyExpandActivity)
	w.RegisterActivity(acts.RunAppCompatCheckActivity)
	w.RegisterActivity(acts.AcquireReadOnlyActivity)
	w.RegisterActivity(acts.SwitchTrafficActivity)
	w.RegisterActivity(acts.ReleaseReadOnlyActivity)
	w.RegisterActivity(acts.ExecuteContractActivity)
	w.RegisterActivity(acts.VerifyContractActivity)
	w.RegisterActivity(acts.ExecuteRollbackActivity)
}
