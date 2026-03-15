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
