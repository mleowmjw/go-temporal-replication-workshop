# Canvas 3 — Section 3: Temporal + local observability

## Goal
Show where orchestration and observability plug into the system **after** the live CDC path is already proven.

## Audience promise
By the end of this section, attendees will understand:
- why Temporal belongs in the platform layer
- how to run Temporal locally with the CLI
- how to scrape local metrics with vmagent
- how logs can flow into VictoriaLogs

## Speaker script
Say:
1. The hard problem in production is not creating one connector.
2. The hard problem is provisioning, updating, pausing, resuming, and observing many connectors safely.
3. Temporal gives us orchestration.
4. VictoriaMetrics and VictoriaLogs give us a lightweight local observability path.

## Files
### `03-platform/temporal/start-temporal.sh`
```bash
#!/usr/bin/env bash
set -euo pipefail

mkdir -p .temporal

temporal server start-dev \
  --db-filename .temporal/temporal.db
```

### `03-platform/compose.yml`
```yaml
services:
  victoriametrics:
    image: victoriametrics/victoria-metrics:v1.114.0
    container_name: ws-victoriametrics
    ports:
      - "8428:8428"
    command:
      - --storageDataPath=/storage

  vmagent:
    image: victoriametrics/vmagent:v1.114.0
    container_name: ws-vmagent
    depends_on:
      - victoriametrics
    ports:
      - "8429:8429"
    command:
      - -promscrape.config=/etc/vmagent/vmagent.yml
      - -remoteWrite.url=http://victoriametrics:8428/api/v1/write
    volumes:
      - ./observability/vmagent.yml:/etc/vmagent/vmagent.yml:ro

  victorialogs:
    image: victoriametrics/victoria-logs:v1.25.0
    container_name: ws-victorialogs
    ports:
      - "9428:9428"
    command:
      - --storageDataPath=/vlogs

  vector:
    image: timberio/vector:0.38.0-alpine
    container_name: ws-vector
    depends_on:
      - victorialogs
    volumes:
      - ./observability/vector.yaml:/etc/vector/vector.yaml:ro
      - /var/run/docker.sock:/var/run/docker.sock
```

### `03-platform/observability/vmagent.yml`
```yaml
global:
  scrape_interval: 5s

scrape_configs:
  - job_name: temporal-dev-server
    static_configs:
      - targets:
          - host.docker.internal:8077

  - job_name: temporal-go-worker
    static_configs:
      - targets:
          - host.docker.internal:9090

  - job_name: vmagent
    static_configs:
      - targets:
          - vmagent:8429
```

### `03-platform/observability/vector.yaml`
```yaml
sources:
  docker_logs:
    type: docker_logs

transforms:
  enrich_logs:
    type: remap
    inputs:
      - docker_logs
    source: |
      .service = .label."com.docker.compose.service" ?? "unknown"
      .container_name = .container_name ?? "unknown"
      .timestamp = .timestamp ?? now()

sinks:
  victorialogs:
    type: elasticsearch
    inputs:
      - enrich_logs
    endpoints:
      - http://victorialogs:9428/insert/elasticsearch/
    api_version: v8
    mode: bulk
    compression: gzip
    healthcheck:
      enabled: false
    query:
      _msg_field: message
      _time_field: timestamp
      _stream_fields: service,container_name
```

## Commands to show live
```bash
cd 03-platform
./temporal/start-temporal.sh
```

In another shell:
```bash
docker compose up -d
```

Useful checks:
```bash
temporal operator namespace list
temporal workflow list
curl http://localhost:8428
curl http://localhost:9428
```

## Success check
- Temporal dev server starts locally
- vmagent starts and can scrape configured targets
- VictoriaMetrics is reachable on `:8428`
- VictoriaLogs is reachable on `:9428`

## What not to do in this section
- do not let observability debugging consume the workshop
- do not expand into full dashboards live unless time remains
- do not move this section ahead of the working CDC demo

## Closing line
“We solved the live path first; now the orchestration and observability pieces have a clear place instead of feeling like extra infrastructure.”
