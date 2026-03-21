# =============================================================================
# Blue-Green Workshop — Dockerfile
# Build context: repository root (where go.mod lives)
# Usage:
#   docker compose -f cmd/blue-green/compose.workshop.yml up
# =============================================================================

# ── Stage 0: Temporal dev server ─────────────────────────────────────────────
# Uses the official install script (pre-built binary, no compile step).
# debian:bookworm-slim is required — Alpine uses musl libc and the pre-built
# temporal binary targets glibc.
FROM debian:bookworm-slim AS temporal-dev
RUN apt-get update && \
    apt-get install -y --no-install-recommends curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*
RUN curl -sSf https://temporal.download/cli.sh | sh
ENV PATH="/root/.temporalio/bin:${PATH}"
EXPOSE 7233 8233 8077
ENTRYPOINT ["temporal"]
CMD ["server", "start-dev", \
     "--ip", "0.0.0.0", \
     "--port", "7233", \
     "--ui-port", "8233", \
     "--metrics-port", "8077", \
     "--db-filename", "/tmp/temporal.db"]

# ── Stage 1: Download Go module dependencies (cached layer) ─────────────────
FROM golang:1.26-alpine AS deps
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: Build the workshop binary ───────────────────────────────────────
FROM deps AS build
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/blue-green ./cmd/blue-green

# ── Stage 3: Test runner (docker compose run --rm test) ──────────────────────
FROM deps AS test
COPY . .
CMD ["go", "test", "-race", "-v", "-count=1", "./internal/bluegreen/..."]

# ── Stage 4: Minimal runtime image ───────────────────────────────────────────
FROM alpine:3.21 AS runtime
COPY --from=build /bin/blue-green /bin/blue-green
EXPOSE 8083
ENTRYPOINT ["/bin/blue-green"]
