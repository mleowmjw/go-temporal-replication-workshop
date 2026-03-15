package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"app/internal/api"
	"app/internal/connectors"
	"app/internal/domain"
	"app/internal/store"
	apptemporal "app/internal/temporal"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	temporalAddr := envOr("TEMPORAL_ADDRESS", "localhost:7233")
	listenAddr := envOr("HTTP_ADDR", ":8081")
	kafkaConnectURL := envOr("KAFKA_CONNECT_URL", "http://localhost:8083")

	// --- In-memory backing stores ---
	ms := store.NewInMemoryMetadataStore()
	cs := store.NewInMemoryConnectorStore()
	secrets := connectors.NewInMemorySecretProvider()
	schemaReg := connectors.NewInMemorySchemaRegistry()
	cdc := connectors.NewFakeCDCProvisioner()
	streamProv := connectors.NewFakeStreamProvisioner()
	sinkProv := connectors.NewFakeSinkProvisioner()

	// --- Kafka Connect client (HTTP) ---
	kcClient := connectors.NewHTTPKafkaConnectClient(kafkaConnectURL)

	// --- Temporal client ---
	tc, err := client.Dial(client.Options{
		HostPort: temporalAddr,
		Logger:   newTemporalLogger(log),
	})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer tc.Close()

	// --- Temporal worker: session-2 ---
	baseDeps := apptemporal.Dependencies{
		Store:          ms,
		Secrets:        secrets,
		SchemaRegistry: schemaReg,
		CDC:            cdc,
		Stream:         streamProv,
		Sink:           sinkProv,
	}
	s2deps := apptemporal.Session2Dependencies{
		Dependencies:   baseDeps,
		KafkaConnect:   kcClient,
		ConnectorStore: cs,
	}

	acts := apptemporal.NewActivities(baseDeps)
	s2acts := apptemporal.NewSession2Activities(s2deps)

	w := worker.New(tc, apptemporal.DefaultTaskQueue, worker.Options{
		BuildID:                 apptemporal.WorkerBuildIDV2,
		UseBuildIDForVersioning: true,
	})

	// Register session-1 activities (reused).
	w.RegisterWorkflow(apptemporal.ProvisionPipelineWorkflow)
	w.RegisterActivity(acts.ValidatePipelineSpecActivity)
	w.RegisterActivity(acts.ValidateSchemaPolicyActivity)
	w.RegisterActivity(acts.EnsureSchemaSubjectActivity)
	w.RegisterActivity(acts.PrepareSourceActivity)
	w.RegisterActivity(acts.EnsureStreamActivity)
	w.RegisterActivity(acts.EnsureSinkActivity)
	w.RegisterActivity(acts.StartCaptureActivity)
	w.RegisterActivity(acts.MarkPipelineActiveActivity)
	w.RegisterActivity(acts.MarkPipelinePausedActivity)
	w.RegisterActivity(acts.StopCaptureActivity)
	w.RegisterActivity(acts.DeleteSinkActivity)
	w.RegisterActivity(acts.DeleteStreamActivity)
	w.RegisterActivity(acts.MarkPipelineErrorActivity)

	// Register session-2 workflows + activities.
	w.RegisterWorkflow(apptemporal.ProvisionCDCPipelineWorkflow)
	w.RegisterWorkflow(apptemporal.PauseCDCPipelineWorkflow)
	w.RegisterWorkflow(apptemporal.ResumeCDCPipelineWorkflow)
	w.RegisterWorkflow(apptemporal.DecommissionCDCPipelineWorkflow)
	w.RegisterActivity(s2acts.CreateConnectorActivity)
	w.RegisterActivity(s2acts.WaitForConnectorRunningActivity)
	w.RegisterActivity(s2acts.DeleteConnectorActivity)
	w.RegisterActivity(s2acts.PauseConnectorActivity)
	w.RegisterActivity(s2acts.ResumeConnectorActivity)

	if err := w.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	defer w.Stop()

	log.Info("temporal worker started",
		"task_queue", apptemporal.DefaultTaskQueue,
		"build_id", apptemporal.WorkerBuildIDV2,
	)

	// --- HTTP server ---
	wfStarter := &temporalSession2Starter{client: tc}
	h := api.NewHandler(ms, wfStarter, api.AllowAllAuthorizer{}, log)
	connH := &api.ConnectorHandler{
		Store:          ms,
		ConnectorStore: cs,
		CDCWorkflows:   wfStarter,
		Auth:           api.AllowAllAuthorizer{},
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	connH.RegisterConnectorRoutes(mux)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("HTTP server listening", "addr", listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server error", "error", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

// temporalSession2Starter implements both api.WorkflowStarter and api.CDCWorkflowStarter.
type temporalSession2Starter struct {
	client client.Client
}

func (s *temporalSession2Starter) StartProvisionWorkflow(ctx context.Context, spec domain.PipelineSpec) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("provision-%s-%s", spec.TenantID, spec.PipelineID),
		TaskQueue: apptemporal.DefaultTaskQueue,
	}
	run, err := s.client.ExecuteWorkflow(ctx, opts, apptemporal.ProvisionPipelineWorkflow, spec)
	if err != nil {
		return "", fmt.Errorf("execute provision workflow: %w", err)
	}
	return run.GetID(), nil
}

func (s *temporalSession2Starter) StartProvisionCDCWorkflow(ctx context.Context, spec domain.PipelineSpec, cfg domain.ConnectorConfig) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("cdc-provision-%s-%s", spec.TenantID, spec.PipelineID),
		TaskQueue: apptemporal.DefaultTaskQueue,
	}
	run, err := s.client.ExecuteWorkflow(ctx, opts, apptemporal.ProvisionCDCPipelineWorkflow, spec, cfg)
	if err != nil {
		return "", fmt.Errorf("execute CDC provision workflow: %w", err)
	}
	return run.GetID(), nil
}

func (s *temporalSession2Starter) StartDeleteConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("decommission-cdc-%s-%s", spec.TenantID, spec.PipelineID),
		TaskQueue: apptemporal.DefaultTaskQueue,
	}
	run, err := s.client.ExecuteWorkflow(ctx, opts, apptemporal.DecommissionCDCPipelineWorkflow, spec, connectorName, "", "")
	if err != nil {
		return "", fmt.Errorf("execute decommission workflow: %w", err)
	}
	return run.GetID(), nil
}

func (s *temporalSession2Starter) StartPauseConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("pause-cdc-%s-%s-%s", spec.TenantID, spec.PipelineID, connectorName),
		TaskQueue: apptemporal.DefaultTaskQueue,
	}
	run, err := s.client.ExecuteWorkflow(ctx, opts, apptemporal.PauseCDCPipelineWorkflow, spec, connectorName)
	if err != nil {
		return "", fmt.Errorf("execute pause workflow: %w", err)
	}
	return run.GetID(), nil
}

func (s *temporalSession2Starter) StartResumeConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("resume-cdc-%s-%s-%s", spec.TenantID, spec.PipelineID, connectorName),
		TaskQueue: apptemporal.DefaultTaskQueue,
	}
	run, err := s.client.ExecuteWorkflow(ctx, opts, apptemporal.ResumeCDCPipelineWorkflow, spec, connectorName)
	if err != nil {
		return "", fmt.Errorf("execute resume workflow: %w", err)
	}
	return run.GetID(), nil
}

// temporalLogger adapts slog to the Temporal SDK Logger interface.
type temporalLogger struct {
	log *slog.Logger
}

func newTemporalLogger(log *slog.Logger) *temporalLogger {
	return &temporalLogger{log: log}
}

func (l *temporalLogger) Debug(msg string, keyvals ...any) { l.log.Debug(msg, keyvals...) }
func (l *temporalLogger) Info(msg string, keyvals ...any)  { l.log.Info(msg, keyvals...) }
func (l *temporalLogger) Warn(msg string, keyvals ...any)  { l.log.Warn(msg, keyvals...) }
func (l *temporalLogger) Error(msg string, keyvals ...any) { l.log.Error(msg, keyvals...) }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
