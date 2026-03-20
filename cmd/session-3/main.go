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
	"app/internal/observability"
	"app/internal/store"
	apptemporal "app/internal/temporal"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	temporalAddr := envOr("TEMPORAL_ADDRESS", "localhost:7233")
	listenAddr := envOr("HTTP_ADDR", ":8082")
	metricsAddr := envOr("METRICS_ADDR", ":9090")
	kafkaConnectURL := envOr("KAFKA_CONNECT_URL", "http://localhost:8085")

	// --- Metrics stack ---
	metrics, err := observability.New()
	if err != nil {
		return fmt.Errorf("init metrics: %w", err)
	}
	defer func() { _ = metrics.MeterProvider.Shutdown(context.Background()) }()

	// --- In-memory backing stores ---
	ms := store.NewInMemoryMetadataStore()
	cs := store.NewInMemoryConnectorStore()
	auditStore := store.NewInMemoryAuditStore()
	secrets := connectors.NewInMemorySecretProvider()
	schemaReg := connectors.NewInMemorySchemaRegistry()
	cdc := connectors.NewFakeCDCProvisioner()
	streamProv := connectors.NewFakeStreamProvisioner()
	sinkProv := connectors.NewFakeSinkProvisioner()

	// --- Kafka Connect client (HTTP) ---
	kcClient := connectors.NewHTTPKafkaConnectClient(kafkaConnectURL)

	// --- Temporal client ---
	tc, err := client.Dial(client.Options{
		HostPort:       temporalAddr,
		Logger:         apptemporal.NewSDKLogger(log),
		MetricsHandler: metrics.TemporalHandler,
	})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer tc.Close()

	// --- Temporal worker: session-3 ---
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
		BuildID:                 apptemporal.WorkerBuildIDV3,
		UseBuildIDForVersioning: true,
	})
	apptemporal.RegisterSession3Worker(w, acts, s2acts)

	if err := w.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	defer w.Stop()

	log.Info("temporal worker started",
		"task_queue", apptemporal.DefaultTaskQueue,
		"build_id", apptemporal.WorkerBuildIDV3,
	)

	// --- Wire workflow starters with audit recording ---
	rawStarter := &temporalSession3Starter{client: tc}
	auditedStarter := api.NewAuditingWorkflowStarter(rawStarter, auditStore, "api")
	auditedCDCStarter := api.NewAuditingCDCWorkflowStarter(rawStarter, auditStore, "api")

	// --- HTTP handlers ---
	h := api.NewHandler(ms, auditedStarter, api.AllowAllAuthorizer{}, log)
	connH := &api.ConnectorHandler{
		Store:          ms,
		ConnectorStore: cs,
		CDCWorkflows:   auditedCDCStarter,
		Auth:           api.AllowAllAuthorizer{},
	}
	auditH := &api.AuditHandler{
		Store: ms,
		Audit: auditStore,
		Auth:  api.AllowAllAuthorizer{},
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	connH.RegisterConnectorRoutes(mux)
	auditH.RegisterAuditRoutes(mux)
	api.RegisterHealthRoute(mux)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      metrics.MetricsMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// --- Separate metrics server on :9090 ---
	metricsSrv := &http.Server{
		Addr:        metricsAddr,
		Handler:     metrics.Handler(),
		ReadTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("metrics server listening", "addr", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server error", "error", err)
		}
	}()
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

	_ = metricsSrv.Shutdown(shutCtx)
	return srv.Shutdown(shutCtx)
}

// temporalSession3Starter implements WorkflowStarter and CDCWorkflowStarter.
type temporalSession3Starter struct {
	client client.Client
}

func (s *temporalSession3Starter) executeWorkflow(ctx context.Context, opts client.StartWorkflowOptions, wf any, args ...any) (string, error) {
	run, err := s.client.ExecuteWorkflow(ctx, opts, wf, args...)
	if err != nil {
		return "", err
	}
	return run.GetID(), nil
}

func startOpts(id string) client.StartWorkflowOptions {
	return client.StartWorkflowOptions{
		ID:        id,
		TaskQueue: apptemporal.DefaultTaskQueue,
	}
}

func reusableOpts(id string) client.StartWorkflowOptions {
	return client.StartWorkflowOptions{
		ID:                    id,
		TaskQueue:             apptemporal.DefaultTaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}
}

func (s *temporalSession3Starter) StartProvisionWorkflow(ctx context.Context, spec domain.PipelineSpec) (string, error) {
	id := fmt.Sprintf("provision-%s-%s", spec.TenantID, spec.PipelineID)
	runID, err := s.executeWorkflow(ctx, startOpts(id), apptemporal.ProvisionPipelineWorkflow, spec)
	if err != nil {
		return "", fmt.Errorf("execute provision workflow: %w", err)
	}
	return runID, nil
}

func (s *temporalSession3Starter) StartProvisionCDCWorkflow(ctx context.Context, spec domain.PipelineSpec, cfg domain.ConnectorConfig) (string, error) {
	id := fmt.Sprintf("cdc-provision-%s-%s", spec.TenantID, spec.PipelineID)
	runID, err := s.executeWorkflow(ctx, startOpts(id), apptemporal.ProvisionCDCPipelineWorkflow, spec, cfg)
	if err != nil {
		return "", fmt.Errorf("execute CDC provision workflow: %w", err)
	}
	return runID, nil
}

func (s *temporalSession3Starter) StartDeleteConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	id := fmt.Sprintf("decommission-cdc-%s-%s", spec.TenantID, spec.PipelineID)
	runID, err := s.executeWorkflow(ctx, reusableOpts(id), apptemporal.DecommissionCDCPipelineWorkflow, spec, connectorName, "", "")
	if err != nil {
		return "", fmt.Errorf("execute decommission workflow: %w", err)
	}
	return runID, nil
}

func (s *temporalSession3Starter) StartPauseConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	id := fmt.Sprintf("pause-cdc-%s-%s-%s", spec.TenantID, spec.PipelineID, connectorName)
	runID, err := s.executeWorkflow(ctx, reusableOpts(id), apptemporal.PauseCDCPipelineWorkflow, spec, connectorName)
	if err != nil {
		return "", fmt.Errorf("execute pause workflow: %w", err)
	}
	return runID, nil
}

func (s *temporalSession3Starter) StartResumeConnectorWorkflow(ctx context.Context, spec domain.PipelineSpec, connectorName string) (string, error) {
	id := fmt.Sprintf("resume-cdc-%s-%s-%s", spec.TenantID, spec.PipelineID, connectorName)
	runID, err := s.executeWorkflow(ctx, reusableOpts(id), apptemporal.ResumeCDCPipelineWorkflow, spec, connectorName)
	if err != nil {
		return "", fmt.Errorf("execute resume workflow: %w", err)
	}
	return runID, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
