# Installation

Install, configure, and run agent-memory-mcp locally or as a shared service, and connect MCP clients to it. The fastest path is `agent-memory-mcp setup <agent> --yes` (see the [README](../README.md#connect-your-agent)); the manual configuration below is the fallback.

## Start Local In 3 Minutes

The recommended path is: run locally first, prove value on one repo, then expand.

Run these commands from your project root.

### Prerequisites

Install the binary with one of these options:

```bash
# Homebrew (macOS/Linux) — recommended, auto-configures Claude Code hooks
brew tap ipiton/tap
brew install agent-memory-mcp
```

```bash
# go install
go install github.com/ipiton/agent-memory-mcp/cmd/agent-memory-mcp@latest
```

Then configure one embedding provider:

- [Jina AI API key](https://jina.ai/) for the quickest hosted setup
- [OpenAI API key](https://platform.openai.com/) or another OpenAI-compatible endpoint
- [Ollama](https://ollama.ai/) with `bge-m3` for a local setup

### 1. Configure local mode

```bash
cp .env.example .env
# Edit .env:
# - keep the solo-local defaults unless you need to change them
# - enable at least one embedding provider
#   JINA_API_KEY, OPENAI_API_KEY, or OLLAMA_BASE_URL
```

The binary auto-loads `.env` from the current directory, so you do not need `source .env`.

The recommended solo-local preset keeps all runtime state inside one directory:

```text
.agent-memory/
  rag-index/
  memory-store/
  logs/
```

## Local-Only Mode

Use local-only mode when you want embeddings without sending text to hosted APIs.

```bash
cp .env.example .env
# Then set:
# MCP_EMBEDDING_MODE=local-only
# JINA_API_KEY=
# OPENAI_API_KEY=
```

In `local-only` mode:

- `agent-memory-mcp` never calls Jina AI
- `agent-memory-mcp` never calls OpenAI-compatible embedding APIs
- embeddings are generated only through a local backend: Ollama or llama.cpp

What still uses the network:

- the local Ollama HTTP endpoint, typically `http://localhost:11434`
- or a local llama.cpp server, typically `http://127.0.0.1:8080/v1`

If no local backend is running or no supported local model is available, embedding requests fail with a local-only specific error telling you to start the backend or disable `MCP_EMBEDDING_MODE=local-only`.

### Alternative local backend: llama.cpp

If you already run `llama.cpp` (Apple Silicon native, GGUF models), point the server at its OpenAI-compatible `/v1/embeddings` endpoint instead of installing Ollama. It is opt-in — set `LLAMACPP_BASE_URL` to enable it. Once set it joins the fallback chain before Ollama (`Jina → OpenAI → llama.cpp → Ollama`) and works in `local-only` mode.

```bash
# Start llama.cpp with an embedding model
llama-server -m bge-m3.gguf --embedding --pooling cls -c 8192 -ub 8192

# Then configure the MCP server
LLAMACPP_BASE_URL=http://127.0.0.1:8080/v1
LLAMACPP_EMBEDDING_MODEL=bge-m3
MCP_EMBEDDING_MODE=local-only
```

llama.cpp returns the model's native embedding dimension, so make sure `MCP_EMBEDDING_DIMENSION` matches it (1024 for bge-m3) — a mismatch is rejected at recall time.

On slow self-hosted hardware (Ollama with `bge-m3` on a low-core or ARM VPS), a single chunk can take 4-7 seconds to embed and the default 5s timeout will fire repeatedly. Raise the limits:

```bash
MCP_EMBEDDING_TIMEOUT=30s      # default 5s
MCP_EMBEDDING_MAX_RETRIES=3    # default 1
```

Invalid values fall back to the defaults, so the service still starts.

#### Concurrency tuning when auto-index / file watcher is on

A single-slot `llama-server` processes requests strictly serially. With `MCP_RAG_AUTO_INDEX` / `MCP_RAG_FILE_WATCHER` enabled, background reindex batches (50 chunks each) hold the only slot for tens of seconds, so interactive `recall` / `semantic_search` / `index_documents` queue behind them and hit `context deadline exceeded` — the server looks "degraded" even though throughput is fine. Give the embedding server parallel slots so interactive calls slip in alongside the batch:

```bash
llama-server -m bge-m3.gguf --embedding --pooling cls \
  -c 32768 -b 8192 -ub 8192 \   # 8192 ctx PER SLOT (see note) — fits the largest chunk
  -np 4 -cb \                   # 4 slots + continuous batching: interactive calls don't wait for the batch
  --metrics                     # exposes Prometheus /metrics; /slots shows live slot occupancy
```

> **`-np` splits the context.** Per-slot context is `ctx_size / n_parallel`. bge-m3 is an encoder — every chunk must fit in one slot whole, and `-b`/`-ub` must be ≥ the largest chunk in tokens, or it fails with "input too large to process". So with `-np 4` you need `-c 32768` to keep 8192 per slot; **do not** lower `-c`, `-b`, or `-ub` below the single-slot value when adding slots.

Measured effect (Apple Silicon, bge-m3 Q8_0): a 50-input batch drops from ~50s to ~5s, and an interactive probe under batch load drops from 8–20s to ~0.03s.

Also smooth the reindex avalanche for large, frequently-edited files (whole-file re-chunk on every edit can re-trigger mid-cycle):

```bash
MCP_RAG_DEBOUNCE=2m         # default 30s — collapses bursts of edits into one reindex
MCP_RAG_WATCH_INTERVAL=5m   # periodic full-scan cadence
```

### 2. Start the local server

For MCP clients such as Claude Desktop, Cursor, or Codex:

```bash
agent-memory-mcp
```

For direct CLI use, the same binary already works without an MCP client:

```bash
agent-memory-mcp store -content "Ingress rollback uses previous Helm revision" -type procedural -tags "helm,rollback"
agent-memory-mcp recall "helm rollback"
agent-memory-mcp stats
```

### 3. Run a smoke check

```bash
agent-memory-mcp store -content "Solo local smoke check" -type working -tags "smoke,local"
agent-memory-mcp recall "solo local smoke"
agent-memory-mcp index
agent-memory-mcp search "agent memory"
```

If you are working from the source checkout, you can run the same flow with:

```bash
make local-smoke
```

## Index Your Repo In 2 Commands

Once local mode is running against a project, index docs and search them:

```bash
agent-memory-mcp index
agent-memory-mcp search "recent ingress change"
```

Typical high-value sources include:

- `docs/`
- `README.md`
- `CHANGELOG.md`
- RFC / ADR folders
- runbooks and incident notes

## Turn It Into A Team Service Later

When local mode proves useful, move in three steps:

1. solo local
2. team laptop with auto-indexing and file watching
3. shared service with HTTP mode, auth token, and reverse proxy

Fastest shared-service path:

```bash
cd deploy/docker
cp .env.shared.example .env.shared
# edit MCP_HTTP_AUTH_TOKEN and MCP_PROJECT_ROOT
docker compose --env-file .env.shared up -d --build
```

This keeps the same retrieval stack, but packages it for team use.

Reference docs:

- [Shared Service Guide](SHARED_SERVICE.md)
- [Security Policy](SECURITY.md)
- [Backup And Restore](BACKUP_RESTORE.md)

## Installation Options

### Homebrew (recommended for macOS)

```bash
brew tap ipiton/tap
brew install agent-memory-mcp
brew services start agent-memory-mcp
```

This installs the binary, creates a default config, and starts the service on `127.0.0.1:18080` with memory enabled. RAG document search is disabled by default — enable it by editing the config:

```bash
# Edit config
nano $(brew --prefix)/etc/agent-memory-mcp/config.env
```

Set `MCP_RAG_ENABLED=true`, `MCP_ROOT=/path/to/your/project`, and `MCP_INDEX_DIRS=docs,README.md`. Changes are picked up automatically within ~30 seconds, or force reload with `kill -HUP $(pgrep agent-memory-mcp)`.

Manage the service:

```bash
brew services restart agent-memory-mcp
brew services stop agent-memory-mcp
brew services info agent-memory-mcp
```

If you previously installed via Cask and want `brew services`:

```bash
brew uninstall --cask agent-memory-mcp
brew install ipiton/tap/agent-memory-mcp
```

### Download a binary

Download a prebuilt archive from the [Releases](https://github.com/ipiton/agent-memory-mcp/releases) page.

The release archives include the version in their filename, so resolve the latest tag first:

```bash
# Resolve latest version once
VERSION=$(curl -fsSL https://api.github.com/repos/ipiton/agent-memory-mcp/releases/latest \
  | grep '"tag_name"' | head -1 | cut -d'"' -f4 | sed 's/^v//')

# macOS (Apple Silicon)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-darwin-arm64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/

# macOS (Intel)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-darwin-amd64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/

# Linux (x86_64)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-linux-amd64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/

# Linux (arm64)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-linux-arm64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/
```

### Build from source

```bash
git clone https://github.com/ipiton/agent-memory-mcp.git
cd agent-memory-mcp
go build -o bin/agent-memory-mcp ./cmd/agent-memory-mcp
```

### Docker

```bash
docker build -f deploy/docker/Dockerfile -t agent-memory-mcp .
docker run -p 18080:18080 \
  -v memory-data:/data \
  -e MCP_HTTP_MODE=http \
  -e MCP_HTTP_HOST=0.0.0.0 \
  -e MCP_HTTP_AUTH_TOKEN=replace-with-long-random-token \
  agent-memory-mcp
```

Or with docker compose:

```bash
cd deploy/docker
cp .env.shared.example .env.shared
docker compose --env-file .env.shared up -d --build
```

The MCP HTTP endpoint will be available at `http://localhost:18080/mcp`.

By default, bare-metal HTTP mode now binds to `127.0.0.1`. For shared/container deployments, set `MCP_HTTP_HOST=0.0.0.0` and a bearer token.
## Connect your agent

Run this from the repository the agent should remember:

| Agent | Command | Writes to |
|---|---|---|
| Claude Code | `agent-memory-mcp setup claude-code --yes` | `~/.claude.json` |
| Codex | `agent-memory-mcp setup codex --yes` | `~/.codex/config.toml` |
| Cursor | `agent-memory-mcp setup cursor --yes` | `~/.cursor/mcp.json` |
| Gemini CLI | `agent-memory-mcp setup gemini --yes` | `~/.gemini/settings.json` |
| OpenCode | `agent-memory-mcp setup opencode --yes` | `~/.config/opencode/opencode.json` |
| VS Code | `agent-memory-mcp setup vscode --yes` | user `mcp.json` |
| Windsurf | `agent-memory-mcp setup windsurf --yes` | `~/.codeium/windsurf/mcp_config.json` |

Without `--yes` the command is a dry run and prints the entry it would add:

```text
$ agent-memory-mcp setup claude-code
Claude Code: would create the file with server "memory" in /Users/you/.claude.json
+ {
+   "args": [
+     "-lc",
+     "cd '/path/to/repo' && MCP_HTTP_MODE=stdio exec '/opt/homebrew/bin/agent-memory-mcp'"
+   ],
+   "command": "/bin/sh",
+   "type": "stdio"
+ }
Dry run: nothing written. Re-run with --yes to apply.
```

The existing config is kept as it is, other servers and keys included, and a timestamped backup is written next to it. Running the command again changes nothing. If a server with the same name already launches something else, the command stops; `--force` replaces only its launch command. Other flags: `--name`, `--root`, `--command`. Restart the agent afterwards.

For Claude Code, `agent-memory-mcp setup` (no agent name) also installs the session hooks that capture knowledge automatically; see [Hooks](HOOKS.md).

## MCP client configuration

Use the built-in generator to produce a project-local config that starts the server from your repo root.

This is the recommended path because it:

- keeps `.env` loading working without duplicating settings into every MCP client
- keeps `.agent-memory/` relative to the project root
- gives you one copy-paste snippet per client

You can override the detected project root or binary path with `-root` and `-command`.

### Claude Desktop

Paste into `~/Library/Application Support/Claude/claude_desktop_config.json`:

```bash
agent-memory-mcp config claude-desktop
```

Example generated output:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/bin/sh",
      "args": [
        "-lc",
        "cd '/path/to/your/project' && exec '/absolute/path/to/agent-memory-mcp'"
      ]
    }
  }
}
```

### Cursor

Paste into `~/.cursor/mcp.json`:

```bash
agent-memory-mcp config cursor
```

Example generated output:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/bin/sh",
      "args": [
        "-lc",
        "cd '/path/to/your/project' && exec '/absolute/path/to/agent-memory-mcp'"
      ]
    }
  }
}
```

### Codex

Paste into `~/.codex/config.toml`:

```bash
agent-memory-mcp config codex
```

Example generated output:

```toml
[mcp_servers.memory]
command = "/bin/sh"
args = ["-lc", "cd '/path/to/your/project' && exec '/absolute/path/to/agent-memory-mcp'"]
```

### Rename the server or override paths

```bash
agent-memory-mcp config claude-desktop \
  -name engineering-memory \
  -root /path/to/your/project \
  -command /absolute/path/to/agent-memory-mcp
```
### HTTP mode (Docker, remote server, shared instance)

Start the server in HTTP mode:

```bash
# Standalone
MCP_HTTP_MODE=http \
MCP_HTTP_HOST=127.0.0.1 \
MCP_HTTP_PORT=18080 \
MCP_HTTP_AUTH_TOKEN=replace-with-long-random-token \
agent-memory-mcp

# Or with Docker
cd deploy/docker
docker compose --env-file .env.shared up -d --build
```

Then point your HTTP-capable MCP client or proxy at:

```text
http://localhost:18080/mcp
```

The `/mcp` endpoint supports the MCP Streamable HTTP transport: JSON-RPC requests go over `POST`, and clients that need a server-push channel (Cursor and similar) open it with `GET` and `Accept: text/event-stream`. The server keeps that stream alive with periodic keepalive comments. A plain `GET` without the SSE `Accept` header still returns `405`.

```bash
curl -N -H "Accept: text/event-stream" \
  -H "Authorization: Bearer $MCP_HTTP_AUTH_TOKEN" \
  http://localhost:18080/mcp
```

For retrieval inspection in a browser, open:

```text
http://localhost:18080/console
```

The console is a lightweight UI for:

- running document, raw-memory, and canonical-knowledge queries
- comparing normal vs debug mode for document retrieval
- inspecting source types, trust/freshness, and score breakdowns

In shared mode, the page itself is static, but live queries from the console still require the same bearer token as `/mcp`.

For shared HTTP mode:

- default bare-metal bind is `MCP_HTTP_HOST=127.0.0.1`; this is the safe local default
- for shared/container deployments set `MCP_HTTP_HOST=0.0.0.0`
- set `MCP_HTTP_AUTH_TOKEN` to require `Authorization: Bearer <token>` on `/mcp`
- startup now fails on non-loopback binds without `MCP_HTTP_AUTH_TOKEN`, unless you explicitly set `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true`
- keep `/health` for load balancer or container health checks
- terminate TLS at a reverse proxy or load balancer
- do not expose the service directly on the public internet without auth and TLS
- use [deploy/nginx/agent-memory-mcp.conf](../deploy/nginx/agent-memory-mcp.conf) as the starting reverse proxy recipe
- use [docs/SHARED_SERVICE.md](SHARED_SERVICE.md) for the full `local -> team laptop -> shared service` path
## macOS service installation

### Homebrew (recommended)

```bash
brew install ipiton/tap/agent-memory-mcp
brew services start agent-memory-mcp
```

See [Installation Options](#installation-options) for details and config location.

### Manual (legacy)

```bash
./scripts/install-macos.sh
```

This builds the binary, creates a `.env` file, and installs a launchd service that auto-starts on login.

Manual control:

```bash
# Status
launchctl list | grep com.agent-memory-mcp

# Start
launchctl load ~/Library/LaunchAgents/com.agent-memory-mcp.plist

# Stop
launchctl unload ~/Library/LaunchAgents/com.agent-memory-mcp.plist
```
