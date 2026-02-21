# agent-memory-mcp

An MCP (Model Context Protocol) server that gives AI agents persistent memory with semantic search. Agents can store, recall, and search memories using vector embeddings, plus optionally index project documents for RAG-powered retrieval.

## Features

- **Persistent memory** with 4 types: episodic (events), semantic (facts), procedural (patterns), working (context)
- **Semantic search** via vector embeddings (Jina AI, OpenAI, or Ollama)
- **RAG document indexing** for project docs, changelogs, and archives
- **Dual transport**: stdio (for MCP clients) and HTTP (for APIs)
- **Auto-indexing** with file watcher for document changes
- **SQLite storage** for both memory and vector index -- no external databases needed

## Installation

### Option 1: Homebrew (macOS/Linux)

```bash
brew tap ipiton/tap
brew install --cask agent-memory-mcp
```

### Option 2: go install

```bash
go install github.com/ipiton/agent-memory-mcp/cmd/agent-memory-mcp@latest
```

### Option 3: Download binary

Download a prebuilt archive from the [Releases](https://github.com/ipiton/agent-memory-mcp/releases) page.

```bash
# macOS (Apple Silicon)
curl -L https://github.com/ipiton/agent-memory-mcp/releases/latest/download/agent-memory-mcp-0.2.0-darwin-arm64.tar.gz | tar xz
mv agent-memory-mcp /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/ipiton/agent-memory-mcp/releases/latest/download/agent-memory-mcp-0.2.0-darwin-amd64.tar.gz | tar xz
mv agent-memory-mcp /usr/local/bin/

# Linux (x86_64)
curl -L https://github.com/ipiton/agent-memory-mcp/releases/latest/download/agent-memory-mcp-0.2.0-linux-amd64.tar.gz | tar xz
sudo mv agent-memory-mcp /usr/local/bin/
```

### Option 4: Build from source

```bash
git clone https://github.com/ipiton/agent-memory-mcp.git
cd agent-memory-mcp
go build -o bin/agent-memory-mcp ./cmd/agent-memory-mcp
```

### Option 5: Docker

```bash
docker build -t agent-memory-mcp .
docker run -p 18080:8080 \
  -v memory-data:/data \
  -e JINA_API_KEY=your-key \
  agent-memory-mcp
```

Or with docker compose:

```bash
# Set your API key
export JINA_API_KEY=your-key

# Start
docker compose up -d
```

The service will be available at `http://localhost:18080`.

## Quick start

### Prerequisites

- Go 1.26+ (for building from source)
- One of:
  - [Jina AI API key](https://jina.ai/) (recommended, multilingual)
  - [OpenAI API key](https://platform.openai.com/) or any OpenAI-compatible API (Together AI, Mistral, etc.)
  - [Ollama](https://ollama.ai/) running locally with `bge-m3` model (free, local)

### Configure

```bash
cp .env.example .env
# Edit .env -- set at least one embedding provider:
#   JINA_API_KEY, OPENAI_API_KEY, or OLLAMA_BASE_URL
```

### Run

**Stdio mode** (for MCP clients like Claude Desktop, Cursor):

```bash
agent-memory-mcp
```

**HTTP mode** (for API access):

```bash
MCP_HTTP_MODE=http MCP_HTTP_PORT=18080 agent-memory-mcp
```

> The server listens on plain HTTP. For TLS, use a reverse proxy (nginx, Caddy, Traefik) or cloud load balancer in front of it.

### CLI mode

The binary also works as a standalone CLI -- no MCP client needed:

```bash
# Memory operations
agent-memory-mcp store -content "Project uses chi router" -type procedural -tags "go,chi"
agent-memory-mcp recall "router middleware"
agent-memory-mcp list -type procedural
agent-memory-mcp delete <memory-id>

# RAG search
agent-memory-mcp search "authentication flow"
agent-memory-mcp index

# Utilities
agent-memory-mcp stats
agent-memory-mcp export > backup.json
agent-memory-mcp import backup.json

# JSON output for scripting
agent-memory-mcp recall "test" -json
agent-memory-mcp stats -json
```

Run `agent-memory-mcp <command> -help` for details on any command.

When no command is given (or flags start with `-`), the binary starts the MCP server as before -- full backward compatibility.

## MCP client configuration

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/path/to/agent-memory-mcp",
      "env": {
        "MCP_ROOT": "/path/to/your/project",
        "MCP_MEMORY_ENABLED": "true",
        "MCP_RAG_ENABLED": "true",
        "JINA_API_KEY": "your-key-here"
      }
    }
  }
}
```

### Cursor

Add to `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/path/to/agent-memory-mcp",
      "env": {
        "MCP_ROOT": "/path/to/your/project",
        "MCP_MEMORY_ENABLED": "true",
        "JINA_API_KEY": "your-key-here"
      }
    }
  }
}
```

### Claude Code

Add to `.claude/settings.json`:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/path/to/agent-memory-mcp",
      "env": {
        "MCP_ROOT": "/path/to/your/project",
        "MCP_MEMORY_ENABLED": "true",
        "JINA_API_KEY": "your-key-here"
      }
    }
  }
}
```

### HTTP mode (Docker, remote server, shared instance)

Start the server in HTTP mode:

```bash
# Standalone
MCP_HTTP_MODE=http MCP_HTTP_PORT=18080 agent-memory-mcp

# Or with Docker
docker compose up -d
```

Then configure your MCP client to connect via HTTP. For example, with Claude Desktop using [mcp-remote](https://github.com/geelen/mcp-remote):

```json
{
  "mcpServers": {
    "memory": {
      "command": "npx",
      "args": ["mcp-remote", "http://localhost:18080/rpc"]
    }
  }
}
```

## CLI commands

| Command | Description |
|---------|-------------|
| `serve` | Start MCP server (stdio/http) -- default when no command given |
| `store` | Store a memory (`-content`, `-title`, `-type`, `-tags`, `-context`, `-importance`, `-stdin`) |
| `recall` | Semantic search in memories (positional query, `-type`, `-tags`, `-limit`, `-json`) |
| `list` | List memories (`-type`, `-context`, `-limit`, `-json`) |
| `delete` | Delete a memory by ID (positional) |
| `search` | RAG semantic search (positional query, `-doc-type`, `-category`, `-limit`, `-json`) |
| `index` | Re-index documents for RAG |
| `stats` | Show memory statistics (`-json`) |
| `export` | Export all memories to JSON (`-o` file, default stdout) |
| `import` | Import memories from JSON (positional file or stdin) |

## MCP tools reference

### Memory tools

| Tool | Description |
|------|-------------|
| `store_memory` | Store a memory with content, type, tags, and importance |
| `recall_memory` | Recall memories by semantic query with optional filters |
| `update_memory` | Update an existing memory by ID |
| `delete_memory` | Delete a memory by ID |
| `list_memories` | List all memories with optional type/context filtering |
| `memory_stats` | Get memory statistics (counts by type) |

### RAG tools

| Tool | Description |
|------|-------------|
| `semantic_search` | Semantic search across indexed documents |
| `find_similar_tasks` | Find similar tasks from archive |
| `get_relevant_docs` | Get relevant documentation by topic |
| `index_documents` | Re-index documents for RAG search |

### File tools

| Tool | Description |
|------|-------------|
| `repo_list` | List files and folders under allowlisted paths |
| `repo_read` | Read a file from allowlisted paths |
| `repo_search` | Text search across allowlisted paths |

## Configuration

All configuration is via environment variables. See [`.env.example`](.env.example) for the full list.

### Key variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_ROOT` | Current dir | Project root path |
| `MCP_MEMORY_ENABLED` | `true` | Enable memory tools |
| `MCP_RAG_ENABLED` | `true` | Enable RAG/search tools |
| `MCP_HTTP_MODE` | `stdio` | Transport: `stdio` or `http` |
| `MCP_HTTP_PORT` | `18080` | HTTP port (when in HTTP mode) |
| `JINA_API_KEY` | - | Jina AI API key for embeddings |
| `OPENAI_API_KEY` | - | OpenAI API key (or compatible: Together, Mistral) |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `OPENAI_EMBEDDING_MODEL` | `text-embedding-3-small` | Embedding model name |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama URL (local fallback) |
| `MCP_EMBEDDING_DIMENSION` | `1024` | Vector dimension (change requires re-indexing) |
| `MCP_INDEX_DIRS` | `docs` | Directories to index for RAG |
| `MCP_DATA_PATH` | `data` | Base path for data storage |

### Data paths

The server creates these directories under `MCP_DATA_PATH`:

- `rag-index/` -- SQLite vector index for document search
- `memory-store/` -- SQLite database for agent memories

## Architecture

```
┌─────────────────────────────────────────┐
│           MCP Protocol Layer            │
│         (stdio or HTTP/JSON-RPC)        │
├─────────────┬───────────┬───────────────┤
│ Memory Tools│ RAG Tools │  File Tools   │
├─────────────┼───────────┼───────────────┤
│MemoryStore  │ RAGEngine │  PathGuard    │
│  (SQLite)   │           │               │
│             │┌─────────┐│               │
│  Embedder◄──┤│DocService││               │
│             ││VecService││               │
│             │└─────────┘│               │
│             │  (SQLite)  │               │
└─────────────┴───────────┴───────────────┘
```

### Embedding providers

The server supports three embedding providers with automatic fallback:

1. **Jina AI** (primary) -- `jina-embeddings-v3`, 1024 dimensions, multilingual
2. **OpenAI** (fallback) -- `text-embedding-3-small`, 1024 dimensions, or any OpenAI-compatible API
3. **Ollama** (local fallback) -- `bge-m3`, 1024 dimensions, runs locally for free

By default, all providers produce 1024-dimensional vectors and are interchangeable. You can increase the dimension via `MCP_EMBEDDING_DIMENSION` for higher accuracy (e.g. `3072` with `text-embedding-3-large`). The server validates dimension consistency on startup -- if you change it, you'll need to re-index with `index_documents`.

If a provider fails or is not configured, the server automatically falls back to the next one.

## macOS service installation

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

## Development

```bash
# Build
make build

# Run
make run

# Test
make test
```

## License

MIT
