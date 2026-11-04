# Configuration

Environment variables, retrieval behaviour, and operational controls.

## Configuration

All configuration is via environment variables. See [`.env.example`](../.env.example) for the full list.

Config files are loaded in this order (each file only fills in values not already set):

1. `--config /path/to/file` (explicit path, skips chain; accepted by every command, not just `serve`)
2. `.env` in the current directory
3. `~/.config/agent-memory-mcp/config.env` (XDG)
4. `$(brew --prefix)/etc/agent-memory-mcp/config.env` (Homebrew)

For solo local mode, copy `.env.example` to `.env` in your project root. For `brew services`, the config is auto-created at `$(brew --prefix)/etc/agent-memory-mcp/config.env`.

### Hot-reload

When running as a service (HTTP mode), the config file is watched for changes every 30 seconds. RAG-related settings (index dirs, embedding keys, enabled/disabled) are applied without restart. HTTP settings (port, host) require a full restart.

You can also force an immediate reload:

```bash
kill -HUP $(pgrep agent-memory-mcp)
```

### Key variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_ROOT` | Current dir | Project root path |
| `MCP_ALLOW_DIRS` | `""` (only `MCP_ROOT`) | Comma-separated extra repo-relative paths the file tools (`repo_list`, `repo_read`, `repo_search`) may read. Paths must stay under `MCP_ROOT`; absolute paths or `..` traversal are rejected at config load. **Critical for shared/HTTP mode** — keep narrow |
| `MCP_MAX_FILE_BYTES` | `2097152` | Max file size (bytes) `repo_read` will return; larger files are rejected |
| `MCP_MAX_SEARCH_RESULTS` | `200` | Hard cap for `repo_search` result count |
| `MCP_MAX_DEPTH` | `3` | Max directory recursion depth for `repo_list` |
| `MCP_STDIO_MODE` | `line` | Stdio framing: `line` (newline-delimited) or `lsp` (Content-Length headers) |
| `MCP_TOOL_PROFILE` | `full` | `core` offers and accepts 17 tools chosen from real usage: search, memory, typed memory and session review. A call to anything else is refused on the server, not only hidden from `tools/list`. `full` is every tool. The Claude Code plugin sets `core` |
| `MCP_TOOL_GROUPING` | `false` | Collapse the core toolset into grouped meta-tools on `tools/list` to cut the discovery schema payload (~42% smaller, 41→8 tools). `tools/call` accepts both grouped (`memory`+`action`) and legacy names regardless. With the `core` profile it is 17→6 tools, 14→9 KB. The Claude Code plugin turns it on. See [Tool grouping mode](MCP_TOOLS.md#tool-grouping-mode-token-efficiency) |
| `MCP_COMPILE_BACKGROUND` | `false` | `context-inject` stops asking the session's model to process pending summaries, and `compile --detach` does it with a headless `claude`. The Claude Code plugin turns it on. See [Background compilation](HOOKS.md#background-compilation) |
| `MCP_COMPILE_MODEL` | `haiku` | Model for `compile` |
| `MCP_COMPILE_CLAUDE_BIN` | `claude` | The `claude` executable for `compile`, for hooks whose `PATH` does not have it |
| `MCP_MEMORY_ENABLED` | `true` | Enable memory tools |
| `MCP_MEMORY_PREVIEW_RUNES` | `0` | Override the per-surface truncation cap (rune-based) for memory content/summary fields in MCP tool responses (`recall_memory`, `list_memories`, `search_runbooks`, …). `0` keeps the built-in caps (150/220/300); a positive value forces that single cap on all surfaces; a negative value disables truncation (full text). |
| `MCP_RAG_ENABLED` | `true` | Enable RAG/search tools (Homebrew service preset overrides this to `false` until you set `MCP_ROOT`) |
| `MCP_HTTP_MODE` | `stdio` | Transport: `stdio` or `http` |
| `MCP_HTTP_HOST` | `127.0.0.1` | HTTP bind host; set `0.0.0.0` for shared/container deployments |
| `MCP_HTTP_PORT` | `18080` | HTTP port (when in HTTP mode) |
| `MCP_HTTP_AUTH_TOKEN` | - | Bearer token required for non-loopback/shared HTTP mode |
| `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED` | `false` | Explicit unsafe override for non-loopback HTTP without auth |
| `JINA_API_KEY` | - | Jina AI API key for embeddings |
| `OPENAI_API_KEY` | - | OpenAI API key (or compatible: Together, Mistral) |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `OPENAI_EMBEDDING_MODEL` | `text-embedding-3-small` | Embedding model name |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama URL (local fallback) |
| `LLAMACPP_BASE_URL` | - | llama.cpp OpenAI-compatible base URL (e.g. `http://127.0.0.1:8080/v1`); empty disables it |
| `LLAMACPP_EMBEDDING_MODEL` | `bge-m3` | llama.cpp embedding model label (used only when `LLAMACPP_BASE_URL` is set). The label is part of the derived model id stored on every record, so changing it invalidates the bank exactly as changing the model does — see `docs/EMBEDDING_MIGRATION.md` |
| `MCP_EMBEDDING_MODE` | `auto` | Embedding mode: `auto` or `local-only` |
| `MCP_EMBEDDING_DIMENSION` | `1024` | Vector dimension (change requires re-indexing) |
| `MCP_EMBEDDING_TIMEOUT` | `5s` | Per-request embedding timeout; raise on slow local backends |
| `MCP_EMBEDDING_MAX_RETRIES` | `1` | Embedding retry count on transient failures |
| `MCP_INDEX_DIRS` | `docs` | Comma-separated directories and individual files to index for RAG. Code fallback is `docs`; the shipped `.env.example` preset sets `docs,README.md,CHANGELOG.md` for a typical project layout |
| `MCP_RAG_AUTO_INDEX` | `true` | Index documents on startup. Code default is `true` (good for HTTP/service mode); the solo-local `.env.example` preset turns it off so you control indexing with explicit `agent-memory-mcp index` runs |
| `MCP_RAG_FILE_WATCHER` | `false` | Watch `MCP_INDEX_DIRS` for changes and reindex incrementally; useful for long-running shared/service instances |
| `MCP_INDEX_EXCLUDE_DIRS` | built-in defaults | Extra directory names or repo-relative paths to exclude from RAG indexing |
| `MCP_INDEX_EXCLUDE_GLOBS` | - | Extra glob patterns matched against repo-relative paths, for example `docs/internal/*.md` |
| `MCP_REDACT_SECRETS` | `true` | Redact common secret-like content before documents are indexed |
| `MCP_ARCHIVE_SWEEP_ENABLED` | `true` | Zero-ops consolidation: a background loop marks archived-task working memories outdated (or promotes durable ones) with no manual runs. Auto-discovers `<MCP_ROOT>/tasks/archive`; no-op if absent. See [Zero-ops consolidation](#zero-ops-task-memory-consolidation) |
| `MCP_ARCHIVE_SWEEP_INTERVAL` | `1h` | Background archive-sweep cadence. `0` disables the loop |
| `MCP_SESSION_TRACKING_ENABLED` | `true` | Enable background session tracking, auto raw summaries, and low-risk close-session orchestration |
| `MCP_SESSION_IDLE_TIMEOUT` | `10m` | Idle timeout before the active background session auto-closes |
| `MCP_SESSION_CHECKPOINT_INTERVAL` | `30m` | Interval for periodic raw checkpoint snapshots during active sessions |
| `MCP_SESSION_MIN_EVENTS` | `2` | Minimum tracked MCP tool calls before background auto-close runs |
| `MCP_DATA_PATH` | `data` | Base path for data storage |
| `MCP_RAG_INDEX_PATH` | `<MCP_DATA_PATH>/rag-index` | Override the SQLite vector index location |
| `MCP_MEMORY_DB_PATH` | `<MCP_DATA_PATH>/memory-store/memories.db` | Override the SQLite memory database path |
| `MCP_LOG_PATH` | `<MCP_DATA_PATH>/logs/mcp-diagnostics.log` | Override the diagnostics log file path |
| `MCP_STATS_ENABLED` | `false` | Append per-call usage records (jsonl) for self-observability |
| `MCP_STATS_PATH` | `<MCP_DATA_PATH>/logs/mcp-usage.jsonl` | Stats jsonl output path |
| `MCP_STATS_SAMPLE_RATE` | `1.0` | Fraction (0.0–1.0) of calls to record when stats are enabled |
| `MCP_STEWARD_ENABLED` | auto | Enable knowledge stewardship (auto-enabled in HTTP mode with memory) |
| `MCP_STEWARD_MODE` | `manual` | Stewardship mode: `off`, `manual`, `scheduled`, `event_driven` |
| `MCP_STEWARD_SCHEDULE_INTERVAL` | `24h` | Interval between scheduled stewardship runs |
| `MCP_STEWARD_DUPLICATE_THRESHOLD` | `0.85` | Similarity threshold for duplicate detection |
| `MCP_STEWARD_STALE_DAYS` | `30` | Days before a memory is considered stale |
| `MCP_STEWARD_CANONICAL_MIN_CONFIDENCE` | `0.80` | Minimum confidence for canonical promotion candidates |
| `MCP_CHECKPOINT_DEDUP_THRESHOLD` | `0.9` | Jaccard similarity threshold above which a checkpoint is considered a duplicate of the previous one in the same context |
| `MCP_CHECKPOINT_DEDUP_WINDOW` | `10m` | Time window for the dedup lookup — only checkpoints newer than this are compared |
| `MCP_CHECKPOINT_DEDUP_MIN_CHARS` | `100` | Minimum content length (chars) before a checkpoint is eligible to be saved; shorter content is dropped as empty |
| `MCP_CHECKPOINT_DEDUP_DISABLED` | `false` | Escape hatch: disable checkpoint-hook deduplication entirely |
| `MCP_TASK_ARCHIVE_ROOTS` | - | Colon-separated archive roots for `sweep-archive` / `end-task` (e.g. `/home/you/tasks/archive`). Empty disables the feature |
| `MCP_TASK_SLUG_PATTERN` | - | Optional regex filtering archive subdirectory names; invalid regex fails config load |
| `MCP_RERANK_ENABLED` | `false` | Master gate for the neural reranker stage after hybrid search. Must be `true` AND `MCP_RERANK_PROVIDER` must be a real provider (`jina`) for the reranker to run |
| `MCP_RERANK_PROVIDER` | `disabled` | Reranker provider: `jina` or `disabled`. With `disabled` (or empty) the pipeline degrades to hybrid-only ranking even when `MCP_RERANK_ENABLED=true` |
| `JINA_RERANKER_MODEL` | `jina-reranker-v2-base-multilingual` | Jina reranker model id |
| `MCP_RERANK_TIMEOUT` | `5s` | Hard timeout for one rerank call; on timeout the hybrid order is kept and `rerank_failed:timeout` is added to debug signals |
| `MCP_RERANK_TOP_N` | `40` | Number of top hybrid candidates sent to the reranker; clamped to `100` at call time |
| `MCP_RETRIEVAL_STRICT` | `false` | Turn silent degradation on the read path into a failed call: an embedding provider falling through to the next one, a reranker timing out, a multihop query with no graph to walk. Intended for measurement runs (`make eval` enables it) and for diagnosing a half-configured install — **leave it off in production**, where a worse answer beats no answer. Regardless of this flag, every `semantic_search` response carries a `retrieval` block naming the path that actually served it |
| `MCP_SEDIMENT_ENABLED` | `false` | Enable layer-aware retrieval scoring (character always surfaced, surface excluded outside context). Schema migration + backfill always run; only retrieval weighting is gated. See `docs/SEDIMENTATION.md` |
| `MCP_RECALL_CENTERED` | `true` | Score memory recall over mean-centered embeddings instead of raw cosine. Adopted on a measured win (T76a): on 345 machine-labelled queries against a live bank, Hit@5 went 0.6232 → 0.7217 and MRR 0.4922 → 0.5739. Raw cosine on real corpora is anisotropic — unrelated pairs sat at a median of 0.555, so `minScore` cleared 100% of candidates and gated nothing; centered, the same sample clears it at 34.1%. Banks with fewer than 100 embeddings ignore this and use raw cosine, since a mean over a handful of vectors is dominated by the vectors it must cancel. Set `false` to score the way earlier releases did |
| `MCP_RECALL_HALFLIFE_DAYS` | `0` | T68 exponential age decay on recall scoring (half-life in days; a card this old scores at half weight). `0` disables decay — the default since T121 measured it: over 345 machine-labelled queries Hit@5 was 0.7217 with decay off against 0.1942 at the previous 30-day default, monotone in between (365d 0.6087, 180d 0.4609, 90d 0.2870), and no age bucket where decay paid for itself. Preferring the current version of a fact is already handled semantically by supersession and lifecycle status; a calendar multiplier cannot tell "written a while ago" from "no longer true". Evergreen entries (canonical knowledge, character layer) never decay |
| `MCP_RECALL_DECAY_TYPES` | `working` | Which memory types age when decay is enabled at all. The type axis dominates the rate: at the same 30-day half-life, decaying every type scored Hit@5 0.1942 while decaying only `working` scored 0.7043 — the old behaviour aged patterns and facts, which is the knowledge the bank exists to accumulate. Empty means every type decays |
| `MCP_RAG_KEEP_NOISE` | `false` | T49 escape hatch: keep noisy Markdown sections (Table of Contents / References / Changelog / etc.) in the index instead of dropping them at chunking time |
| `MCP_RAG_MAX_CHUNKS_PER_DOC` | `1` | How many chunks of one document may occupy a search result list. Adjacent chunks of a file carry nearly the same score, so an uncapped top-5 unfolded into **2.07** distinct documents on 250 questions — over half the list spent on more of a file already shown. At one chunk per document that is 5.00, R@5 by document 0.5104 → 0.8676 and nDCG@5 0.6184 → 0.8927, while first-hit metrics hold (Hit@5 0.9600 → 0.9640, MRR 0.9580 → 0.9590); 207 of 250 queries improved and none got worse (T127). The cap is a preference, not a quota — when fewer documents exist than the limit asks for, the skipped chunks are added back rather than the list coming up short. `0` restores the uncapped order, `2` is the middle position (3.11 documents, R@5 0.7648) |
| `MCP_DATA_SCOPE` | - | `project` keeps data, logs and stats outside the repository, in `$XDG_DATA_HOME/agent-memory-mcp/projects/<name>-<hash>` (`~/.local/share/...` when unset), keyed by the path of the repository's common `.git`. All worktrees share one database, and the index is built from the main checkout whichever worktree the server started in, so uncommitted documents in a worktree are not indexed. A submodule is its own project; a folder without git is keyed by its path. The data survives deleting a clone. Moving a repository changes the key: move the directory by hand or set `MCP_DATA_PATH`. `MCP_DATA_PATH`, `MCP_MEMORY_DB_PATH` and `MCP_RAG_INDEX_PATH` still win. With no user directory, as in a bare container, data goes to `<root>/.agent-memory`. The Claude Code plugin sets this; unset keeps `<root>/data` |
| `MCP_RAG_KEYWORD_FALLBACK` | `false` | Work without any embedding model. A fresh index stores text only and queries are answered by FTS5 keywords, with `retrieval.keyword_only` and `retrieval.degraded` set. On start the index is rebuilt with vectors once a model is reachable. Off by default: on an installation that has a model, an outage must not replace good vectors, and with a stored model indexing still fails during an outage. Search during an outage falls back to keywords only when this is on. `MCP_RETRIEVAL_STRICT` wins over it |
| `MCP_TRIPLE_EXTRACTOR_ENABLED` | `false` | T50 knowledge-graph layer. Enable to fire an async LLM call on every memory write that extracts 3-7 (subj, rel, obj) triples powering `recall_multihop` |
| `MCP_TRIPLE_EXTRACTOR_BASE_URL` | - | OpenAI-compatible `/chat/completions` endpoint (DeepSeek, Together, Groq, Qwen, …); falls back to `OPENAI_BASE_URL` when empty |
| `MCP_TRIPLE_EXTRACTOR_API_KEY` | - | Bearer token for the extractor. Falls back to `OPENAI_API_KEY` **only** when `MCP_TRIPLE_EXTRACTOR_BASE_URL` is empty or equal to `OPENAI_BASE_URL`. Pointing the extractor at a third-party endpoint without giving it its own key disables extraction with an explicit message — the OpenAI key is never sent to an address it was not issued for |
| `MCP_TRIPLE_EXTRACTOR_MODEL` | - | Model id passed to the extractor (e.g. `deepseek-chat`, `qwen2.5-72b-instruct`) |
| `MCP_TRIPLE_EXTRACTOR_TIMEOUT` | `30s` | Per-request timeout for the extractor HTTP call |

### Data paths

The server creates these directories under `MCP_DATA_PATH`:

- `rag-index/` -- SQLite vector index for document search
- `memory-store/` -- SQLite database for agent memories

The recommended solo-local preset stores them under `.agent-memory/`.

### Indexing safety controls

RAG indexing scans supported docs and engineering text files, but you can further reduce risk with explicit controls:

- built-in excluded directories such as `.git`, `.agent-memory`, `node_modules`, `logs`, and `.terraform`
- `MCP_INDEX_EXCLUDE_DIRS` for repo-relative path excludes such as `docs/private,runbooks/internal`
- `MCP_INDEX_EXCLUDE_GLOBS` for glob-style excludes such as `docs/internal/*.md`
- `MCP_REDACT_SECRETS=true` to redact common secret-like lines and private key blocks before indexing

This is especially important if you use hosted embedding providers or shared HTTP mode.

### Automatic session tracking

When the MCP server is running with the default session-tracking policy, it keeps a lightweight background session buffer.

Current behavior:

- successful MCP tool calls are grouped into an active session automatically
- idle timeout or server shutdown triggers a background `close_session` run
- clients can explicitly flush or checkpoint the active session with `notifications/session_event` and `event=task_done|final_summary|checkpoint|reset`
- raw session summaries are persisted automatically
- low-risk updates can auto-apply under the existing `safe_auto_apply` policy
- risky or ambiguous changes are stored as review inbox items instead of silently rewriting maintained knowledge
- periodic raw checkpoints provide crash-recovery breadcrumbs during long sessions

To inspect the inbox, use `project_bank_view view=review_queue` or `agent-memory-mcp project-bank -view review_queue`.
To close an item after manual review, use `resolve_review_item` or `agent-memory-mcp resolve-review-item <id>`.

Example notification payload:

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/session_event",
  "params": {
    "event": "task_done",
    "summary": "Incident stabilized, workaround verified, follow-up is to replace the temporary fix.",
    "context": "payments",
    "service": "api",
    "mode": "incident",
    "tags": ["done", "verification"]
  }
}
```

If you want to tune or disable this behavior, use `MCP_SESSION_TRACKING_ENABLED`, `MCP_SESSION_IDLE_TIMEOUT`, `MCP_SESSION_CHECKPOINT_INTERVAL`, and `MCP_SESSION_MIN_EVENTS`.

### Index integrity and recovery

Document indexing now treats chunk updates and tracking metadata as one logical state.

- each run marks the index state as `dirty` before changing chunks
- a successful final commit flips the state back to `ready` together with `indexed_files`, `embedding_model`, and `last_indexed`
- if a run is interrupted or the final tracking-state commit fails, the next `index_documents` / `agent-memory-mcp index` run detects the dirty state and forces a rebuild

This makes incremental indexing more predictable after crashes, provider interruptions, or storage errors.

### Source-aware ingestion

The indexer now classifies engineering sources and carries that metadata into retrieval.

Supported source types:

- `docs` for `README.md` and general Markdown docs
- `adr` and `rfc` for architecture decision and RFC-style documents
- `changelog` for `CHANGELOG.md` and release-note style docs
- `runbook` and `postmortem` for operational knowledge
- `ci_config` for GitHub Actions, GitLab CI, and Jenkins pipeline files
- `helm`, `terraform`, and `k8s` for source-aware infra files

Use `source_type` when you want to narrow retrieval to a specific class of knowledge:

```bash
agent-memory-mcp search -source-type runbook "ingress rollback"
agent-memory-mcp search -source-type adr "cache invalidation decision"
```

The MCP `semantic_search` tool also accepts `source_type` and `debug`.

### Hybrid retrieval

Search now uses multiple ranking signals instead of cosine similarity alone.

Current ranking signals:

- semantic similarity from the active embedding model
- keyword/BM25-like scoring across chunk title, path, and content
- `source_type` filtering when you want a narrower retrieval set
- recency boost for recently updated operational context
- source-aware weighting so runbooks, changelogs, ADRs, and other source classes can rank higher for matching query intent

The retrieval pipeline now works in two stages:

- semantic top-K candidate generation from the vector index
- keyword top-K candidate generation from a precomputed in-memory keyword index

Only the merged candidate set is reranked. This keeps shared-service retrieval more predictable as the indexed corpus grows.

This means a strong keyword hit in a runbook or changelog can outrank a semantically similar but less task-relevant document.

### Trust-aware retrieval

Retrieval now carries explicit trust metadata for both stored memories and indexed docs.

Each result can expose:

- `source_type`
- `confidence`
- `last_verified_at`
- `owner`
- `freshness_score`

What this means in practice:

- accepted decisions and verified runbooks rank above draft or low-confidence notes when the textual match is similar
- ADRs, runbooks, postmortems, and changelogs can carry different trust weights even before you add a full canonical layer
- CLI `search` / `recall` and MCP `semantic_search` / `recall_memory` now show trust summaries in human-readable output

Engineering workflow tools also stamp stored entries with `last_verified_at` so fresh operational knowledge is easier to trust and rank.

### Explainable retrieval

Use debug mode when you want retrieval to explain why a document was returned.

CLI:

```bash
agent-memory-mcp search -source-type runbook -debug "ingress rollback"
```

MCP:

- call `semantic_search` with `debug: true`
- keep `debug` unset or `false` for the normal compact response

Debug mode adds:

- applied filters such as `source_type=runbook`
- ranking signals used for the response
- candidate counts: indexed, filtered out, discarded as noise, returned
- per-result trust summary: `source`, `confidence`, `freshness`, `owner`, `verified`
- per-result score breakdown: `semantic`, `keyword_raw`, `keyword_normalized`, `recency_boost`, `source_boost`, `confidence_boost`, `final_score`
- per-result applied boosts, for example `keyword_match` or `source_type:runbook`

### Retrieval console

If you want a faster inspection workflow than raw CLI or JSON-RPC calls, use the built-in console in HTTP mode:

```text
/console
```

What it is good for:

- compare normal vs debug retrieval side by side
- inspect document search, raw memory recall, and canonical knowledge recall in one place
- see source type, trust layer, confidence, freshness, owner, and verification time
- open raw JSON for the same structured response the UI is rendering

The console is intentionally lightweight and does not replace MCP tools or CLI workflows.

### Engineering workflow tools

These MCP tools map domain-specific workflows onto the existing memory and retrieval backends.

Recommended starting points:

- `store_decision` for architectural or operational choices such as disabling HPA or pinning an ingress version
- `store_incident` for short-lived operational facts you want to recall during active debugging
- `store_runbook` for procedural steps, rollback instructions, and verification notes
- `store_postmortem` for durable incident learnings and action items
- `close_session` when you want an explicit end-of-session plan with rationale, traces, and review-safe actions
- `accept_session_changes` when the close-session report is low risk and you want to persist the raw summary plus apply safe updates
- `resolve_review_item` when the background inbox already contains a reviewed item and you want to clear it without deleting the audit trail
- `search_runbooks` when you need a fix path and want both memory-stored runbooks and indexed runbook docs
- `recall_similar_incidents` when you are triaging an outage or regression
- `summarize_project_context` at session start to get a compact operational briefing
- `project_bank_view` when you want maintained knowledge by view instead of raw recall results, including `review_queue` for pending background decisions

These workflow tools also add verification metadata so that retrieval can treat newly stored operational knowledge as fresher and more trustworthy than anonymous raw notes.

### Memory consolidation

The memory layer now supports a manual consolidation workflow without deleting historical notes.

Use these MCP tools when the same project knowledge starts to drift:

- `merge_duplicates` to consolidate repeated notes into one primary memory and archive the rest as merged duplicates
- `mark_outdated` to demote stale runbooks, superseded decisions, or obsolete incident notes without losing them
- `promote_to_canonical` to mark the current best memory as canonical knowledge
- `conflicts_report` to surface `duplicate_candidates`, `status_conflict`, and `multiple_canonical` groups

Current behavior:

- merged or archived memories stay accessible, but trust-aware recall pushes them down
- canonical memories get a trust boost and higher minimum importance
- outdated or superseded memories are automatically archived and downranked
- conflict reporting is manual and safe: nothing is deleted unless you explicitly choose to delete it

### Zero-ops task-memory consolidation

Every closed task leaves behind working memories (`Task started`, per-phase
notes, `Session close`, auto-extracted review items). Left alone they accumulate
linearly and keep surfacing in recall for tasks that are already done. The
service consolidates them automatically — no cron, no manual cleanup, no config:

- A background loop (on by default, `MCP_ARCHIVE_SWEEP_INTERVAL`, default 1h)
  sweeps archived tasks: durable entries (procedural, or importance ≥ 0.70) are
  promoted, the rest are marked `outdated`. It runs a first pass shortly after
  startup, which also backfills any archive that accumulated before the loop
  existed.
- **Zero configuration.** With `MCP_TASK_ARCHIVE_ROOTS` unset it watches the
  `<MCP_ROOT>/tasks/archive` convention; a missing directory is a silent no-op.
- **Safe by construction.** Promotion goes through the provenance gate: a
  conversational-origin memory is routed to the review queue, never
  auto-canonicalized (see [Memory poisoning defense](THREAT_MODEL.md)).
- The `end_task` and `sweep_archive` tools default to the same consolidating
  behavior, so an explicit `/end-task` consolidates immediately.

Policy details (state derivation, promotion threshold, idempotency,
symlink/traversal guards): [`docs/concepts/lifecycle.md`](concepts/lifecycle.md).
Turn the loop off with `MCP_ARCHIVE_SWEEP_ENABLED=false`.

### Canonical knowledge layer

The project now exposes two distinct layers:

- `raw memory`: captured notes, incidents, decisions, and procedural memories as they were stored
- `canonical knowledge`: confirmed entries projected from memories promoted with `promote_to_canonical`

What this changes:

- `list_canonical_knowledge` gives you the current confirmed knowledge set without raw noise
- `recall_canonical_knowledge` searches only canonical entries
- `summarize_project_context` surfaces canonical knowledge before raw memory sections when canonical entries exist
- trust summaries now show `layer=raw`, `layer=canonical`, or `layer=document`

Migration story:

- existing memories do not need a schema migration
- promote any high-value existing memory with `promote_to_canonical`
- legacy workflow memories that only have tags like `decision` or `service:api` are still recognized by the canonical layer

### Knowledge stewardship

The stewardship layer provides automated and manual knowledge maintenance.

`steward_run` executes a full maintenance cycle in one call:

- scans for duplicate candidates (memories with matching entity/service/context/subject)
- detects conflicting entries (multiple canonical, status disagreements)
- flags stale entries (not verified within the configured threshold)
- suggests canonical promotion candidates (high-importance, active, recognized engineering type)
- generates a structured report with per-action rationale
- with `dry_run=false`, applies safe actions and sends the rest to the stewardship inbox

`drift_scan` compares memory entries against live repo files:

- detects `source_changed` when a referenced file was modified after the memory was last verified
- detects `source_missing` when a referenced file path no longer exists
- flags `stale_unverified` when entries exceed the stale threshold

`verification_candidates` ranks memories that need verification:

- never-verified canonical entries: high urgency
- entries with `verification_failed` or `needs_update` status: high urgency
- stale entries beyond threshold: medium urgency

`steward_inbox` is the single place for all review-required actions. Resolve items with `steward_inbox_resolve` using actions like merge, mark_outdated, promote, verify, suppress, or defer.

Stewardship is auto-enabled in HTTP mode when memory is available. Configure thresholds and mode via `MCP_STEWARD_*` environment variables. See `steward_policy` for runtime configuration.

For a detailed guide, see [Stewardship Guide](STEWARDSHIP.md).

### Temporal knowledge

Memories can carry temporal metadata that tracks when knowledge was valid and how it evolved:

- `valid_from` / `valid_until` — the time window during which this knowledge was true
- `superseded_by` / `replaces` — bidirectional links forming supersession chains
- `observed_at` — when knowledge was first observed (may differ from `created_at`)

`recall_as_of` retrieves knowledge that was valid at a specific timestamp. This is useful for questions like "what was our database strategy in January?" or "what changed between these two dates?"

`knowledge_timeline` shows the chronological evolution of entries matching a query, ordered by `valid_from`.

When `mark_outdated` is called with a superseding entry, the system automatically sets `valid_until` on the old entry and `valid_from` + `replaces` on the new entry, building a navigable chain.

## Security And Operations

Core deployment guidance:

- solo local: keep `MCP_HTTP_MODE=stdio`, prefer `MCP_EMBEDDING_MODE=local-only` if you need no-send semantics
- shared HTTP mode: set `MCP_HTTP_HOST=0.0.0.0`, set `MCP_HTTP_AUTH_TOKEN`, keep TLS at the reverse proxy, and scope `MCP_ALLOW_DIRS` narrowly
- indexing: exclude private runbooks or credentials docs with `MCP_INDEX_EXCLUDE_DIRS` / `MCP_INDEX_EXCLUDE_GLOBS`
- backup: either copy `.agent-memory/` or use `agent-memory-mcp export` for memory-only backups

Reference docs:

- [Stewardship Guide](STEWARDSHIP.md)
- [Security Policy](SECURITY.md)
- [Threat Model](THREAT_MODEL.md)
- [Backup And Restore](BACKUP_RESTORE.md)
- [Shared Service Guide](SHARED_SERVICE.md)
