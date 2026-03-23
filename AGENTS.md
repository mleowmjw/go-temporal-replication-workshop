# AGENTS

## Language

- Use latest Go v1.26.x and its full capabilities
- Always prefer stdlib if available and it make sense

## Orchestration

- Use Temporal + Go SDK to handle the workflows
- Use Temporal testsuite as much as possible to have widest test coverage
- Use advanced techique like registerdelayedcallback to simulate long running system
- Ensure use Temporal Worker version to ensure multiple workflow versions can co-exists

## Runtime

- Whole system should be fully testable standalone with Go binary
- Use modern Go capabilities (when needed): generics, structured log, built-in http
routing, testing/synctest
- Use techniques like first class anonymous function as method replacement, synctest
 to ensure all things are deterministic

## Testing

- All new cases should be at least 80% coverage
- Unit tests and inetgration tests MUST be comepleted without needing to spin up any
 external dependencies
- Only Full End-to-End should need to stand up Temporal Test Server

## Tools

- Use mise to run tasks, set env variables, automate
- Setup env like CGO_ENABLED=0, PATH append to use ~/go/bin and loading from .env
- Tools available: ripgrep, fzf, air, goreleaser, watchexec
- Use overmind to start temporal-cli + air
- Each session has its own `Procfile.<session>` at workspace root and its own
  `.air.toml` under `cmd/<session>/`
- Use `overmind start -f Procfile.<session>` via `mise run <session>:dev`
- Docker compose runs **without** `-d` inside Procfiles so overmind owns the lifecycle

## Specification (MVP)

- Follow cmd/replication-platform/PRD.md for suggested details but it MUST NOT override what stated here
- This is a workshop with 3 parts; start with cmd/session-1
- Once successful, then do cmd/session-2
- Finally complete cmd/session-3
- Ask if anything unsure or contradictory
- Use mise to launch the workshops for all sessions

## Specification (Advanced) 
- Finally CI/CD will use End-to-End Tests
- Implement this ONLY after Unit/Integration tests are passing

## Implementation Status & Learnings

### Database

- **Always use PostgreSQL 18** (`postgres:18` Docker image) across all sessions
- Add `track_commit_timestamp=on` to the postgres command flags so that
  `pg_stat_subscription_stats` can track `*_origin_differs` conflict counters
- Add healthcheck to the compose service so dependent services wait properly

### PostgreSQL 18 CDC Features (actively used in the workshop)

| Feature | How we use it |
|---|---|
| **Stored generated columns** | `search_key` column in `inventory.customers` is `GENERATED ALWAYS AS … STORED`; shows up in CDC stream |
| **`publish_generated_columns = stored`** | Publication created with this option so Debezium receives computed values without recalculating on the subscriber |
| **`failover = true` on replication slot** | Slot created via `pg_create_logical_replication_slot(…, failover => true)` — survives primary failover by syncing to physical standbys |
| **`track_commit_timestamp = on`** | Postgres flag; enables `confl_*_origin_differs` counters in `pg_stat_subscription_stats` for conflict monitoring |
| **`pg_stat_subscription_stats`** | New view in PG18; query in verification/observability sections |
| **`pg_read_all_data`** | Predefined role (PG14+) granted to `dbz`; cleaner than per-table `GRANT SELECT` |

#### Other PG18 capabilities available but not yet wired in

- `UUIDv7` (`uuidv7()`) — timestamp-ordered UUIDs; useful if we ever switch PKs away from BIGSERIAL
- `RETURNING OLD/NEW` in UPDATE/DELETE — surfaces old and new row values in a single statement; relevant for CDC verification queries
- `streaming = parallel` is now the default for `CREATE SUBSCRIPTION` — benefits Debezium session-2
- Asynchronous I/O (`io_method=worker` or `io_uring`) — significant throughput gain for write-heavy CDC sources; opt-in via pg command flag

### Overmind + air per-session convention

- **One Procfile per session** at workspace root: `Procfile.session-1`, `Procfile.session-2`, …
- **Three standard processes** per session Procfile:
  - `temporal`: `temporal server start-dev --db-filename .temporal/temporal.db --metrics-port 8077`
  - `postgres`: `docker compose -f cmd/session-N/01-db/compose.yml up` (no `-d`; foreground)
  - `app`: `air -c cmd/session-N/.air.toml`
- **One `.air.toml` per session** at `cmd/session-N/.air.toml`; build output goes to `.air-session-N/`
- **mise task** `session-N:dev` runs `overmind start -f Procfile.session-N`
- `.temporal/` and `.air-session-*/` are git-ignored

### Session-1 package layout

```
cmd/session-1/main.go           entry point: HTTP server + Temporal worker
cmd/session-1/01-db/            Postgres 18 compose + init SQL
cmd/session-1/.air.toml         air hot-reload config
Procfile.session-1              overmind stack (temporal + postgres + app)
internal/domain/                types + validation (100 % coverage)
internal/store/                 MetadataStore interface + InMemoryMetadataStore
internal/connectors/            CDCProvisioner, StreamProvisioner, SinkProvisioner,
                                SecretProvider, SchemaRegistry — interfaces + fakes
internal/temporal/              ProvisionPipelineWorkflow + 8 activities + tests
internal/api/                   HTTP handlers + Authorizer + routing
```

### Temporal worker versioning

- Every binary sets `BuildID` in `worker.Options` (e.g. `session-1-v1`)
- `UseBuildIDForVersioning: true` so multiple worker versions can coexist

### Build convention

- **All session binaries are compiled to `bin/`** — e.g. `bin/session-1`, `bin/session-2`
- The `bin/` directory is git-ignored (already covered by `./bin` in `.gitignore`)
- Always use the `session-N:build` mise task rather than `go build` directly, so paths stay consistent
- `air` hot-reload also targets `bin/session-N` (not a temp dir)

### mise task conventions

- `session-N:build`    — `go build -o bin/session-N ./cmd/session-N`
- `session-N:dev`      — `overmind start -f Procfile.session-N`
- `session-N`          — `mise run session-N:build && bin/session-N`
- `session-N:test`     — `go test -race -v -count=1 ./internal/...`
- `session-N:db:up`    — `docker compose … up -d`
- `session-N:db:down`  — `docker compose … down -v`
- `session-N:db:psql`  — psql shortcut
- `session-N:db:logs`  — tail container logs

---

## Idiomatic Temporal — Learnings (blue-green refactor)

These rules are derived from a concrete mistake: an initial implementation that
duplicated workflow state in an external `InMemoryDeploymentStore` and used an
`UpdatePhaseActivity` to sync every phase transition to it. That pattern is
**anti-idiomatic** and was fully removed. Do not repeat it.

### 1. Temporal IS the store — never shadow workflow state externally

Temporal's event history is the durable, replayable source of truth for all
workflow state. Building a parallel store (in-memory map, database table, Redis
key, etc.) that mirrors what phase the workflow is in is redundant and creates
two sources of truth that can diverge.

**Wrong pattern (do not do this):**
```go
// Anti-pattern: activity whose only job is to update a shadow store
func (a *Activities) UpdatePhaseActivity(ctx context.Context, id string, phase Phase, ...) error {
    return a.deps.Store.UpdatePhase(ctx, id, phase, ...)  // just mirrors workflow code-path
}
// Called ~8 times per workflow, adding ~16 history events of pure overhead
```

**Right pattern:**
```go
// Workflow-local state — lives in Temporal history, zero external writes
phase := PhasePending
var history []PhaseTransition

setPhase := func(p Phase, reason string) {
    phase = p
    history = append(history, PhaseTransition{Phase: p, At: workflow.Now(ctx), Reason: reason})
}
```

### 2. Use `workflow.SetQueryHandler` to expose live state

Queries are the idiomatic Temporal mechanism for reading workflow state from
outside (HTTP handlers, CLI, dashboards) without polling or writing to an
external store.

- Register the query handler **before any blocking call** so the workflow is
  queryable from the instant it starts.
- The handler is a plain closure capturing workflow-local variables — it reads
  state without mutating it.
- Works on **running** workflows (returns live state) and **completed** workflows
  (Temporal replays history to reconstruct the last state).

```go
const QueryDeploymentState = "deployment_state"

// Register once at the top of the workflow function:
_ = workflow.SetQueryHandler(ctx, QueryDeploymentState, func() (DeploymentStatus, error) {
    return DeploymentStatus{ID: plan.ID, Phase: phase, Plan: plan, History: history}, nil
})
```

From the HTTP handler or test, query via the Temporal client:
```go
resp, err := client.QueryWorkflow(ctx, workflowID, "", QueryDeploymentState)
var status DeploymentStatus
resp.Get(&status)
```

In tests, use `env.QueryWorkflow` inside a `RegisterDelayedCallback` to assert
mid-workflow state (while the workflow is blocked on a signal/selector):
```go
s.env.RegisterDelayedCallback(func() {
    val, _ := s.env.QueryWorkflow(QueryDeploymentState)
    var status DeploymentStatus
    val.Get(&status)
    assert.Equal(t, PhaseExpandVerify, status.Phase)
}, 500*time.Millisecond)
```

### 3. Activities should only do real work — never update a shadow state

An activity that exists solely to update an external store is a smell. Activities
should interact with the real world (database DDL, load-balancer API, feature
flag service). Phase tracking belongs in the workflow via `setPhase()`.

**Consequence for `SwitchTrafficActivity`:** previously it called
`Store.UpdatePhase(ctx, id, PhaseMonitoring, ...)` as a side-effect. After the
refactor it is a pure side-effect placeholder (load balancer / feature flag call).
The phase transition to `PhaseMonitoring` is done directly in the workflow with
`setPhase(PhaseMonitoring, "traffic switched to green")`.

### 4. Use `workflow.Now(ctx)` for timestamps inside the workflow

`time.Now()` inside a workflow (or inside an activity called to set state) is
non-deterministic across replays. Use `workflow.Now(ctx)` when you need a
timestamp that must be consistent across Temporal history replays.

```go
// Correct — deterministic:
history = append(history, PhaseTransition{Phase: p, At: workflow.Now(ctx), ...})

// Wrong — breaks deterministic replay if used inside workflow code:
history = append(history, PhaseTransition{Phase: p, At: time.Now(), ...})
```

### 5. Rollback / compensation is a successful outcome, not an error

When a workflow compensates (rolls back the expand, releases locks), it has
**successfully completed its job**. Returning a Go `error` from the workflow
function in this case:
- Shows the workflow execution as `FAILED` in the Temporal UI (misleading)
- Makes `env.GetWorkflowResult(&result)` impossible in tests (SDK returns the
  error, not the result value)

Return `(result, nil)` with `result.Phase = PhaseRolledBack`. Reserve non-nil
errors for genuinely unexpected failures (plan validation, contract SQL failure
that cannot be compensated).

```go
compensate := func(reason string) (DeploymentResult, error) {
    // ... release lock, run rollback SQL ...
    setPhase(PhaseRolledBack, reason)
    return DeploymentResult{DeploymentID: plan.ID, Phase: PhaseRolledBack, History: history}, nil
    // NOT: return result, fmt.Errorf("rolled back: %s", reason)
}
```

### 6. Workflow result should carry full state for callers

Return the complete `DeploymentResult` (including `History`) from the workflow
function. This lets callers who `Get()` on the workflow run inspect the full
lifecycle without needing a separate query.

```go
return DeploymentResult{
    DeploymentID: plan.ID,
    Phase:        PhaseComplete,
    History:      history,  // full ordered phase log
}, nil
```

### 7. Test workflow state via result or mid-flight QueryWorkflow — not external stores

With the query handler approach, tests become simpler:

- **Happy path / terminal state**: use `env.GetWorkflowResult(&result)` and
  assert on `result.Phase` and `result.History`.
- **Mid-flight state**: use `env.RegisterDelayedCallback` + `env.QueryWorkflow`
  to assert state while the workflow is blocked on a signal.
- **Never** create a store, seed it, and call `store.Get()` to verify workflow
  progress — that is the anti-pattern we removed.

### 8. Duplicate workflow ID detection belongs in the client adapter

`temporal.IsWorkflowExecutionAlreadyStartedError(err)` (from
`go.temporal.io/sdk/temporal`) detects when `ExecuteWorkflow` is called with an
already-running workflow ID. Map this to a domain sentinel error (`ErrDeploymentAlreadyExists`)
at the adapter layer so the rest of the codebase does not depend on Temporal
error types:

```go
run, err := s.client.ExecuteWorkflow(ctx, opts, BlueGreenDeploymentWorkflow, plan)
if temporal.IsWorkflowExecutionAlreadyStartedError(err) {
    return "", ErrDeploymentAlreadyExists
}
```

The HTTP handler then maps `ErrDeploymentAlreadyExists` → `409 Conflict` without
importing any Temporal packages.

---

## Idiomatic Temporal — Learnings (DatabaseOpsWorkflow / concurrency control)

### 9. `workflow.Go` goroutines must use their own context for blocking calls

`workflow.Go(ctx, func(gCtx workflow.Context) { ... })` creates a new Temporal
coroutine. **Every blocking call inside it must use `gCtx`** (the parameter
passed to the closure), never the parent `ctx`. Using the parent context causes:

> `trying to block on coroutine which is already blocked, most likely a wrong
> Context is used to do blocking call`

This is the workflow equivalent of `time.Sleep` inside workflow code — it breaks
deterministic replay. The same rule applies to `workflow.NewTimer`, `Future.Get`,
and `Channel.Receive` inside any `workflow.Go` goroutine.

```go
// WRONG — ctx is already in use by sel.Select(ctx) on the main coroutine:
workflow.Go(lockCtx, func(gCtx workflow.Context) {
    _ = workflow.SignalExternalWorkflow(ctx, id, "", signal, nil).Get(ctx, nil)
})

// CORRECT — gCtx is scoped to this goroutine:
workflow.Go(lockCtx, func(gCtx workflow.Context) {
    _ = workflow.SignalExternalWorkflow(gCtx, id, "", signal, nil).Get(gCtx, nil)
})
```

### 10. Update validator errors: use `NewApplicationError` directly

When a `SetUpdateHandlerWithOptions` validator returns an error, return a clean
`*temporal.ApplicationError` directly. Do **not** use `fmt.Errorf("%w", appErr)`
to wrap it — the outer `fmt.wrapError` becomes the outermost error in the
serialised failure chain. The client receives this wrapped form and
`errors.As(err, &appErr)` finds the wrong type ("wrapError"), causing
`appErr.Type() == "SchemaLocked"` to be false and breaking error detection.

```go
// WRONG — fmt.Errorf wraps the ApplicationError; client sees type "wrapError" first:
return fmt.Errorf("%w: deployment %s is active", ErrSchemaLocked, activeDeployment.PlanID)

// CORRECT — one clean ApplicationError with the right type:
return temporal.NewApplicationError(
    fmt.Sprintf("deployment %s is active", activeDeployment.PlanID),
    "SchemaLocked",
)
```

On the client side, always walk the **full error chain** rather than stopping at
the first `ApplicationError` found:

```go
func isSchemaLockedError(err error) bool {
    for e := err; e != nil; e = errors.Unwrap(e) {
        var appErr *temporal.ApplicationError
        if errors.As(e, &appErr) && appErr.Type() == "SchemaLocked" {
            return true
        }
    }
    return strings.Contains(err.Error(), "SchemaLocked") ||
        strings.Contains(err.Error(), "schema locked")
}
```

### 11. DatabaseOpsWorkflow — long-running coordinator pattern

For operations that span many independent workflows (e.g. all deployments to
the same database), use a single long-lived coordinator workflow per resource:

- **Workflow ID** is derived from the resource identifier (e.g.
  `db-ops-<fingerprint-of-db-url>`). This gives idempotent start for free.
- **Update handler** (`SetUpdateHandlerWithOptions`) provides a synchronous
  lock-request API with a **Validator** that rejects concurrent requests.
- **`workflow.Go` + `workflow.NewTimer`** implements a per-deployment lock
  timeout inside the coordinator, sending a rollback signal if the deployment
  does not finish in time.
- **`ContinueAsNew`** after 24 hours keeps history size bounded. Defer it
  until `activeDeployment == nil` by checking `continueAsNewRequested` inside
  the selector loop rather than adding the timer future a second time.
- **Signal back**: the deployment workflow signals `SignalDeploymentComplete`
  to the coordinator on every terminal path (complete, rolled_back, failed) so
  the lock is always released.

---

## PostgreSQL DDL — Learnings (blue-green activities)

### `CONCURRENTLY` DDL cannot run inside a transaction

`CREATE INDEX CONCURRENTLY` and `DROP INDEX CONCURRENTLY` raise `SQLSTATE 25001`
if executed inside a transaction block. Detect and run them directly on the pool:

```go
if strings.Contains(strings.ToUpper(stmt), "CONCURRENTLY") {
    _, err = pool.Exec(ctx, stmt)  // no transaction
} else {
    // normal transactional DDL
}
```

### `GENERATED ALWAYS AS … STORED` columns create DROP COLUMN dependencies

If a generated column references another column, `DROP COLUMN` on the referenced
column fails with `SQLSTATE 2BP01` ("other objects depend on it"). Always use
`CASCADE` and immediately recreate the generated column referencing the new
column name:

```sql
-- ContractSQL for full_name → display_name rename:
ALTER TABLE inventory.customers DROP COLUMN IF EXISTS full_name CASCADE;
-- search_key was dropped by CASCADE; recreate it using display_name:
ALTER TABLE inventory.customers
  ADD COLUMN IF NOT EXISTS search_key TEXT
  GENERATED ALWAYS AS (lower(display_name) || ' ' || lower(email)) STORED;
```

Always use schema-qualified names (`inventory.customers`, not `customers`) when
the table is not in the `public` schema, since PostgreSQL's `search_path` may
not include custom schemas by default in connection pools.
