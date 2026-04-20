# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Stack & common commands

Go 1.25 MCP server + Postgres/pgvector. Postgres and the server run in Docker; Ollama runs on the host and is reached from containers via `host.docker.internal:11434`. The Docker image is built via multi-stage Dockerfile — Go is **not** required on the host.

```bash
# Run / rebuild after code change
docker compose up -d --build ob-mcp
docker compose logs -f ob-mcp

# Host-side Go checks (optional — CI builds inside the image)
go build ./...
go vet ./...

# Re-embed all rows with the currently configured provider/model
docker compose stop ob-mcp
docker compose run --rm --entrypoint /app/ob-reembed ob-mcp
docker compose up -d ob-mcp

# Inspect data
docker exec -it ob-local-postgres-1 psql -U postgres -d openbrain
```

No test suite exists yet. Verification is by smoke test via an MCP client plus direct `psql` checks (see README "Smoke test").

## Architecture

Two binaries share `internal/`:

- `cmd/ob-mcp` — MCP server. Exposes `capture_thought` and `search_thoughts` over either stdio (default) or streamable HTTP (`-http :8080`, path `/ob-mcp`, liveness `/health`). On `capture_thought`, embedding and classification run **concurrently** via goroutines; classification failure is non-fatal (topics/people default to empty), embedding failure aborts the insert.
- `cmd/ob-reembed` — one-shot utility that re-embeds rows where `embed_provider` or `embed_model` disagrees with `.env`. Idempotent, safe to rerun after a crash. Batches of 64.

`internal/embed` and `internal/classify` are parallel provider-plugin packages — each has a `Provider` interface, a `Config` struct, and a `New()` factory that switches on `cfg.Provider`. Embedding ships with `ollama` and `openai` (the latter wrapped in `WithRetry` with exponential backoff + `NonRetryableError` short-circuit). Classification currently only supports `ollama`; adding a provider means dropping a file in `internal/classify/` and extending the switch in `classify.go`.

`internal/db` wraps `pgxpool` and **must** install the `pgxvec.RegisterTypes` hook in `AfterConnect` — without it, binding a `pgvector.Vector` parameter fails with an encoding error on any fresh connection. Both binaries do this; any new binary touching the DB needs the same hook.

`schema.sql` is mounted into the Postgres image at `/docker-entrypoint-initdb.d/` and only runs on a fresh `pgdata` volume. Schema changes to an existing volume must be applied manually via `psql` or by nuking the volume (`docker compose down -v`).

## Startup safety gate (`assertProviderAlignment`)

Before serving, `ob-mcp` compares the configured provider's dimension and identity against the database. Two fatal conditions:

1. **Dim mismatch** — `EMBED_DIMENSION` disagrees with the `vector(N)` declared in schema. Fix env or alter the column and re-embed.
2. **Provider/model mismatch** — `.env` names a different `(provider, model)` than the newest row. Fix by running `ob-reembed`, or bypass with `FORCE_PROVIDER_CHANGE=true` (only sensible if you're about to re-embed anyway — mixed vector spaces make cross-row cosine similarity meaningless).

An empty table passes the gate unconditionally.

## Things worth knowing before changing behavior

- `schema.sql` hardcodes `vector(768)`. If you change embedding dimension, update both the schema and `EMBED_DIMENSION`, then re-embed.
- HNSW search recall can be tuned at runtime with `SET hnsw.ef_search = 100;` — this is per-session, not a schema change.
- `POSTGRES_PASSWORD` is spliced into `DATABASE_URL`. Keep it in `[A-Za-z0-9_\-.~!*]+` or URL-reserved characters (`@ : / ? # % & +`) will corrupt parsing.
- TLS is off by default. The HTTPS path is only needed for LAN exposure or a claude.ai web Custom Connector — local Claude Code / Claude Desktop over loopback doesn't need it. See README "Enabling HTTPS".
- `certs/` is gitignored **and** `.dockerignored` — private keys never enter the image and must be regenerated per host.
- `host.docker.internal` resolves on macOS/Windows automatically; Linux needs the `extra_hosts: host-gateway` entry already in `docker-compose.yml`.
- Module path is `github.com/KyleBrandon/local-ob` (Go module) while the repo directory is `local-open-brain` — don't "fix" this mismatch.
