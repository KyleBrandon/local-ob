# local-ob

Self-hosted personal knowledge brain. Go MCP server + Postgres (pgvector) + pluggable embedding and classification providers. Ships with Ollama (local, default) and OpenAI (remote, opt-in). Postgres and the MCP server run in Docker; Ollama runs native on the host for Metal/CUDA acceleration and is reached from containers via `host.docker.internal`.

## What you need

- **Docker** (engine + compose) — Postgres and the MCP server both run in containers. macOS and Windows run Linux containers inside the VM Docker Desktop ships; on Linux the engine runs natively, no desktop app required.
- **Ollama** — runs on the host (not in Docker) so it can use Metal/CUDA. Containers reach it via `host.docker.internal:11434`.

That's it for the core stack. `psql` and Go are not required on the host (smoke tests use `docker exec`; Go builds run inside the image via multi-stage Dockerfile).

## Install

Follow the section for your OS, then continue to [Pull the models](#pull-the-models).

### macOS

- Install [Docker Desktop](https://www.docker.com/products/docker-desktop/). Launch it once so the engine starts (`open -a Docker`).
- Install Ollama:
  ```bash
  brew install ollama
  brew services start ollama
  ```
- Keep models resident so first-capture latency stays low:
  ```bash
  launchctl setenv OLLAMA_KEEP_ALIVE 24h
  brew services restart ollama
  ```

### Linux

- Install Docker Engine for your distro — follow [docs.docker.com/engine/install](https://docs.docker.com/engine/install/). Enable non-root use (`sudo usermod -aG docker $USER`, then re-login).
- Install Ollama:
  ```bash
  curl -fsSL https://ollama.com/install.sh | sh
  sudo systemctl enable --now ollama
  ```
- Keep models resident — add `Environment=OLLAMA_KEEP_ALIVE=24h` to a systemd drop-in:
  ```bash
  sudo systemctl edit ollama
  sudo systemctl restart ollama
  ```

### Windows

- Install [Docker Desktop](https://www.docker.com/products/docker-desktop/) with the WSL2 backend. Launch it once so the engine starts.
- Install [Ollama for Windows](https://ollama.com/download/windows).
- Keep models resident — set the `OLLAMA_KEEP_ALIVE` user environment variable to `24h`, then restart the Ollama tray app.

## Pull the models

```bash
ollama pull nomic-embed-text      # 768-dim embeddings
ollama pull qwen3:8b              # classification
```

## Run the stack

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
HTTP MCP on :8080 (path /ob-mcp, health /health)
```

Quick health check:

```bash
curl -fsS http://127.0.0.1:8080/health   # → ok
```

Note: a plain GET on `/ob-mcp` will hang — that's the MCP streaming endpoint waiting for server-sent events from a real client. Use `/health` for liveness checks.

## Connect a client

### Claude Code (CLI)

Native HTTP transport — no shim:

```bash
claude mcp add --transport http --scope user open-brain-local http://localhost:8080/ob-mcp
claude mcp list
```

### Claude Desktop

Claude Desktop's `claude_desktop_config.json` only accepts stdio servers; `"url"` entries are silently dropped. Bridge to the HTTP server with the [`mcp-remote`](https://www.npmjs.com/package/mcp-remote) shim. It runs via `npx`, which needs [Node](https://nodejs.org/) — install that first if you don't have it.

```json
{
  "mcpServers": {
    "open-brain-local": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://localhost:8080/ob-mcp"]
    }
  }
}
```

Config file location:

- **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

Quit and relaunch Claude Desktop after editing. Log to watch: `~/Library/Logs/Claude/main.log` (macOS) or `%APPDATA%\Claude\logs\main.log` (Windows).

## Smoke test

From any MCP client:

> Capture this thought: Miller's 1956 paper claimed short-term memory holds 7±2 chunks; Cowan's 2001 review revised that estimate down to about 4.

> Search my brain for working memory capacity.

Verify directly in Postgres:

```bash
docker exec -it ob-local-postgres-1 psql -U postgres -d openbrain -c \
  "SELECT left(content,60), thought_type, topics, embed_provider, embed_model
   FROM thoughts ORDER BY created_at DESC LIMIT 5;"
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

---

## Enabling HTTPS (optional)

Skip this entire section unless you're exposing the server on your LAN or registering it as a [claude.ai web Custom Connector](https://support.anthropic.com/en/articles/11175166-getting-started-with-custom-connectors-using-remote-mcp). Claude Code and Claude Desktop running locally over loopback do not need TLS.

Assumes the HTTP setup above is working.

1. Install [mkcert](https://github.com/FiloSottile/mkcert#installation) (macOS: `brew install mkcert`) and trust its local CA:

   ```bash
   mkcert -install
   ```

2. Generate a cert scoped to loopback:

   ```bash
   mkdir -p certs
   mkcert -cert-file certs/cert.pem -key-file certs/key.pem localhost 127.0.0.1 ::1
   ```

3. Uncomment the TLS block in `.env`:

   ```
   TLS_CERT_FILE=/certs/cert.pem
   TLS_KEY_FILE=/certs/key.pem
   ```

4. Rebuild:

   ```bash
   docker compose up -d --build ob-mcp
   docker compose logs --tail 5 ob-mcp    # now shows "HTTPS MCP on :8080"
   curl -fsS https://localhost:8080/health
   ```

5. Update clients:
   - Claude Code: `claude mcp remove open-brain-local && claude mcp add --transport http --scope user open-brain-local https://localhost:8080/ob-mcp`
   - Claude Desktop: change the `mcp-remote` URL to `https://...` and add `"env": {"NODE_EXTRA_CA_CERTS": "<mkcert -CAROOT>/rootCA.pem"}` — Node doesn't use the OS trust store.

`certs/` is gitignored and `.dockerignored` — the key never ships in the image and has to be regenerated per host.

---

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

| Symptom                                                     | Cause                                                    | Fix                                                        |
| ----------------------------------------------------------- | -------------------------------------------------------- | ---------------------------------------------------------- |
| `curl http://127.0.0.1:8080/ob-mcp` hangs                   | It's the SSE streaming endpoint; a plain GET waits       | Use `/health` for liveness; `/ob-mcp` is for MCP clients   |
| Startup fails with "dim mismatch"                           | `EMBED_DIMENSION` doesn't match schema `vector(N)`       | Fix env or alter schema + re-embed                         |
| Startup fails with "provider/model mismatch"                | Env changed vs existing data                             | Run `ob-reembed` or set `FORCE_PROVIDER_CHANGE=true`       |
| Capture succeeds, topics/people empty                       | `qwen3:8b` returned non-JSON                             | Non-blocking — embedding quality is the real signal        |
| First capture after idle is slow                            | Ollama unloaded the model                                | Set `OLLAMA_KEEP_ALIVE=24h` per the Pull-the-models step   |
| Container can't reach Ollama                                | `host.docker.internal` not resolving                     | `extra_hosts` block is present; Linux needs `host-gateway` |
| Search returns nothing                                      | HNSW index cold or `ef_search` low                       | `SET hnsw.ef_search = 100;` in psql session                |
| DB fails with odd host errors                               | Password with URL-reserved chars corrupts `DATABASE_URL` | Use `[A-Za-z0-9_\-.~!*]+` or percent-encode                |
| Claude Desktop: `Skipped invalid MCP server config entries` | `claude_desktop_config.json` rejects `url` entries       | Use the `mcp-remote` shim above                            |
