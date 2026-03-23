package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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

// bgWorkflowIDPrefix is the workflow ID prefix for BlueGreenDeploymentWorkflow runs.
const bgWorkflowIDPrefix = "bg-deploy-"

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

	dbURL := envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5435/appdb")

	// ── Migrator: real Postgres by default, fake when BG_MODE=test ───────────
	var migrator bg.DatabaseMigrator
	var pool *pgxpool.Pool
	if envOr("BG_MODE", "e2e") == "test" {
		log.Info("using fake in-memory migrator (BG_MODE=test)")
		migrator = bg.NewFakeDatabaseMigrator()
	} else {
		var err error
		pool, err = pgxpool.New(ctx, dbURL)
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

	w := sdkworker.New(tc, bg.TaskQueue, sdkworker.Options{})
	bg.RegisterBlueGreenWorker(w, acts)

	if err := w.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	defer w.Stop()

	log.Info("temporal worker started", "task_queue", bg.TaskQueue)

	// ── Database ops coordinator config ──────────────────────────────────────
	env := bg.Environment(envOr("BG_ENVIRONMENT", string(bg.EnvDev)))
	dbOpsConfig := bg.DatabaseOpsConfig{
		DatabaseID:  bg.DatabaseFingerprint(dbURL),
		Environment: env,
	}
	dbOpsWorkflowID := bg.DatabaseOpsWorkflowIDPrefix + dbOpsConfig.DatabaseID
	log.Info("database ops coordinator",
		"workflowID", dbOpsWorkflowID,
		"environment", env,
		"lockTimeout", env.LockTimeout(),
	)

	// ── HTTP server ───────────────────────────────────────────────────────────
	wfClient := &temporalWorkflowClient{
		client:          tc,
		dbOpsConfig:     dbOpsConfig,
		dbOpsWorkflowID: dbOpsWorkflowID,
	}
	h := bg.NewHandler(wfClient, log)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// ── Workshop UI ───────────────────────────────────────────────────────────
	log.Info("initialising workshop UI handler", "pool_nil", pool == nil)
	uiHandler, err := bg.NewUIHandler(pool, wfClient, app, log)
	if err != nil {
		log.Error("FATAL: failed to init UI handler", "error", err)
		return fmt.Errorf("init UI handler: %w", err)
	}
	log.Info("registering workshop UI routes (GET /, GET /ui/state, POST /ui/*)")
	uiHandler.RegisterUIRoutes(mux)
	logRouteProbes(log, mux)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Bind explicitly so port conflicts fail immediately and terminate the process.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}
	serveErrCh := make(chan error, 1)
	go func() {
		log.Info("HTTP server listening", "addr", listenAddr)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
			return
		}
		serveErrCh <- nil
	}()

	select {
	case err := <-serveErrCh:
		if err != nil {
			log.Error("HTTP server error", "error", err)
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return errors.New("HTTP server stopped unexpectedly")
	case <-ctx.Done():
		log.Info("shutting down")

		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		// Ensure serve goroutine exits before returning.
		_ = <-serveErrCh
		return nil
	}
}

// temporalWorkflowClient implements bg.WorkflowClient using the real Temporal client.
type temporalWorkflowClient struct {
	client          client.Client
	dbOpsConfig     bg.DatabaseOpsConfig
	dbOpsWorkflowID string
}

// StartDeployment:
//  1. Ensures the DatabaseOpsWorkflow coordinator is running (idempotent start).
//  2. Acquires the schema lock via an Update.
//  3. Starts the BlueGreenDeploymentWorkflow with a reference back to the coordinator.
func (s *temporalWorkflowClient) StartDeployment(ctx context.Context, plan bg.MigrationPlan) (string, error) {
	// 1. Start coordinator (or connect to existing run).
	_, err := s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        s.dbOpsWorkflowID,
		TaskQueue: bg.TaskQueue,
	}, bg.DatabaseOpsWorkflow, s.dbOpsConfig)
	if err != nil && !temporal.IsWorkflowExecutionAlreadyStartedError(err) {
		return "", fmt.Errorf("start database ops coordinator: %w", err)
	}

	// 2. Acquire schema lock via Update — synchronous and strongly consistent.
	deploymentWorkflowID := bgWorkflowIDPrefix + plan.ID
	handle, err := s.client.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   s.dbOpsWorkflowID,
		UpdateName:   bg.UpdateRequestDeployment,
		WaitForStage: client.WorkflowUpdateStageCompleted,
		Args:         []any{bg.DeploymentLockRequest{PlanID: plan.ID, WorkflowID: deploymentWorkflowID}},
	})
	if err != nil {
		if isSchemaLockedError(err) {
			return "", bg.ErrSchemaCurrentlyLocked
		}
		return "", fmt.Errorf("request deployment lock: %w", err)
	}
	var lockResp bg.DeploymentLockResponse
	if err := handle.Get(ctx, &lockResp); err != nil {
		if isSchemaLockedError(err) {
			return "", bg.ErrSchemaCurrentlyLocked
		}
		return "", fmt.Errorf("deployment lock response: %w", err)
	}

	// 3. Start the deployment workflow, wiring it back to the coordinator.
	req := bg.DeploymentRequest{
		Plan:             plan,
		ParentWorkflowID: s.dbOpsWorkflowID,
	}
	run, err := s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        deploymentWorkflowID,
		TaskQueue: bg.TaskQueue,
	}, bg.BlueGreenDeploymentWorkflow, req)
	if err != nil {
		if temporal.IsWorkflowExecutionAlreadyStartedError(err) {
			return "", bg.ErrDeploymentAlreadyExists
		}
		return "", fmt.Errorf("execute deployment workflow: %w", err)
	}
	return run.GetID(), nil
}

func (s *temporalWorkflowClient) ApproveDeployment(ctx context.Context, deploymentID string, payload bg.ApprovalPayload) error {
	return s.client.SignalWorkflow(ctx, bgWorkflowIDPrefix+deploymentID, "", bg.SignalApprove, payload)
}

func (s *temporalWorkflowClient) RollbackDeployment(ctx context.Context, deploymentID string, payload bg.RollbackPayload) error {
	return s.client.SignalWorkflow(ctx, bgWorkflowIDPrefix+deploymentID, "", bg.SignalRollback, payload)
}

func (s *temporalWorkflowClient) GetDeploymentStatus(ctx context.Context, deploymentID string) (bg.DeploymentStatus, error) {
	resp, err := s.client.QueryWorkflow(ctx, bgWorkflowIDPrefix+deploymentID, "", bg.QueryDeploymentState)
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

func (s *temporalWorkflowClient) GetDatabaseOpsState(ctx context.Context) (bg.DatabaseOpsState, error) {
	resp, err := s.client.QueryWorkflow(ctx, s.dbOpsWorkflowID, "", bg.QueryDatabaseOpsState)
	if err != nil {
		if isWorkflowNotFound(err) {
			return bg.DatabaseOpsState{}, bg.ErrDeploymentNotFound
		}
		return bg.DatabaseOpsState{}, fmt.Errorf("query database ops workflow: %w", err)
	}
	var state bg.DatabaseOpsState
	if err := resp.Get(&state); err != nil {
		return bg.DatabaseOpsState{}, fmt.Errorf("decode database ops state: %w", err)
	}
	return state, nil
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

// isSchemaLockedError returns true when the DatabaseOpsWorkflow Update validator
// rejected the request because the schema lock is already held.
//
// The error may arrive as a chain of ApplicationErrors (outer wrapper type
// "wrapError", inner type "SchemaLocked") so we walk the entire chain rather
// than stopping at the first ApplicationError found.
func isSchemaLockedError(err error) bool {
	if err == nil {
		return false
	}
	// Walk all ApplicationErrors in the chain.
	for e := err; e != nil; e = errors.Unwrap(e) {
		var appErr *temporal.ApplicationError
		if errors.As(e, &appErr) {
			if appErr.Type() == "SchemaLocked" {
				return true
			}
		}
	}
	// Fallback: inspect the error string (covers gRPC-wrapped variants).
	s := err.Error()
	return strings.Contains(s, "SchemaLocked") || strings.Contains(s, "schema locked")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type routeProbe struct {
	Method string
	Path   string
}

// logRouteProbes prints how net/http ServeMux resolves each expected route.
// This helps verify there is no pattern conflict between API and UI routes.
func logRouteProbes(log *slog.Logger, mux *http.ServeMux) {
	probes := []routeProbe{
		{Method: http.MethodGet, Path: "/"},
		{Method: http.MethodGet, Path: "/ui"},
		{Method: http.MethodGet, Path: "/ui/"},
		{Method: http.MethodGet, Path: "/ui/state"},
		{Method: http.MethodGet, Path: "/healthz"},
		{Method: http.MethodGet, Path: "/v1/database"},
		{Method: http.MethodGet, Path: "/v1/deployments/demo"},
		{Method: http.MethodPost, Path: "/v1/deployments"},
		{Method: http.MethodPost, Path: "/v1/deployments/demo/approve"},
		{Method: http.MethodPost, Path: "/v1/deployments/demo/rollback"},
		{Method: http.MethodPost, Path: "/ui/deploy"},
		{Method: http.MethodPost, Path: "/ui/approve"},
		{Method: http.MethodPost, Path: "/ui/rollback"},
	}

	for _, p := range probes {
		req, err := http.NewRequest(p.Method, "http://route-probe.local"+p.Path, nil)
		if err != nil {
			log.Error("route probe request build failed", "method", p.Method, "path", p.Path, "error", err)
			continue
		}
		_, pattern := mux.Handler(req)
		if pattern == "" {
			log.Warn("route probe unmatched", "method", p.Method, "path", p.Path)
			continue
		}
		log.Info("route probe matched", "method", p.Method, "path", p.Path, "pattern", pattern)
	}
}
