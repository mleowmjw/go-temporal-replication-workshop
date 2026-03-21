package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	bg "app/internal/bluegreen"
	apptemporal "app/internal/temporal"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	sdkworker "go.temporal.io/sdk/worker"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	temporalAddr := envOr("TEMPORAL_ADDRESS", "localhost:7233")
	listenAddr := envOr("HTTP_ADDR", ":8083")

	app := bg.NewCustomerApp()

	// ── Migrator: real Postgres by default, fake when BG_MODE=test ───────────
	var migrator bg.DatabaseMigrator
	if envOr("BG_MODE", "e2e") == "test" {
		log.Info("using fake in-memory migrator (BG_MODE=test)")
		migrator = bg.NewFakeDatabaseMigrator()
	} else {
		dbURL := envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5435/appdb")
		pool, err := pgxpool.New(ctx, dbURL)
		if err != nil {
			return fmt.Errorf("connect to postgres (%s): %w", dbURL, err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("ping postgres: %w", err)
		}
		log.Info("connected to postgres", "url", dbURL)
		migrator = bg.NewPgDatabaseMigrator(pool)
	}

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
		Migrator: migrator,
		App:      app,
	}
	acts := bg.NewBGActivities(deps)

	w := sdkworker.New(tc, bg.TaskQueue, sdkworker.Options{
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
	wfClient := &temporalWorkflowClient{client: tc}
	h := bg.NewHandler(wfClient, log)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

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

// temporalWorkflowClient implements bg.WorkflowClient using the real Temporal client.
type temporalWorkflowClient struct {
	client client.Client
}

func (s *temporalWorkflowClient) StartDeployment(ctx context.Context, plan bg.MigrationPlan) (string, error) {
	opts := client.StartWorkflowOptions{
		ID:        "bg-deploy-" + plan.ID,
		TaskQueue: bg.TaskQueue,
	}
	run, err := s.client.ExecuteWorkflow(ctx, opts, bg.BlueGreenDeploymentWorkflow, plan)
	if err != nil {
		if temporal.IsWorkflowExecutionAlreadyStartedError(err) {
			return "", bg.ErrDeploymentAlreadyExists
		}
		return "", fmt.Errorf("execute workflow: %w", err)
	}
	return run.GetID(), nil
}

func (s *temporalWorkflowClient) ApproveDeployment(ctx context.Context, deploymentID string, payload bg.ApprovalPayload) error {
	return s.client.SignalWorkflow(ctx, "bg-deploy-"+deploymentID, "", bg.SignalApprove, payload)
}

func (s *temporalWorkflowClient) RollbackDeployment(ctx context.Context, deploymentID string, payload bg.RollbackPayload) error {
	return s.client.SignalWorkflow(ctx, "bg-deploy-"+deploymentID, "", bg.SignalRollback, payload)
}

func (s *temporalWorkflowClient) GetDeploymentStatus(ctx context.Context, deploymentID string) (bg.DeploymentStatus, error) {
	resp, err := s.client.QueryWorkflow(ctx, "bg-deploy-"+deploymentID, "", bg.QueryDeploymentState)
	if err != nil {
		if isWorkflowNotFound(err) {
			return bg.DeploymentStatus{}, bg.ErrDeploymentNotFound
		}
		return bg.DeploymentStatus{}, fmt.Errorf("query workflow: %w", err)
	}
	var status bg.DeploymentStatus
	if err := resp.Get(&status); err != nil {
		return bg.DeploymentStatus{}, fmt.Errorf("decode query result: %w", err)
	}
	return status, nil
}

// isWorkflowNotFound returns true when the Temporal server cannot find the workflow.
func isWorkflowNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "workflow execution not found") ||
		strings.Contains(msg, "workflow not found") ||
		strings.Contains(msg, "EntityNotExistsError")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
