# Provenance

Go backend for provenance analysis and narrative intelligence.

The project runs as a single Go service backed by Postgres and NATS JetStream:

- `services/go-backend/` - HTTP API, provenance pipeline workers, campaign/narrative engine, providers, migrations, and scheduler.
- `public/` - DataScope frontend served by the Go backend.
- `docs/` - architecture and product specs.

## Run

```bash
cp services/go-backend/.env.example services/go-backend/.env
docker compose up --build
```

The API and frontend listen on `http://localhost:8080`. The Compose stack also starts Postgres and NATS with JetStream enabled.

## Local Checks

```bash
cd services/go-backend
go test ./...
```

## Main APIs

- `POST /v1/reports` submits a headline, claim, URL, or text for provenance analysis.
- `GET /v1/jobs/{job_id}` polls provenance pipeline status.
- `GET /v1/reports/{report_id}` returns the final provenance report.
- `POST /v1/campaigns` and related `/v1/campaigns/*` routes manage narrative intelligence campaigns.