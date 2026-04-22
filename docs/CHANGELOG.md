# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Wave 1 hygiene** — T45 checkpoint-hook content-similarity filter (`MCP_CHECKPOINT_DEDUP_*`, Jaccard dedup over session-checkpoint records); T47 task-lifecycle archive sweep (`sweep-archive` / `end-task` CLI + MCP tools, `MCP_TASK_ARCHIVE_ROOTS`).
- **Wave 2 quality** — T43 RAG eval suite with baseline regression gate (`//go:build eval`, `make eval`, `docs/RAG_EVAL.md`); T46 dead-end tracking (`EngineeringTypeDeadEnd`, `store_dead_end` MCP tool, `mark-dead-end` CLI, retrieval boost + blend on pitfall keywords via `scoring.IsPitfallQuery`); T44 Jina v2 neural reranker (`internal/reranker`, `MCP_RERANK_*`, `JINA_RERANKER_MODEL`, 5s timeout with graceful fallback, `docs/RERANKER_LOCAL.md`).
- **Wave 3 architecture** — T48 memory sedimentation (`sediment_layer` column, `promote_sediment` / `demote_sediment` / `sediment_cycle` MCP tools, `sediment-cycle` CLI, `project_bank_view(view=sediment_candidates)`, `MCP_SEDIMENT_ENABLED`, `docs/SEDIMENTATION.md`).

## [0.6.0] - 2026-04-07

### Added

- **Claude Code hooks integration** — automatic session capture with zero manual effort
  - `setup` command auto-configures hooks in `~/.claude/settings.json` during `brew install`
  - `SessionStart` hook injects recent knowledge and pending raw summaries for agent-driven compilation
  - `SessionEnd` hook auto-captures session knowledge via the extract/plan/apply pipeline
  - `PreCompact` hook saves a checkpoint before context window compression
- `hooks-config` CLI command for manual hooks JSON generation
- `context-inject` CLI command — outputs knowledge context and uncompiled session summaries with compilation instructions
- `auto-capture` CLI command — full session consolidation pipeline from stdin transcript
- `checkpoint` CLI command — saves raw session checkpoints with configurable boundary type
- **Embedding-based contradiction scanner** in stewardship — detects semantically similar memories with conflicting signals (lifecycle status, temporal markers, content patterns)
- New steward scope `semantic_conflicts` and action kind `flag_contradiction`
- Pre-compact event support in session tracker (`pre_compact` notification)
- Version injection via ldflags — released binaries report actual version instead of "dev"

### Changed

- Homebrew formula runs `setup` automatically in `post_install` — hooks configured on install
- Homebrew caveats updated to reflect automatic hooks configuration
- `forceCheckpoint` now accepts boundary parameter for pre-compact vs regular checkpoints

## [0.4.1] - 2026-03-25

### Fixed

- RAG re-indexing now deletes old chunks by document path before upserting, preventing stale chunks from remaining when chunk count changes
- MCP `resources/templates/list` returns empty list instead of "method not found" error

## [0.3.0] - 2026-03-07

### Added

- Opinionated solo-local setup with one recommended `.agent-memory/` data layout
- Automatic `.env` loading from the current project directory
- `local-only` embedding mode (`MCP_EMBEDDING_MODE=local-only`) that keeps all embedding traffic on local Ollama
- `reembed` CLI command for migrating stored memories to the active embedding model
- `config` CLI command for generating ready MCP client config snippets (Claude Desktop, Cursor, Codex)
- Memory stats grouped by embedding model so you can see what needs re-embedding
- `make local-smoke` for quick first-run validation
- `MCP_INDEX_EXCLUDE_DIRS`, `MCP_INDEX_EXCLUDE_GLOBS`, and `MCP_REDACT_SECRETS` for safer RAG indexing
- Source-aware ingestion: docs, ADRs, RFCs, changelogs, runbooks, postmortems, CI configs, Helm, Terraform, and K8s files are classified and searchable by type
- Hybrid retrieval: semantic similarity combined with keyword/BM25 scoring, recency, and source-aware weighting
- Explainable retrieval: opt-in `debug` mode shows filters, ranking signals, score breakdowns, and applied boosts
- DevOps-first MCP tools: `store_decision`, `store_incident`, `store_runbook`, `store_postmortem`, `search_runbooks`, `recall_similar_incidents`, `summarize_project_context`
- Trust metadata on retrieval results: `source_type`, `confidence`, `last_verified_at`, `owner`, `freshness_score`
- Memory consolidation: `merge_duplicates`, `mark_outdated`, `promote_to_canonical`, `conflicts_report`
- Canonical knowledge layer with `list_canonical_knowledge` and `recall_canonical_knowledge`
- Shared service packaging: Docker Compose, shared env template, nginx reverse proxy, and deployment guide
- Built-in retrieval console at `/console` for inspecting search results and ranking in the browser
- End-of-session workflow: `close_session`, `review_session_changes`, `accept_session_changes` (MCP tools and CLI commands)
- Background session tracking with idle/shutdown auto-close, periodic raw checkpoints, and review queue
- `resolve_review_item` for clearing pending review queue items without deleting the audit trail
- `project_bank_view` for structured views: canonical knowledge, decisions, runbooks, incidents, caveats, migrations, review queue
- Auto-detection of embedding model mismatch with background re-embed on startup
- `MCP_HTTP_HOST` and `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED` for explicit HTTP exposure control
- Threat model and backup/restore documentation

### Changed

- Memory store now reads from SQLite directly and keeps only vector index data in RAM — significantly lower memory usage for large memory banks
- Quick start focuses on local-first time-to-value with a clear upgrade path to shared service mode
- MCP client setup uses a project-local config generator instead of manual JSON editing
- Semantic recall no longer mixes memories from different embedding spaces — avoids confident but incorrect matches across providers
- RAG search refuses to query an index built with a different embedding model and asks for re-indexing instead
- Search results include trust summaries in both CLI and MCP output
- HTTP mode binds to `127.0.0.1` by default; non-loopback binds require `MCP_HTTP_AUTH_TOKEN`
- `/health` endpoint now requires auth in shared HTTP mode
- POST endpoints now enforce `Content-Type: application/json`
- CLI and MCP now share the same validation rules for memory types, tags, and query/content limits
- Startup rejects invalid chunk overlap settings and `MCP_ALLOW_DIRS` entries that escape `MCP_ROOT`
- Interrupted RAG indexing runs are now detected and recovered automatically on the next rebuild
- Session consolidation tags actions as `safe_auto_apply`, `soft_review`, or `hard_review` with mode-aware review policy
- Session analysis auto-infers mode (`coding`/`incident`/`migration`/`research`/`cleanup`) and keeps incident/migration consolidation in stricter review-first policy
- Project context summaries surface canonical knowledge before raw memory when canonical entries exist

### Fixed

- Memory store partial writes no longer leave data in an inconsistent state
- Prevented silent cross-provider embedding mismatch that could return incorrect semantic matches
- Local-only mode no longer falls back to hosted embedding providers when Ollama is unavailable
- Explicit `importance=0` is now preserved instead of being rewritten to the default importance
- Invalid hosted batch embeddings are now rejected before they can corrupt memory or RAG vector stores
- Misconfigured chunk settings can no longer trigger an infinite chunking loop during indexing
- `repo_*` allowlists can no longer point outside the configured project root

## [0.2.1] - 2026-02-23

### Fixed

- Security hardening: data race fixes, code quality improvements

### Changed

- Updated installation options and examples in README

## [0.2.0] - 2026-02-23

### Added

- CLI subcommands architecture
- GoReleaser build pipeline
- Homebrew tap support

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
