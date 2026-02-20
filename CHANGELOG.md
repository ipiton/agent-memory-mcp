# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2025-02-20

### Added

- MCP server with stdio and HTTP transport
- Memory system with 4 types: episodic, semantic, procedural, working
- Semantic memory search via vector embeddings
- RAG document indexing and search
- Jina AI embeddings (primary) with Ollama fallback
- SQLite storage for memory and vector index
- Auto-indexing with file watcher
- macOS launchd service support
- PathGuard for secure file access
