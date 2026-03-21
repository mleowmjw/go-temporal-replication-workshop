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

	bg "app/internal/bluegreen"
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
	listenAddr := envOr("HTTP_ADDR", ":8083")

	// ── In-memory stores and fakes (swap for real DB/migrator in E2E) ────────
	deployStore := bg.NewInMemoryDeploymentStore()
	migrator := bg.NewFakeDatabaseMigrator()
	app := bg.NewCustomerApp()

	// ── Temporal client ───────────────────────────────────────────────────────
	tc, err := client.Dial(client.Options{
		HostPort: temporalAddr,
		Logger:   apptemporal.NewSDKLogger(log),
	})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer tc.Close()

	// ── Temporal worker ───────────────────────────────────────────────────────
	deps := bg.BGDependencies{
		Store:    deployStore,
		Migrator: migrator,
		App:      app,
	}
	acts := bg.NewBGActivities(deps)

	w := worker.New(tc, bg.TaskQueue, worker.Options{
		BuildID:                 bg.WorkerBuildID,
		UseBuildIDForVersioning: true,
	})
	bg.RegisterBlueGreenWorker(w, acts)

	if err := w.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	defer w.Stop()

	log.Info("temporal worker started", "task_queue", bg.TaskQueue, "build_id", bg.WorkerBuildID)

	// ── HTTP server ───────────────────────────────────────────────────────────
	wfStarter := &temporalWorkflowStarter{client: tc}
	h := bg.NewHandler(deployStore, wfStarter, log)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

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

// temporalWorkflowStarter implements bg.WorkflowStarter using the real Temporal client.
type temporalWorkflowStarter struct {
	client client.Client
}

func (s *temporalWorkflowStarter) StartDeployment(ctx context.Context, plan bg.MigrationPlan) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        "bg-deploy-" + plan.ID,
		TaskQueue: bg.TaskQueue,
	}
	run, err := s.client.ExecuteWorkflow(ctx, opts, bg.BlueGreenDeploymentWorkflow, plan)
	if err != nil {
		return "", fmt.Errorf("execute workflow: %w", err)
	}
	return run.GetID(), nil
}

func (s *temporalWorkflowStarter) ApproveDeployment(ctx context.Context, deploymentID string, payload bg.ApprovalPayload) error {
	return s.client.SignalWorkflow(ctx, "bg-deploy-"+deploymentID, "", bg.SignalApprove, payload)
}

func (s *temporalWorkflowStarter) RollbackDeployment(ctx context.Context, deploymentID string, payload bg.RollbackPayload) error {
	return s.client.SignalWorkflow(ctx, "bg-deploy-"+deploymentID, "", bg.SignalRollback, payload)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
