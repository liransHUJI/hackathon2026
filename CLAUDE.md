# Claude Code Instructions — Provenance

This repository is now Go-only.

## Project Summary

Provenance is a Go service that provides:

1. A headline/claim provenance pipeline exposed through `/v1/reports`.
2. NATS JetStream workers for normalization, semantic expansion, search, enrichment, ranking, AI detection, expert review, and final report generation.
3. A campaign/narrative intelligence backend with Postgres persistence and scheduled live-provider crawling.
4. The DataScope frontend served directly by the Go HTTP server.

## Core Rules

- Do not add Python pipeline code back to this repository.
- Keep pipeline business logic in Go under `services/go-backend/internal/`.
- Use typed Go structs from `internal/models` for pipeline and API contracts.
- Preserve the provider registry pattern in `internal/providers`; pipeline stages should depend on provider interfaces, not concrete provider types.
- Keep Bright Data budget guards intact.
- External calls must be bounded by context-aware HTTP clients and should use configured limits where available.
- Run `gofmt` on changed Go files.
- Run `go test ./...` from `services/go-backend` after backend changes.

## Runtime Shape

```text
DataScope UI / API client
  -> Go HTTP API
  -> NATS JetStream provenance subjects
  -> Go pipeline workers
  -> Postgres reports/jobs
```

Campaign discovery runs in the same Go binary through `internal/engine` and `internal/scheduler`.

## Important Paths

- `services/go-backend/cmd/provenance-api/main.go` - application startup and worker wiring.
- `services/go-backend/internal/api/server.go` - HTTP routes.
- `services/go-backend/internal/pipeline/` - provenance pipeline stages.
- `services/go-backend/internal/providers/` - source provider interfaces and implementations.
- `services/go-backend/internal/models/models.go` - API, pipeline, and campaign contracts.
- `services/go-backend/migrations/` - Postgres schema.
- `public/` - frontend assets served by Go.
