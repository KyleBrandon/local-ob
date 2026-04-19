# local-ob

Self-hosted personal knowledge brain. Go MCP server + Postgres (pgvector) + pluggable embedding and classification providers. Ships with Ollama (local, default) and OpenAI (remote, opt-in). Postgres and the MCP server run in Docker; Ollama runs native on the host for Metal acceleration and is reached from containers via `host.docker.internal`.

The authoritative design doc (with locked architecture decisions) lives at `~/obsidian/Arx/00-inbox/Local Open Brain — Implementation.md`. This README is the short path to a running stack.

## Prerequisites

```bash
brew install ollama go docker
brew services start ollama

# Optional:
brew install node        # only for the mcp-remote Claude Desktop shim
brew install mkcert      # only for locally-trusted HTTPS
```

`psql` is not required on the host — smoke tests go through `docker exec`.

Pull the Ollama models and keep them resident:

```bash
ollama pull nomic-embed-text      # 768-dim embeddings
ollama pull qwen3:8b              # classification
launchctl setenv OLLAMA_KEEP_ALIVE 24h
brew services restart ollama
```

## Layout

```
cmd/
  ob-mcp/          server entrypoint (tools + streamable HTTP + /health)
  ob-reembed/      utility for migrating existing rows to a new provider
internal/
  db/              pgxpool wrapper with pgvector type registration
  embed/           Ollama + OpenAI embedding providers, retry wrapper
  classify/        Ollama classify provider
schema.sql         pgvector schema + match_thoughts() SQL function
docker-compose.yml
Dockerfile
.env.example
```

## Setup

```bash
cp .env.example .env
# Edit POSTGRES_PASSWORD. Keep it in [A-Za-z0-9_\-.~!*]+ — it gets spliced into
# DATABASE_URL and URL-reserved characters (@ : / ? # % & +) will corrupt parsing.

docker compose up -d
docker compose logs -f ob-mcp
```

Expected log lines:

```
embed: ollama/nomic-embed-text (dim=768)
classify: ollama/qwen3:8b
HTTP MCP on :8080 (path /mcp, health /health)
```

Quick health check:

```bash
curl -fsS http://127.0.0.1:8080/health   # → ok
```

## Client configuration

### Claude Code (CLI)

Native HTTP transport — no shim:

```bash
claude mcp add --transport http --scope user open-brain-local http://localhost:8080/mcp
claude mcp list
```

### Claude Desktop

Claude Desktop's `claude_desktop_config.json` only accepts stdio servers; `"url"` entries are silently dropped. Use the `mcp-remote` stdio shim (requires Node):

```json
{
  "mcpServers": {
    "open-brain-local": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://localhost:8080/mcp"]
    }
  }
}
```

File location: `~/Library/Application Support/Claude/claude_desktop_config.json`. Quit and relaunch Claude Desktop after editing. Log to watch: `~/Library/Logs/Claude/main.log`.

## Smoke test

From any MCP client (Claude Code or Claude Desktop):

> Capture this thought: Tirzepatide trials showed ~20% body weight reduction at 15mg.

> Search my brain for weight loss research.

Verify directly in Postgres:

```bash
docker exec -it ob-local-postgres-1 psql -U postgres -d openbrain -c \
  "SELECT left(content,60), thought_type, topics, embed_provider, embed_model
   FROM thoughts ORDER BY created_at DESC LIMIT 5;"
```

## Optional: local HTTPS

Skip unless you're exposing the server on your LAN or registering it as a claude.ai web Custom Connector. Claude Desktop and Claude Code over loopback don't need TLS.

```bash
mkcert -install
mkdir -p certs
mkcert -cert-file certs/cert.pem -key-file certs/key.pem localhost 127.0.0.1 ::1
```

Uncomment in `.env`:

```
TLS_CERT_FILE=/certs/cert.pem
TLS_KEY_FILE=/certs/key.pem
```

`docker compose up -d --build ob-mcp`; the log line becomes `HTTPS MCP on :8080`. `certs/` is gitignored — regenerate on every host.

## Switching embedding providers

`.env` controls which provider is active. The startup safety gate refuses to boot if the configured provider/model disagrees with what's already in the database, pointing at the mismatch.

```bash
# 1. Edit .env — point EMBED_* at the new provider
# 2. Migrate existing rows
docker compose stop ob-mcp
docker compose run --rm --entrypoint /app/ob-reembed ob-mcp
# 3. Start the server — gate now passes
docker compose up -d ob-mcp
```

`ob-reembed` filters on `embed_provider != $1 OR embed_model != $2`, so a crashed run is safe to rerun.

Emergency override: `FORCE_PROVIDER_CHANGE=true` skips the gate. Only do this if you're about to re-embed anyway — new rows land in a different vector space than old ones, and cross-row cosine similarity becomes meaningless.

## Operations

```bash
# Backup
docker exec ob-local-postgres-1 pg_dump -U postgres openbrain \
  | gzip > "backups/openbrain-$(date +%F).sql.gz"

# Restore
gunzip -c backups/openbrain-<date>.sql.gz \
  | docker exec -i ob-local-postgres-1 psql -U postgres openbrain

# Rebuild after code change
docker compose up -d --build ob-mcp

# Teardown (keep data)
docker compose down

# Nuke everything including pgdata volume
docker compose down -v
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Startup fails with "dim mismatch" | `EMBED_DIMENSION` doesn't match schema `vector(N)` | Fix env or alter schema + re-embed |
| Startup fails with "provider/model mismatch" | Env changed vs existing data | Run `ob-reembed` or set `FORCE_PROVIDER_CHANGE=true` |
| Capture succeeds, topics/people empty | `qwen3:8b` returned non-JSON | Non-blocking — embedding quality is the real signal |
| First capture after idle is slow | Ollama unloaded the model | `launchctl setenv OLLAMA_KEEP_ALIVE 24h` |
| Container can't reach Ollama | `host.docker.internal` not resolving | `extra_hosts` block is present; Linux needs `host-gateway` |
| Search returns nothing | HNSW index cold or `ef_search` low | `SET hnsw.ef_search = 100;` in psql session |
| DB fails with odd host errors | Password with URL-reserved chars corrupts `DATABASE_URL` | Use `[A-Za-z0-9_\-.~!*]+` or percent-encode |
| Claude Desktop: `Skipped invalid MCP server config entries` | `claude_desktop_config.json` rejects `url` entries | Use the `mcp-remote` shim above |
