# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Opinionated solo-local setup with one recommended `.agent-memory/` data layout
- Automatic `.env` loading from the current project directory
- `MCP_HTTP_HOST` and `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED` for explicit HTTP exposure control
- `local-only` embedding mode that disables hosted providers and keeps embedding traffic on the local Ollama endpoint
- `reembed` CLI command for migrating stored memories to the active embedding model
- `config` CLI command for generating ready MCP client config snippets
- Memory stats grouped by embedding model in CLI and MCP responses
- `make local-smoke` and a documented smoke path for first-run validation
- Recommended workflow snippets for start-of-session recall, end-of-session consolidation, infra changes, and incident mode
- `MCP_INDEX_EXCLUDE_DIRS` and `MCP_INDEX_EXCLUDE_GLOBS` for safer RAG indexing
- `MCP_REDACT_SECRETS=true` for redacting common secret-like content before indexing
- Threat model and backup/restore documentation
- Source-aware ingestion for docs, ADRs, RFCs, changelogs, runbooks, postmortems, CI configs, Helm, Terraform, and K8s files
- Hybrid retrieval that combines semantic similarity with keyword/BM25-like scoring, recency, and source-aware weighting
- Explainable retrieval with opt-in debug output for filters, ranking signals, score breakdowns, and applied boosts
- DevOps-first MCP tools for decisions, incidents, runbooks, postmortems, runbook search, incident recall, and project context summaries
- Trust metadata on memory and document retrieval results: `source_type`, `confidence`, `last_verified_at`, `owner`, and `freshness_score`
- Manual memory consolidation tools: `merge_duplicates`, `mark_outdated`, `promote_to_canonical`, and `conflicts_report`
- Explicit canonical knowledge layer with `list_canonical_knowledge` and `recall_canonical_knowledge`
- Shared service packaging: fixed Docker Compose recipe, shared env template, nginx reverse proxy recipe, and dedicated shared deployment guide
- Built-in retrieval console at `/console` with structured debug API for documents, raw memory, and canonical knowledge
- `analyze_session` MCP tool and `internal/sessionclose` orchestration layer for dry-run session analysis, raw summary capture, and candidate consolidation planning
- `review_session_changes` and `accept_session_changes` MCP tools for explainable review and explicit application of session consolidation plans
- Explicit `close_session` MCP alias plus CLI commands `close-session`, `review-session`, and `accept-session` for end-of-session workflow overrides
- Background session tracking with idle/shutdown auto-close, periodic raw checkpoints, and persisted review queue items
- Optional `notifications/session_event` support for `task_done`, `final_summary`, `checkpoint`, and `reset` boundaries in background session tracking
- `resolve_review_item` MCP tool and `resolve-review-item` CLI command for clearing pending review queue items without deleting the audit trail

### Changed

- README now positions the project as a memory, docs, and repo context layer for engineering agents
- Quick start now focuses on local-first time-to-value and a clear upgrade path to shared service mode
- MCP client setup now uses a project-local config generator for Claude Desktop, Cursor, and Codex
- README now documents the HTTP auth story and safer indexing controls for shared deployments
- Search now exposes `source_type`, supports source-type filtering, and applies basic source-aware ranking hints
- RAG search now ranks results with a hybrid scorer instead of cosine similarity alone
- CLI `search` and MCP `semantic_search` now support debug mode for explainable retrieval output
- README now documents task-oriented engineering workflow tools and how they fit incident/debugging workflows
- Semantic recall no longer mixes memories from different embedding spaces
- RAG search now refuses to query an index built with a different embedding model and asks for re-indexing instead
- Retrieval now uses trust/freshness signals in addition to semantic, keyword, recency, and source-aware ranking
- CLI `search` / `recall` and MCP `semantic_search` / `recall_memory` now show trust summaries in human-readable output
- Workflow-oriented store tools now stamp `last_verified_at` metadata on new entries
- Canonical, outdated, merged, and archived memory states now affect trust-aware recall ranking
- README now documents the consolidation workflow and conflict-reporting tools
- Project context summaries now surface canonical knowledge before raw memory when canonical entries exist
- Trust summaries now expose the knowledge layer: `raw`, `canonical`, or `document`
- Shared-service docs now describe the explicit path `solo local -> team laptop -> shared service`
- Docker and HTTP docs now point to the real MCP endpoint `/mcp` and the correct container port `18080`
- HTTP mode now includes a browser console for retrieval inspection and normal-vs-debug comparison
- HTTP mode now binds to `127.0.0.1` by default, and non-loopback binds require `MCP_HTTP_AUTH_TOKEN` unless you explicitly opt into unsafe unauthenticated access
- Memory recall now snapshots cached state before query embedding/scoring, reducing lock contention during concurrent writes
- RAG indexing now marks runs as `dirty` before chunk changes and atomically commits `indexed_files` plus index metadata back to `ready` at the end of a successful run
- CLI and MCP memory flows now share the same validation and trust-summary formatting rules for types, tags, query/content limits, and zero `verified` timestamps
- RAG search now generates semantic and keyword candidate sets separately and reranks only the merged candidate set instead of rescoring the full chunk corpus on every query
- Memory writes now use copy-on-write cache replacement and a dedicated write path instead of holding the global store lock across embedding and SQL work
- `merge_duplicates` now persists the primary record and archived duplicates inside one storage transaction before publishing cache updates
- Store-level normalization now owns type/tag/title/metadata shaping, so CLI, MCP, and import paths share one memory validation contract
- Recall and document search now keep only bounded top-K heaps for ranked results instead of sorting full candidate sets on every query
- Freshness scoring, title rendering, and keyword-matching helpers now live in shared packages instead of drifting across retrieval paths
- Startup config validation now rejects invalid chunk overlap settings and any `MCP_ALLOW_DIRS` entry that escapes `MCP_ROOT`
- Session consolidation planning now tags actions as `safe_auto_apply`, `soft_review`, or `hard_review` and avoids extracting project knowledge from summaries with insufficient engineering signal
- Write-enabled session analysis can now auto-apply low-risk near-exact updates while leaving higher-risk consolidation proposals in `review_required`
- Session analysis reports now include explicit summary counts, review summaries, and next actions such as `accept_all`, `review_changes`, and `save_raw_only`
- Session analysis now infers `coding/incident/migration/research/cleanup` mode from explicit input or summary signals, exposes the chosen mode in reports, and keeps incident/migration consolidation in stricter review-first policy
- Added structured project bank views for `canonical_overview`, `decisions`, `runbooks`, `incidents`, `caveats`, `migrations`, and `review_queue` with shared filters across MCP and CLI
- README and generated client config snippets now document the project-bank-plus-close-session rhythm, including coding/incident/migration close recipes and raw-only fallback
- README and `.env.example` now document the background session-maintenance policy and the session-tracking configuration knobs
- Active `review_queue` views now hide already resolved inbox items while keeping the underlying memories for audit purposes

### Fixed

- Prevented silent cross-provider embedding mismatch that could return confident but incorrect semantic matches
- Local-only mode no longer falls back to hosted embedding providers when Ollama is unavailable
- Final tracking-state persistence errors no longer leave document indexing in an undetected half-updated state; the next run forces a rebuild when recovery is needed
- CLI and MCP no longer drift on invalid memory types, tag normalization, or `verified=0001-01-01T00:00:00Z` in trust output
- Explicit `importance=0` is now preserved end-to-end instead of being rewritten to the default importance during store/update flows
- Invalid hosted batch embeddings are now rejected and retried through fallback providers before they can poison memory or RAG vector stores
- Misconfigured chunk settings can no longer trigger an infinite chunking loop during indexing
- `repo_*` allowlists can no longer point outside the configured project root through absolute or parent-relative paths

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
