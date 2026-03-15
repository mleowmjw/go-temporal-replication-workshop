# PRD: Multi-tenant CDC Replication Platform (Temporal-orchestrated)

## 1) Summary
Build a managed, multi-tenant Change Data Capture (CDC) replication platform that provisions and operates near-real-time data pipelines from heterogeneous sources (initially PostgreSQL) into multiple sinks (initially “search index” and “analytical lake”), with strong automation, isolation, and observability.

This PRD is modeled on the evolution path described by Datadog: starting from a single-purpose Postgres→search replication, then generalizing into a multi-tenant replication platform with Temporal-driven provisioning, asynchronous replication, schema-compatibility controls, and customization mechanisms. :contentReference[oaicite:0]{index=0}

---

## 2) Problem statement
Teams repeatedly need “fresh-enough” downstream copies of operational data for:
- low-latency faceted search (denormalized docs)
- analytics/event-driven pipelines
- cross-region locality and resilience
- database unwinding / backups / migrations

Hand-built point solutions don’t scale: provisioning is error-prone, changes are risky (schema evolution), and operations become the bottleneck. Datadog explicitly hit this inflection and solved it with a managed multi-tenant platform plus Temporal automation. :contentReference[oaicite:1]{index=1}

---

## 3) Goals & Non-goals

### Goals
1. **Self-serve pipeline provisioning** (create/update/pause/resume/decommission) via API and UI later.
2. **Multi-tenancy**: per-tenant isolation, quotas, and blast-radius control.
3. **Asynchronous replication** as the default (optimize availability/throughput; accept bounded lag). :contentReference[oaicite:2]{index=2}
4. **Schema safety**:
   - pre-flight validation of schema migrations (policy checks)
   - runtime enforcement via schema registry compatibility (BACKWARD by default). :contentReference[oaicite:3]{index=3}
5. **Customization**:
   - message-level transforms (filter/reshape/reroute)
   - optional enrichment hooks (derived fields / metadata). :contentReference[oaicite:4]{index=4}
6. **Operational excellence**:
   - standard SLOs
   - first-class observability (metrics, logs, traces, audit events)

### Non-goals (initial release)
- Replacing Kafka Connect/Debezium internals with a bespoke CDC engine (we integrate through abstractions).
- Exactly-once end-to-end semantics across arbitrary sinks (we provide idempotency contracts; sinks may still be at-least-once).
- Full UI/portal (API + CLI first).

---

## 4) Personas & user journeys

### Personas
- **Application Team Engineer**: wants a search index / analytics table kept fresh without becoming a CDC expert.
- **Platform Operator (SRE/DE)**: wants guardrails, visibility, and safe defaults.
- **Data Consumer**: wants stable schemas and reliable downstream datasets.

### Primary user journeys
1. **Create pipeline** (tenant T):
   - define source (Postgres logical replication)
   - define sink (SearchIndex / Lake / Postgres target)
   - define transforms/enrichment
   - submit → platform provisions infra and begins replication
2. **Evolve schema**:
   - migration proposed → validation gate checks unsafe changes
   - if accepted, schemas propagate; consumers remain compatible (backward rules)
3. **Operate**:
   - monitor lag, throughput, error rates
   - pause/resume, replay from checkpoint, rotate credentials
4. **Decommission**:
   - stop connectors, clean resources, preserve audit trail

---

## 5) Functional requirements

### 5.1 Tenancy & isolation
- Tenant identity present in every pipeline, resource, and metric label.
- Isolation boundaries:
  - separate namespaces/queues per tenant (Temporal)
  - quotas: max pipelines, max throughput, max concurrent provisioning
  - per-tenant RBAC (admin/editor/viewer)

### 5.2 Pipeline definition
A pipeline is a declarative spec:
- Source:
  - type (Postgres initially)
  - connection ref (secret id)
  - capture scope (db/schema/tables)
- Transport:
  - logical stream (topic/stream abstraction)
  - serialization format + schema subject(s)
- Sink:
  - SearchIndex / Lake / Postgres / CustomSink
- Transform chain:
  - filter rows/events
  - projection/renames
  - doc denormalization rules (for search)
- Enrichment:
  - optional HTTP/gRPC hook (with timeout and circuit-break)
- Operational:
  - desired lag target
  - retry policies
  - DLQ policy (logical DLQ, not necessarily Kafka)
  - backfill strategy (snapshot + streaming)

### 5.3 Provisioning automation (Temporal)
Provisioning is a deterministic workflow that:
- validates spec (including schema policy checks)
- provisions source CDC capture (logical replication prerequisites)
- provisions transport + schema registry subjects
- provisions sink connectors
- rolls out in safe order with retries and compensation

Datadog cites Temporal as key to modular, reliable provisioning at scale. :contentReference[oaicite:5]{index=5}  
Temporal Go SDK testing is required for deterministic workflow logic. :contentReference[oaicite:6]{index=6}

### 5.4 Schema compatibility & governance
- Default compatibility: **BACKWARD** for Avro/Protobuf/JSON schema subjects. :contentReference[oaicite:7]{index=7}
- Explicitly block risky schema changes (example: introducing NOT NULL when historical events may have null). :contentReference[oaicite:8]{index=8}
- Provide “break-glass” override with approvals and scheduled rollout.

### 5.5 Observability & audit
For every pipeline and tenant:
- metrics:
  - replication lag (p50/p95), throughput, error rate
  - provisioning duration and failure reasons
- logs:
  - workflow history summaries, activity failures
- audit events:
  - create/update/pause/resume/decommission
  - schema validation decisions

### 5.6 Reliability behaviors
- Asynchronous replication (default), bounded lag target.
- At-least-once delivery is acceptable; require idempotent sink writes.
- Replay capability from checkpoints.
- Graceful degradation:
  - if enrichment fails → configurable: drop/enqueue for retry/DLQ

---

## 6) Non-functional requirements (NFRs)

### 6.1 SLOs (initial)
- Provisioning success rate: 99.5%
- Provisioning p95 latency: < 10 minutes (varies by backfill)
- Replication lag:
  - steady-state p95 < 2 seconds for “search class” pipelines (best-effort)
- Availability:
  - control plane API: 99.9%
  - workflow workers: 99.9%

### 6.2 Security
- Secrets never logged; stored in a secret manager (abstracted interface).
- Per-tenant RBAC.
- Network policy segmentation by tenant class (enterprise tiers).

### 6.3 Scalability targets (v1)
- 1,000 tenants
- 10,000 pipelines total
- 200 concurrent provisioning workflows
- Horizontal scale of workers with queue-based sharding

---

## 7) Success metrics
- Median time-to-first-pipeline: < 1 hour (from request to running)
- Reduced bespoke pipeline work by >80%
- Incident rate per pipeline reduced quarter-over-quarter
- Adoption: pipelines/tenant and retention

---

## 8) Release plan
### MVP (v0)
- Postgres source + SearchIndex sink (simulated sink in test)
- Create/Pause/Resume/Decommission
- Temporal-based provisioning with full testsuite coverage
- Basic schema validation checks + BACKWARD policy enforcement hooks

### v1
- Add Lake sink
- Add transform DSL (minimal)
- Add enrichment HTTP hook with circuit-breaker
- Add quotas + RBAC

### v2
- Add Cassandra source (as Datadog did) as a pluggable connector type. :contentReference[oaicite:9]{index=9}
- Cross-region replication patterns
- UI portal

---

## 9) Open questions / decisions
- Schema governance: where does migration SQL originate? (CI gate vs platform API)
- Transform DSL: JSON rules vs embedded WASM vs compiled plugins
- Tenant isolation: per-tenant Temporal namespace vs per-tenant task queue + authz
```

````markdown
# Technical Spec: Go (target “1.26”), Temporal-Orchestrated Multi-tenant CDC Platform (Standalone-testable)

## 0) Design constraints & interpretation
- **Language**: Go (targeting “1.26” per request). Implementation will be **Go-stdlib first** (net/http, context, encoding/json, sync, time, log/slog).
- **Workflow orchestration**: Temporal Go SDK workflows/activities. :contentReference[oaicite:10]{index=10}
- **Tests**: Temporal `testsuite.WorkflowTestSuite` + `TestWorkflowEnvironment` + `testify/suite` style to run all scenarios **standalone** (no Temporal server, no Kafka/Postgres). :contentReference[oaicite:11]{index=11}
- **No external dependencies at runtime for tests**: all connectors are simulated via in-memory fakes.
- **Avoid anonymous-function-heavy style**: prefer named functions/methods for clarity, testability, and traceability.

> Note: Temporal Go SDK and testify are external modules, but the tests run fully in-process without external services, aligned with Temporal’s testing-suite intent. :contentReference[oaicite:12]{index=12}

---

## 1) High-level architecture

### 1.1 Control plane (HTTP API)
- Provides CRUD for Tenants, Pipelines, Secrets refs, Policies
- Persists metadata in a `MetadataStore` interface
- Starts/Signals Temporal workflows for provisioning and lifecycle operations

### 1.2 Temporal workflows (orchestration plane)
Core workflows:
1. `ProvisionPipelineWorkflow`
2. `UpdatePipelineWorkflow`
3. `PausePipelineWorkflow`
4. `ResumePipelineWorkflow`
5. `DecommissionPipelineWorkflow`
6. `ValidateSchemaChangeWorkflow` (optional gating workflow)

Temporal chosen because it supports modular steps with retries/compensation and durable execution, mirroring Datadog’s approach to scaling provisioning. :contentReference[oaicite:13]{index=13}

### 1.3 Connector plane (pluggable)
We model Debezium/Kafka Connect/etc. as *interfaces*, not concrete dependencies.

Rationale: Debezium Postgres connector semantics include initial snapshot then streaming changes; we model those phases in our simulator. :contentReference[oaicite:14]{index=14}

---

## 2) Domain model

### 2.1 Tenant
```go
type TenantID string

type Tenant struct {
  ID   TenantID
  Name string
  Tier string // e.g. "standard", "enterprise"
}
````

### 2.2 PipelineSpec

```go
type PipelineID string

type SourceType string
const (
  SourcePostgres SourceType = "postgres"
)

type SinkType string
const (
  SinkSearch SinkType = "search"
  SinkLake   SinkType = "lake"
  SinkPostgres SinkType = "postgres"
)

type CompatibilityMode string
const (
  CompatBackward CompatibilityMode = "BACKWARD"
)

type PipelineSpec struct {
  TenantID  TenantID
  PipelineID PipelineID
  Name      string

  Source    SourceSpec
  Sink      SinkSpec

  Transform TransformSpec
  Enrichment EnrichmentSpec

  Schema SchemaSpec
  Ops    OpsSpec
}

type SourceSpec struct {
  Type SourceType
  SecretRef string
  Database string
  Schema   string
  Tables   []string
}

type SinkSpec struct {
  Type SinkType
  SecretRef string
  Target string // index/table/cluster logical name
}

type TransformSpec struct {
  Enabled bool
  Rules   []TransformRule
}

type TransformRule struct {
  Kind string // "filter", "project", "rename", "composite_key"
  Params map[string]string
}

type EnrichmentSpec struct {
  Enabled bool
  Endpoint string
  Timeout time.Duration
  FailureMode string // "drop"|"retry"|"dlq"
}

type SchemaSpec struct {
  Format string // "avro"|"protobuf"|"jsonschema"
  Compatibility CompatibilityMode // default BACKWARD
  Subject string
}

type OpsSpec struct {
  DesiredLag time.Duration
  MaxRetries int
  DLQEnabled bool
}
```

---

## 3) Interfaces (ports) to keep runtime/test isolation

### 3.1 Metadata store

```go
type MetadataStore interface {
  CreateTenant(ctx context.Context, t Tenant) error
  GetTenant(ctx context.Context, id TenantID) (Tenant, error)

  CreatePipeline(ctx context.Context, spec PipelineSpec) error
  GetPipeline(ctx context.Context, tenant TenantID, pipeline PipelineID) (PipelineSpec, error)
  UpdatePipeline(ctx context.Context, spec PipelineSpec) error

  SetPipelineState(ctx context.Context, tenant TenantID, pipeline PipelineID, state string) error
}
```

**Test impl**: `InMemoryMetadataStore` with `sync.RWMutex`.

### 3.2 Secret provider

```go
type SecretProvider interface {
  Resolve(ctx context.Context, secretRef string) (map[string]string, error)
}
```

**Test impl**: in-memory map.

### 3.3 Schema registry

We enforce compatibility rules at “register schema” time. Default BACKWARD. ([Confluent Docs][1])

```go
type SchemaRegistry interface {
  Register(ctx context.Context, subject string, schema string, mode CompatibilityMode) (schemaID int, err error)
  GetLatest(ctx context.Context, subject string) (schema string, id int, err error)
}
```

**Test impl**: `InMemorySchemaRegistry` implementing a simplified BACKWARD rule-set (enough for unit tests):

* allow adding optional fields
* reject renames/removals unless explicitly marked optional/compatible

### 3.4 CDC engine + transport + sink

We keep this abstract; production could be Debezium/Kafka Connect; tests use simulators.

```go
type CDCProvisioner interface {
  PrepareSource(ctx context.Context, spec SourceSpec) error // e.g., logical replication prerequisites
  StartCapture(ctx context.Context, spec SourceSpec) (CaptureHandle, error)
  StopCapture(ctx context.Context, h CaptureHandle) error
}

type CaptureHandle struct {
  ID string
}

type StreamProvisioner interface {
  EnsureStream(ctx context.Context, tenant TenantID, pipeline PipelineID, schemaSubject string) (streamID string, err error)
  DeleteStream(ctx context.Context, streamID string) error
}

type SinkProvisioner interface {
  EnsureSink(ctx context.Context, spec SinkSpec) (sinkID string, err error)
  DeleteSink(ctx context.Context, sinkID string) error
}
```

**Why this matches the article**: Datadog’s platform provisions a sequence of components (replication objects, Debezium, topics, schema registry, sink connectors). We model those steps explicitly as activity calls with compensation. ([Datadog][2])

---

## 4) Temporal workflows & activities

### 4.1 Workflow: ProvisionPipelineWorkflow

**Inputs**: `PipelineSpec`
**Output**: `ProvisionResult{Resources...}`

Steps (each step is an Activity with retries; state recorded in workflow):

1. `ValidatePipelineSpecActivity`
2. `ValidateSchemaPolicyActivity`
3. `EnsureSchemaSubjectActivity` (register initial schema; enforce BACKWARD) ([Confluent Docs][1])
4. `PrepareSourceActivity`
5. `EnsureStreamActivity`
6. `EnsureSinkActivity`
7. `StartCaptureActivity`
8. `MarkPipelineActiveActivity`

Compensation (on failure after resource creation):

* stop capture
* delete sink
* delete stream
* (schema subject usually retained; policy-dependent)

**Temporal implementation notes**

* Use `workflow.RetryPolicy` for activities.
* Use deterministic time via `workflow.Now(ctx)`.
* Use signals for pause/resume.
* Avoid goroutines/timers that violate determinism; use Temporal primitives (selectors, workflow.Sleep). ([Go Packages][3])

### 4.2 Workflow: Pause/Resume

* `PausePipelineWorkflow`: Signal running pipeline workflow or run state-transition workflow:

  * call `StopCaptureActivity`, set state `PAUSED`
* `ResumePipelineWorkflow`:

  * call `StartCaptureActivity`, set state `ACTIVE`

### 4.3 Workflow: Decommission

* Stop capture
* delete sink/stream
* mark `DECOMMISSIONED`
* write audit event

---

## 5) Control plane HTTP API (stdlib net/http)

### 5.1 Endpoints (v1)

* `POST /v1/tenants`
* `POST /v1/tenants/{tenant}/pipelines`
* `GET  /v1/tenants/{tenant}/pipelines/{pipeline}`
* `POST /v1/tenants/{tenant}/pipelines/{pipeline}:pause`
* `POST /v1/tenants/{tenant}/pipelines/{pipeline}:resume`
* `DELETE /v1/tenants/{tenant}/pipelines/{pipeline}` (decommission)

### 5.2 Request/response

* JSON via `encoding/json`
* Errors: problem+json style (minimal)

### 5.3 AuthZ

* Pluggable middleware interface:

```go
type Authorizer interface {
  Authorize(r *http.Request, tenant TenantID, action string) error
}
```

Test impl: allow-all.

---

## 6) Testing strategy (standalone)

Temporal explicitly supports unit and functional tests via `testsuite.WorkflowTestSuite` and `TestWorkflowEnvironment` without a Temporal server. ([Temporal Docs][4])

### 6.1 Test harness components

* `InMemoryMetadataStore`
* `InMemorySecretProvider`
* `InMemorySchemaRegistry`
* `FakeCDCProvisioner`
* `FakeStreamProvisioner`
* `FakeSinkProvisioner`
* `FakeEnrichmentService` (optional)

### 6.2 Scenario matrix (must all run offline)

1. **Happy path provisioning**

   * all activities succeed
   * verify: pipeline state ACTIVE, resources recorded

2. **Transient failure with retry**

   * e.g., `EnsureSinkActivity` fails twice then succeeds
   * verify: retries invoked; final ACTIVE

3. **Permanent failure with compensation**

   * `StartCaptureActivity` fails permanently
   * verify: sink deleted, stream deleted, state ERROR

4. **Schema incompatibility rejection**

   * register initial schema
   * attempt update that violates BACKWARD (rename/remove field)
   * verify: workflow fails before provisioning proceeds (or update workflow rejects)

5. **Pause/resume behavior**

   * provision then pause
   * verify: capture stopped, state PAUSED
   * resume
   * verify: capture started, state ACTIVE

6. **Decommission**

   * verify: stop capture + delete resources + state DECOMMISSIONED

7. **Enrichment failure modes**

   * drop/retry/dlq paths (in-memory DLQ list)

### 6.3 Temporal test patterns

* Register workflow + activities in test env
* Use `env.OnActivity(...).Return(...)` to simulate deterministic outcomes
* Assert calls and state transitions

---

## 7) Implementation sketch (package layout)

```
/cmd/controlplane         // main for HTTP API + Temporal client bootstrap
/internal/api             // handlers, routing, authz middleware
/internal/domain          // Tenant, PipelineSpec, validation
/internal/store           // in-memory + (future) sql store
/internal/temporal        // workflows + activities + task queue names
/internal/connectors      // interfaces + fakes
/internal/observability   // slog helpers, metrics interface
```

---

## 8) Operational concerns (production-ready, but testable)

### 8.1 Multi-tenancy in Temporal

Options:

* **Per-tenant Task Queue** (recommended v1): `replication.<tenantID>`
* Worker pool can shard by tier (enterprise vs standard)

### 8.2 Idempotency

All activities must be idempotent:

* `Ensure*` activities return existing resource IDs if already created
* `StartCapture` checks if capture already running

### 8.3 Asynchronous replication stance

Default asynchronous; replication lag is monitored and bounded by alerts. This matches the trade-off Datadog describes when choosing async replication for scalability/availability. ([Datadog][2])

### 8.4 Schema evolution plumbing

In real deployments, Debezium can emit schema updates and serialize events (often Avro) into Kafka + schema registry. Our platform integrates at the “registry enforcement” boundary; tests simulate registrations and compatibility checks. ([Debezium][5])

---

## 9) Deliverables (what “complete” means)

1. Go module compiling with `go test ./...` and all scenario tests passing offline
2. Temporal workflows + activities with testsuite coverage
3. HTTP control plane with in-memory backing store for local runs
4. Clear extension points for real connectors (Debezium/Kafka Connect/etc.) without changing workflow logic

---

## 10) Key references

* Datadog multi-tenant replication platform evolution + Temporal provisioning + schema compatibility themes ([Datadog][2])
* Temporal Go SDK + Workflow API ([Temporal Docs][6])
* Temporal Go testing suite (standalone workflow tests) ([Temporal Docs][4])
* Debezium Postgres connector behavior (snapshot then streaming changes) ([Debezium][5])
* Schema Registry BACKWARD compatibility semantics ([Confluent Docs][1])

```

If you want, I can also generate a concrete repo scaffold (directories + minimal compilable workflow/activity code + full testsuite cases) in a follow-up message—still keeping all tests fully in-memory and runnable offline.
::contentReference[oaicite:27]{index=27}
```

[1]: https://docs.confluent.io/platform/current/schema-registry/fundamentals/schema-evolution.html?utm_source=chatgpt.com "Schema Evolution and Compatibility for Schema Registry on Confluent ..."
[2]: https://www.datadoghq.com/blog/engineering/cdc-replication-search/ "Replication redefined: How we built a low-latency, multi-tenant data replication platform | Datadog"
[3]: https://pkg.go.dev/go.temporal.io/sdk/workflow?utm_source=chatgpt.com "workflow package - go.temporal.io/sdk/workflow - Go Packages"
[4]: https://docs.temporal.io/develop/go/testing-suite?utm_source=chatgpt.com "Testing - Go SDK | Temporal Platform Documentation"
[5]: https://debezium.io/documentation/reference/stable/connectors/postgresql.html?utm_source=chatgpt.com "Debezium connector for PostgreSQL"
[6]: https://docs.temporal.io/develop/go?utm_source=chatgpt.com "Go SDK developer guide | Temporal Platform Documentation"

