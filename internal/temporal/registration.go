package temporal

import "go.temporal.io/sdk/worker"

// RegisterSession1Worker registers session-1 workflows and activities.
func RegisterSession1Worker(w worker.Worker, acts *Activities) {
	w.RegisterWorkflow(ProvisionPipelineWorkflow)
	w.RegisterActivity(acts.ValidatePipelineSpecActivity)
	w.RegisterActivity(acts.ValidateSchemaPolicyActivity)
	w.RegisterActivity(acts.EnsureSchemaSubjectActivity)
	w.RegisterActivity(acts.PrepareSourceActivity)
	w.RegisterActivity(acts.EnsureStreamActivity)
	w.RegisterActivity(acts.EnsureSinkActivity)
	w.RegisterActivity(acts.StartCaptureActivity)
	w.RegisterActivity(acts.MarkPipelineActiveActivity)
	w.RegisterActivity(acts.StopCaptureActivity)
	w.RegisterActivity(acts.DeleteSinkActivity)
	w.RegisterActivity(acts.DeleteStreamActivity)
	w.RegisterActivity(acts.MarkPipelineErrorActivity)
}

// RegisterSession3Worker registers session-3 workflows and activities.
// It includes all session-2 registrations; session-3 adds no new workflow logic
// (observability is wired outside Temporal).
func RegisterSession3Worker(w worker.Worker, acts *Activities, s2acts *Session2Activities) {
	RegisterSession2Worker(w, acts, s2acts)
}

// RegisterSession2Worker registers session-2 workflows and activities.
// It includes all session-1 registrations plus session-2 CDC operations.
func RegisterSession2Worker(w worker.Worker, acts *Activities, s2acts *Session2Activities) {
	RegisterSession1Worker(w, acts)
	w.RegisterActivity(acts.MarkPipelinePausedActivity)
	w.RegisterActivity(acts.MarkPipelineDecommissionedActivity)

	w.RegisterWorkflow(ProvisionCDCPipelineWorkflow)
	w.RegisterWorkflow(PauseCDCPipelineWorkflow)
	w.RegisterWorkflow(ResumeCDCPipelineWorkflow)
	w.RegisterWorkflow(DecommissionCDCPipelineWorkflow)

	w.RegisterActivity(s2acts.CreateConnectorActivity)
	w.RegisterActivity(s2acts.WaitForConnectorRunningActivity)
	w.RegisterActivity(s2acts.DeleteConnectorActivity)
	w.RegisterActivity(s2acts.PauseConnectorActivity)
	w.RegisterActivity(s2acts.ResumeConnectorActivity)
}

