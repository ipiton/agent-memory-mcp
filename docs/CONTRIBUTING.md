# Contributing to agent-memory-mcp

Thanks for your interest in contributing! This document covers the process for contributing to this project.

## Getting started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/agent-memory-mcp.git`
3. Create a branch: `git checkout -b my-feature`
4. Make your changes
5. Run tests: `go test ./...`
6. Push and open a Pull Request

## Development setup

### Prerequisites

- Go 1.26+
- One of:
  - [Jina AI API key](https://jina.ai/) for embedding tests
  - [Ollama](https://ollama.ai/) with `bge-m3` model

### Build and test

```bash
# Build
make build

# Run tests
make test

# Run locally in stdio mode
make run

# Run in HTTP mode
MCP_HTTP_MODE=http MCP_HTTP_PORT=18080 go run ./cmd/agent-memory-mcp
```

### Project structure

```
.
├── cmd/agent-memory-mcp/      # Binary entry point + CLI subcommands
│   ├── main.go                # Subcommand dispatch (serve, store, search, hooks…)
│   ├── cli.go                 # Memory/RAG/session CLI handlers
│   ├── hooks_cli.go           # Claude Code hook commands (context-inject, auto-capture, checkpoint)
│   ├── hooks_config.go        # `hooks-config` JSON generator
│   ├── setup.go               # `setup` — auto-merges hooks into ~/.claude/settings.json
│   ├── session_close.go       # close/review/accept-session CLI
│   ├── project_bank.go        # project-bank views CLI
│   ├── sediment_cli.go        # sediment-cycle / promote / demote CLI
│   ├── dead_end_cli.go        # mark-dead-end / dead-ends-stale CLI
│   ├── index_triples_cli.go   # T50 knowledge-graph triple backfill
│   └── lifecycle_cli.go       # sweep-archive / end-task (T47)
├── internal/
│   ├── config/                # Env loading, dotenv chain, hot-reload watcher
│   ├── server/                # MCP JSON-RPC server, tools_registry, HTTP handlers
│   ├── memory/                # Memory CRUD, types, sediment metadata
│   ├── rag/                   # RAG engine: indexing, chunking, hybrid search
│   ├── vectorstore/           # SQLite-backed vector index
│   ├── embedder/              # Jina/OpenAI/Ollama providers + embedder cache
│   ├── reranker/              # Optional neural reranker (Jina cross-encoder)
│   ├── search/                # Keyword/BM25 indexer
│   ├── topk/                  # Hybrid top-K candidate merge
│   ├── scoring/               # Trust/recency/source-aware scoring
│   ├── trust/                 # Trust metadata: confidence/freshness/owner
│   ├── steward/               # Stewardship: duplicates, drift, verification, inbox
│   ├── sessionclose/          # close-session pipeline (extract/plan/apply)
│   ├── review/                # Review queue
│   ├── hooks/                 # Hook dedup logic (jaccard window)
│   ├── lifecycle/             # Task-archive sweep (T47)
│   ├── paths/                 # PathGuard — allowlisted paths
│   ├── stats/                 # Usage stats jsonl
│   ├── logger/                # File-based diagnostics logger
│   └── userio/                # Stdio framing (line / lsp)
├── deploy/                    # Docker, nginx, systemd unit, env shared template
├── docs/                      # Reference docs (this folder)
├── scripts/                   # Helper shell scripts (install, smoke, release)
└── Makefile                   # Build, test, lint, smoke, quality-gates
```

## How to contribute

### Reporting bugs

Open an issue using the **Bug report** template. Include:

- What you expected to happen
- What actually happened
- Steps to reproduce
- Your environment (OS, Go version, MCP client)

### Suggesting features

Open an issue using the **Feature request** template. Describe:

- The problem you're trying to solve
- Your proposed solution
- Any alternatives you've considered

### Pull requests

- Keep PRs focused on a single change
- Write clear commit messages
- Add tests for new functionality
- Update documentation if behavior changes
- Make sure `go test ./...` and `go vet ./...` pass

## Code style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use meaningful variable names
- Keep functions focused and small
- Error messages should be lowercase, no trailing punctuation
- No emojis in programmatic output (tool responses, logs)
- All user-facing strings in English

## Testing

```bash
# Unit tests
go test ./...
```

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
