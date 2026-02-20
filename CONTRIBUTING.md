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
MCP_HTTP_MODE=http MCP_HTTP_PORT=18080 go run .
```

### Project structure

```
.
├── main.go              # Entry point
├── server.go            # MCP server, JSON-RPC dispatch
├── tools.go             # Tool definitions and descriptions
├── tool_calls.go        # Tool call handlers (file operations)
├── tool_results.go      # Tool result formatting
├── memory_store.go      # Memory CRUD + vector search
├── rag_engine.go        # RAG: document indexing, embeddings, search
├── vector_store.go      # SQLite-backed vector store
├── config.go            # Configuration from env vars / flags
├── paths.go             # Path guard (security: allowlisted paths)
├── resources.go         # MCP resources (file listing)
├── search.go            # Text search across files
├── readers.go           # File readers
├── logger.go            # File-based logging
├── stats.go             # Usage statistics
├── scripts/             # Shell scripts for testing and installation
└── system/macos/        # macOS launchd service files
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
