# Shared Service Guide

This guide turns `agent-memory-mcp` from a local repo tool into a shared HTTP service for a team.

## Target path

Use this progression:

1. `solo local`: one repo, stdio mode, local data under `.agent-memory/`
2. `team laptop`: same repo, but with auto-indexing and file watching
3. `shared service`: HTTP mode, auth token, reverse proxy, and a persistent Docker volume

## Prerequisites

- Docker and Docker Compose plugin
- A repo checkout on the host machine
- A long random bearer token for `/mcp`

Generate a token:

```bash
openssl rand -hex 32
```

## 1. Prepare shared config

From the repo root:

```bash
cd deploy/docker
cp .env.shared.example .env.shared
```

Set at least:

- `MCP_HTTP_HOST=0.0.0.0`
- `MCP_HTTP_AUTH_TOKEN`
- `MCP_PROJECT_ROOT`

Default shared recipe choices:

- `MCP_EMBEDDING_MODE=local-only`
- Ollama sidecar with `bge-m3`
- project mount is read-only
- runtime state is stored in Docker volumes

## 2. Start the shared service

```bash
cd deploy/docker
docker compose --env-file .env.shared up -d --build
```

What this starts:

- `agent-memory-mcp` in HTTP mode on port `18080`
- `ollama` for local embeddings
- `ollama-pull` to fetch `bge-m3`

## 3. Verify the service

Health check:

```bash
curl http://localhost:18080/health
```

Protected MCP endpoint:

```bash
curl -i \
  -H "Authorization: Bearer $MCP_HTTP_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  http://localhost:18080/mcp
```

Expected behavior:

- `/health` responds without auth
- `/mcp` requires `Authorization: Bearer <token>`
- startup fails if you bind a non-loopback host without `MCP_HTTP_AUTH_TOKEN`, unless you explicitly set `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true`

## 4. Put TLS in front of it

The built-in HTTP server is intentionally plain HTTP.

Use a reverse proxy for:

- TLS termination
- stable hostname
- connection limits and request logging

An nginx recipe is included at:

`deploy/nginx/agent-memory-mcp.conf`

Adapt these values before use:

- `server_name`
- certificate paths
- upstream host if you are not proxying from the same Docker network

## 5. MCP client connection

Point your remote-capable MCP client or proxy at:

```text
https://mcp.example.com/mcp
```

Use the bearer token configured in `MCP_HTTP_AUTH_TOKEN`.

## Auth notes

- for shared mode, set `MCP_HTTP_HOST=0.0.0.0` and keep `MCP_HTTP_AUTH_TOKEN` non-empty
- never expose `/mcp` publicly without both auth and TLS
- keep the token in secret management, not in git
- rotate the token if it was ever copied into shell history or logs

## Data and backups

The shared Docker recipe stores runtime state in persistent volumes:

- `memory-data`
- `ollama-models`

Backup guidance:

- see [Backup And Restore](BACKUP_RESTORE.md)
- for shared mode, back up the configured `MCP_DATA_PATH` or export memories before risky changes

## Recommended rollout

For a team rollout:

1. start with one repo and one team
2. keep `MCP_EMBEDDING_MODE=local-only` first
3. enable hosted embeddings only if you explicitly want that tradeoff
4. narrow `MCP_INDEX_DIRS` and `MCP_ALLOW_DIRS` before broadening scope
5. check `conflicts_report` and promote canonical knowledge as the team starts using the service
