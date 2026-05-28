# Provenance Monorepo

This repository contains two related Provenance services:

- `services/python-pipeline/` - the original async Python provenance pipeline.
- `services/go-backend/` - the Go HTTP API, NATS JetStream workers, and Postgres backend.
- `docs/` - shared project specs, including the Go/NATS rewrite spec.

## Run the Go Backend

```bash
cd services/go-backend
cp .env.example .env
docker compose up --build
```

The API listens on `http://localhost:8080` by default. The Compose stack also starts Postgres and
NATS with JetStream enabled.

For local Go checks:

```bash
cd services/go-backend
go test ./...
```

## Run the Python Pipeline

```bash
cd services/python-pipeline
python -m venv .venv
pip install -e ".[dev]"
cp .env.example .env
python run_demo.py --headline "Prime Minister announces retirement"
```

See `services/python-pipeline/README.md` for the legacy pipeline details.