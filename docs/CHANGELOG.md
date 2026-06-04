# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.8.8] - 2026-06-04

Feature release. Recall now applies exponential age decay so stale memories sink,
and the duplicate `Session close / X` records that bloated the steward review
inbox are tackled on both sides — prevented at write time and auto-mergeable in
the steward.

### Added

- **Temporal decay for recall scoring (T68)** — recall ranking weighted relevance by importance, confidence, and a soft trust-freshness term, but had no explicit decay by age: a month-old episodic competed almost head-to-head with today's note. `Recall` now multiplies the weighted score by `e^(-ln2/halflife × ageDays)` (age from `created_at`), so a card at one half-life scores at ~50% and naturally falls under the `minScore` cutoff as it ages. The half-life is configurable via `MCP_RECALL_HALFLIFE_DAYS` (default 30; `0` disables decay). Decay is a multiplier kept deliberately separate from the existing `trust.FreshnessScore` term — they are different axes (source-verification recency vs calendar age) — and is applied before the additive sediment-layer boosts so character's always-surface boost is never eroded. Evergreen entries never decay: canonical knowledge (lifecycle/knowledge-layer `canonical`) and the character sediment layer. Unit tests cover the boundary values, monotonicity, evergreen/off exemptions, and an end-to-end recall ordering where the fresher of two equal-relevance memories ranks first.
- **Opt-in steward auto-merge for near-duplicate groups (T69)** — subject-key duplicate groups (e.g. several `Session close / X` for one context) were detected at confidence `0.75` but always queued as `review_required`, so an auto-mode steward accumulated hundreds of pending merges it never applied. `steward_policy` gains `auto_merge_duplicate_min_confidence` (default `0.95` = off, also the safe behaviour for policies persisted before the field) and `auto_merge_require_content_similarity` (default `0.85`). A group auto-applies only when the detection confidence is at or above the min-confidence **and** every non-primary member is textually near-identical to the primary (Jaccard ≥ the threshold, reusing the session-checkpoint dedup hashing so no embeddings are needed) **and** no member is canonical — otherwise it stays `review_required`. This is the cleanup-side complement to the write-time fix below.

### Changed

- **Idempotent session-close writes (T71)** — two independent paths wrote near-identical episodic records per slug: the `/finalize` workflow's `Task complete: X` and the `SessionEnd` auto-hook's `Session close / X`. On a large corpus this produced hundreds of duplicate pairs that the steward then flagged as duplicates/contradictions, drowning the review inbox. The session-close raw-summary write now folds into a recent terminal episodic of the same slug (a prior session summary or a `Task complete:` finalize record, within a 6h window) instead of creating a second one — merging tags/metadata, keeping the richer content and the higher importance. Only the terminal session-summary write consolidates; checkpoints and review-queue items keep their own pipelines. Cross-session duplicates remain the steward's job (now auto-mergeable via T69).

## [0.8.7] - 2026-05-30

Performance release. Fixes the recurring `index_documents` slowdown/timeouts on
local embedding backends by re-embedding only what actually changed and removing
a dead fallback provider.

### Added

- **Incremental re-index — reuse embeddings of unchanged chunks (T70)** — the indexer diffed at the file level: any edit (or even a `mod_time`-only touch) re-embedded **every** chunk of the file. A one-line change to a 507-chunk planning doc recomputed all 507 embeddings, producing 6-minute reindex cycles, embedding-slot starvation, and 60s batch timeouts on the local bge-m3 backend. `indexDocuments` now builds a content-hash → embedding reuse map from the file's existing chunks before deleting them; structure-aware chunking (T49) keeps unchanged sections byte-identical, so only the edited section's chunks are re-embedded and the rest reuse their stored vectors. Reuse is skipped on a full rebuild (a model or chunker-version change invalidates old vectors). An unchanged file now reuses every embedding (zero embed calls), neutralising the `mod_time`-triggers-full-reindex path.

### Fixed

- **Dead Ollama fallback tripled every llama.cpp failure** — `Embedder.New` force-defaulted `OLLAMA_BASE_URL` to `localhost:11434` whenever it was empty, so Ollama always joined the candidate chain. After a host switched from Ollama to llama.cpp, every llama.cpp batch failure was followed by two connection-refused retries (`bge-m3` + `mxbai-embed-large`) — hundreds of dead failures per day in the logs plus retry latency on the indexing hot path. Ollama now defaults only when no other local backend (llama.cpp) is configured; an explicit `OLLAMA_BASE_URL` still enables it, and Ollama-only setups are unaffected.

## [0.8.6] - 2026-05-29

Feature release. Memory preview truncation in MCP tool responses is now
configurable, and the legacy byte-slice truncation that could corrupt
multibyte content is gone.

### Added

- **`MCP_MEMORY_PREVIEW_RUNES` — configurable preview truncation** — memory `content`/`summary` fields in MCP tool responses (`recall_memory`, `list_memories`, `search_runbooks`, canonical-knowledge views, …) were hardcoded to per-surface caps (150/220/300). The new env var overrides that policy: `0` (default) keeps the built-in per-surface caps, a positive value forces that single rune cap on every surface, and a negative value disables truncation entirely so agents can read the full body of a runbook/decision. All preview paths now route through a single rune-aware `previewText` helper.

### Fixed

- **UTF-8 corruption on truncated previews** — `formatMemoryResults`/`formatMemoryList` still cut with the byte-based `s[:300]` idiom, which splits a multibyte sequence mid-codepoint on Cyrillic/CJK/emoji content and emits invalid UTF-8. Truncation is now rune-aware via `textfmt.Truncate` everywhere. Regression test `TestPreviewText` asserts the three policy branches and that a Cyrillic cut stays valid UTF-8.

## [0.8.5] - 2026-05-23

Bugfix release. Removes the last concurrent-writer path that could still reach
`SQLITE_BUSY` after 0.8.4 (RC5 in the SQLite busy incident postmortem).

### Fixed

- **Foreground `index_documents` could write concurrently with the file-watcher** — `server.callIndexDocuments` called `Engine.IndexDocuments` directly, bypassing the `indexWithLock` guard (`re.indexing`) that only the startup and file-watcher paths used. So a manual `index_documents` and a background watcher index could write to `vectors.db` at the same time. 0.8.4 made them queue politely (busy_timeout up to 5s), but a heavy index holding a write transaction longer than that could still hit `SQLITE_BUSY`. `IndexDocuments` now holds a dedicated `Engine.indexMu` for its whole duration, serialising every indexing run regardless of caller. The watcher's `indexing` flag is unchanged (it still coalesces debounced ticks); foreground callers now wait on the mutex and index for real instead of being skipped. Regression test `TestIndexDocumentsSerialisesWriters` asserts a second call blocks while the lock is held and completes once released. See `06-planning/2026-05-05-sqlite-busy-incident.md` §7 RC5.

## [0.8.4] - 2026-05-23

Bugfix release. Closes the SQLITE_BUSY recurrence on `index_documents` that the
0.8.0 incident fix (`internal/dbutil`) did not fully resolve.

### Fixed

- **SQLITE_BUSY recurred because `busy_timeout` never reached the whole pool** — `dbutil.OpenSQLite` applied the pragmas via `db.Exec` after Open. `journal_mode=WAL` is persisted at the database-file level (so WAL did engage), but `busy_timeout` is **per-connection**: a single `Exec` only configures the one pooled connection that served it, and every other connection `database/sql` opened for concurrent work defaulted to `busy_timeout=0` → instant `SQLITE_BUSY`. Observed when the background file-watcher index raced a write (logs 2026-05-22 14:54 `delete chunks`, 2026-05-23 00:24 `upsert chunk`, `trigger: file_watcher`). `OpenSQLite` now passes the pragmas through the DSN (`_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)`), which `modernc.org/sqlite` runs on **every** new pool connection (`Driver.Open`), plus `_txlock=immediate` so writers take the write lock at `BEGIN` — `busy_timeout` is not honored when a deferred transaction fails to upgrade a read lock to a write lock (SQLite returns `SQLITE_BUSY` immediately without invoking the busy handler). Regression test `TestOpenSQLite_BusyTimeoutPerConnection` asserts the timeout holds on a second pooled connection. The original `?_journal_mode=WAL` DSN form (incident RC2) was also a non-existent `modernc` parameter — the driver only honors `_pragma=...` — which is why the rollback journal appeared on disk in the first place. See `06-planning/2026-05-05-sqlite-busy-incident.md` §7. Known follow-ups (tracked in the incident doc): foreground `index_documents` bypasses the file-watcher's write guard (RC5), and heavy indexing can still hit the embedding HTTP timeout.

## [0.8.3] - 2026-05-22

Bugfix release. The llama.cpp backend shipped in 0.8.2 never reached the RAG
indexing path, so `index_documents` failed on any host without Ollama.

### Fixed

- **RAG embedder ignored llama.cpp** — `internal/rag/rag.go` built its embedder from a hand-written `embedder.Config` literal that omitted `LlamaCPPBaseURL`/`LlamaCPPModel` (and hardcoded `MaxRetries`/`Timeout`). T64 wired llama.cpp into `Config.EmbedderConfig()` and both embedder candidate chains, but this inline literal was never updated — so RAG indexing silently skipped llama.cpp and fell back to Ollama. With Ollama down, `index_documents` failed with `all embedding providers failed` surfaced as a generic `-32000`, while memory (which uses `cfg.EmbedderConfig()`) kept working. The RAG engine now uses `cfg.EmbedderConfig()` as the single source of truth, matching `server.go` and the CLI helpers, and honors `MCP_EMBEDDING_TIMEOUT`/`MCP_EMBEDDING_MAX_RETRIES`.
- **`LoadFromEnv` config tests leaked machine config.env** — `LoadFromEnv` walks the dotenv chain (`CWD/.env → XDG → Homebrew prefix`), so a real `/opt/homebrew/etc/agent-memory-mcp/config.env` with `LLAMACPP_BASE_URL` set broke `TestLoadFromEnvLlamaCPPDisabledByDefault` and siblings locally (CI lacks the file, so it passed there). A `hermeticDotEnv` test helper now points every chain source at an empty temp dir.

## [0.8.2] - 2026-05-20

Self-hosting and client-compatibility hardening. Slow local embedding
backends no longer time out, llama.cpp joins the local-only path, MCP
clients using the Streamable HTTP transport can connect, and the steward
stops drowning the contradiction inbox with dual-encoding false positives.

### Added

- **T64 llama.cpp embedding backend** — a `llama-server --embedding` instance (OpenAI-compatible `/v1/embeddings`) can now serve embeddings as a second local-only option alongside Ollama. Opt-in via `LLAMACPP_BASE_URL` (empty disables it); when set it joins the fallback chain before Ollama (`Jina → OpenAI → llama.cpp → Ollama`) and works under `MCP_EMBEDDING_MODE=local-only`. `LLAMACPP_EMBEDDING_MODEL` defaults to `bge-m3`. The adapter reuses the shared OpenAI-compatible transport and omits the `dimensions` parameter (the model returns its native dimension, validated at recall time).
- **T65 configurable embedding timeout and retries** — `MCP_EMBEDDING_TIMEOUT` (default `5s`) and `MCP_EMBEDDING_MAX_RETRIES` (default `1`) replace the previously hardcoded values in `EmbedderConfig()`. Slow self-hosted backends (Ollama with `bge-m3` on low-core/ARM VPS, where a single chunk takes 4–7s) no longer hit persistent timeouts. Negative values are rejected at config load; invalid values fall back to the defaults so the service still starts. Defaults match prior behaviour.
- **T66 GET SSE endpoint on `/mcp`** — the MCP Streamable HTTP transport opens a server-push channel with `GET /mcp` and `Accept: text/event-stream`. Previously any non-POST request returned `405`, so clients like Cursor failed to connect and retried indefinitely. The stream is held open behind the same Bearer auth as POST, with a keepalive comment every 25s; the write deadline is cleared so the server `WriteTimeout` does not abort it. A plain `GET` without the SSE `Accept` header still returns `405`.

### Fixed

- **T60 steward dual-encoding contradiction false positives** — conflict detection treated any lifecycle difference between two similar memories as a contradiction. The same event is routinely stored at two layers — a raw session summary (`draft`) and the extracted/promoted entity (`active`/`canonical`) — so a full scan over ~1879 memories returned 64 contradictions, all dual-encoding pairs, forcing dozens of manual `suppress` actions per cleanup. A lifecycle difference now counts as a conflict only on explicit invalidation (one side `outdated`/`superseded` while the other is live); `draft`↔`active`↔`canonical` maturation is ignored. Genuine same-layer disagreements are still caught via explicit supersession links, temporal-window overlap, and content contradiction keywords. See `docs/STEWARDSHIP.md` for the dual-encoding policy.

## [0.8.1] - 2026-05-12

Memory cleanup unblocked. `sweep_archive` no longer hides the underlying
configuration error behind a generic JSON-RPC `-32000`, and a new
`auto_promote` flag turns the promotion path into an in-place
`PromoteToCanonical` instead of growing the review queue
proportional to closed tasks. Additional guards at the `store_memory`
boundary stop common status-stub titles from polluting recall.

### Fixed

- **T61 `sweep_archive -32000` masked the real error** — `lifecycle.ErrNoRoots` (set when `MCP_TASK_ARCHIVE_ROOTS` is empty) bubbled up as a generic `sweep_archive failed` server-error. New `mapSweepError` in `internal/server/tools_workflow.go` maps it to `rpcErrInvalidParams` with an actionable hint (`"Set MCP_TASK_ARCHIVE_ROOTS in service config or pass roots[] explicitly"`). `end_task` shares the same mapper. Tests cover the typed-error path and the unknown-error fallthrough.
- **Config hot-reload covers non-RAG fields** — `MCPServer.ReloadRAG` only wrote `s.config = newCfg` inside the RAG-enabled happy path, so a SIGHUP after editing `MCP_TASK_ARCHIVE_ROOTS` could not be picked up while RAG was off or failed to initialise. Refactored to `ReloadConfig` which assigns `s.config` first, under the existing `ragMu` write lock; `ReloadRAG` kept as a deprecated alias. `main.go` watcher and SIGHUP handler now call `ReloadConfig`.

### Added

- **T62 `sweep_archive --auto_promote` / `end_task --auto_promote`** — when set, high-importance promotion candidates are promoted to canonical in-place via `store.PromoteToCanonical` instead of producing a new `review_queue_item` working memory. Stops the inbox from growing 5-10 items per closed task. `ArchiveSweepConfig.AutoPromote`, `SweepResult.TotalPromoted`, and per-slug `Promoted` counters are exposed; `storeAPI` interface extended with `PromoteToCanonical`. Default remains `false` so existing operator review flows are unchanged. Dry-run honours the flag (counts go to the Promoted bucket, no writes).
- **Noise guards at the `store_memory` boundary** — `callStoreMemory` rejects working-memory titles matching status-stub prefixes (`Task started:`, `Research complete:`, `Spec created:`, `Plan created:`, `Implementation complete:`, `Tests complete:`, `Review queue /`) with `rpcErrInvalidParams` and a remediation hint pointing at `store_decision` / `store_runbook` / `end_task`. Stops a recurring class of recall pollution.
- **`SEMA_MCP_SUPPRESS_REVIEW_QUEUE_WRITES=1`** opt-out env in `session_tracker.persistReviewQueue` — auto-generated review queue items (typically importance 0.35–0.55, 5–10 per `close_session`) are kept in `result.Actions` for the caller but no longer persisted as working memories. Useful while operators triage the inbox manually before flipping `auto_promote` on globally.

### Operational note

`MCP_TASK_ARCHIVE_ROOTS` must be set in the service config (`/opt/homebrew/etc/agent-memory-mcp/config.env` for brew installs) for `sweep_archive` and `end_task` to do anything. Without it, both tools now return a typed invalid-params error instead of running silently against nothing.

## [0.8.0] - 2026-05-06

Round 3 remediation roadmap waves 0-2 closed plus Phase 3 partial. Highlights:
24× steward perf regression closed via cache-resident metadata; shutdown
goroutine drain and SQLite WAL hardening (incident 2026-05-04 root cause
+ pragma fix); ~500 LOC of tools/memory dedup; deterministic time
injection for tests.

### Fixed

- **T54 shutdown stability + race fixes (Round 3 Phase 0)** —
  - C1 `Close()` drains `extractionWG` before closing the DB; in-flight triple-extraction goroutines no longer write to a closed store on shutdown (paniced and lost data previously).
  - H1 `console.go` reads `ragEngine`/`memoryStore` via `getRagEngine()` / new `getMemoryStore()` getters; `-race` CI no longer reports a HTTP-mode + ReloadRAG race.
  - M2 `defer recover()` on the extractor goroutine — panics are logged, no longer crash the server.
  - M7 ollama retry sleep is now ctx-aware via `select { time.After / ctx.Done() }`; shutdown no longer hangs up to 4 seconds on a pending retry.
  - H12 `cmd/.../setup.go` replaces the home-grown `containsStr`/`findSubstr` with `filepath.Base`-comparing `isOurHookCommand`. `agent-memory-mcp-old` no longer false-matches `agent-memory-mcp` and overwrites unrelated hooks.
- **T59 SQLite busy-timeout incident hardening** — root cause of the 2026-05-04 25-hour `index_documents` stall. New `internal/dbutil` package opens both `vectors.db` and `memories.db` with explicit `PRAGMA busy_timeout=5000`, `journal_mode=WAL` (verified via the returned mode — the previous DSN form `?_journal_mode=WAL` was silently ignored by `modernc.org/sqlite`, leaving the DB in rollback-journal mode), and `synchronous=NORMAL`. Backfill migration adds `defer tx.Rollback()`. `tools_search` logs `index_documents` / `search` failures via `fileLogger.Error` so the next incident leaves a trace in `service.err.log` rather than only the JSON-RPC `data` field. See `06-planning/2026-05-05-sqlite-busy-incident.md` for the postmortem.
- **T52 steward run perf regression** — ~24× slowdown after T48-T50 traced via `BenchmarkRunScanners_2000` to `loadActiveMemories → Store.List → getBatch` doing a full-corpus SQL roundtrip per scan invocation. Root cause: `cachedMemory` did not carry the raw `Metadata` map, so steward (which needs `MemoryService` / `EngineeringTypeOf` / `LifecycleStatusOf`) was forced through the SQL re-hydration path. Fix: cache `Metadata` (~300 bytes/memory, 30 MB on a 100k corpus) and add `Store.ListLightweight(filters)` cache-only path. `loadActiveMemories` switched. Bench: 32ms → 8.6ms/op on 2000-memory corpus; projected real-world 351-memory corpus drops from 8.44s to <1s.
- **T53 steward mode reset on restart** — `server.go` unconditionally overrode the persisted policy with config defaults on every start, so a user-set `steward_policy mode=scheduled` was clobbered to `manual` after `brew upgrade`. Each `MCP_STEWARD_*` override is now gated on `os.LookupEnv` — env unset honours DB; explicit env applies config. Bonus observability: warn at startup when `mode=manual` and pending review queue >100, with a hint to run `steward_policy mode=scheduled`.

### Performance

- **T56 Round 3 Phase 2 hotspots** — N+1 patterns and WAL-fsync waste eliminated:
  - **H3** `RecallMultihop` collects all top-K ids and calls `getBatch` once instead of N separate `Get` calls. For limit=100 this drops 100 SELECTs to 1.
  - **H4** `getBatch` chunks IN-clause by 500 ids; SQLite's `SQLITE_MAX_VARIABLE_NUMBER` (default 999 in `modernc.org/sqlite`) no longer crashes ExportAll or any massive batch load. New regression test covers a 1500-id load.
  - **H5** `RunSedimentCycle` and `loadPendingSedimentReviews` use `ListLightweight` (cache-only); no full-corpus SQL roundtrip per cycle.
  - **H6** sediment cycle pre-loads existing pending review-queue items into a `{targetID:struct{}{}}` set ONCE; `createSedimentReviewItem` does O(1) dedup instead of re-Listing the working memories per candidate (was O(n*m) inside the loop).
  - **H7** archive sweep builds `existingReviews` from the already-loaded slug cohort and passes it to `createPromotionCandidate`. One List per slug instead of N.
  - **M3** `flushAccessStats` wraps per-id UPDATEs in a single transaction with prepared statement — WAL fsync count drops from N to 1 per batch.
  - **M8** Sweeper now serialises per-slug invocations via a `sync.Map` of `*sync.Mutex`. Concurrent `SweepArchive + EndTask` on the same slug can no longer race the existence-check + Store write.
  - **M9** Sweeper uses `os.Lstat` and rejects symlinked candidates (new `statNoSymlink`). A symlink under an archive root cannot redirect the sweeper to mark an unrelated slug as stale.
  - **M10** Session-tracker checkpoints run on a cap-2 semaphore-bounded goroutine pool — `HandleToolCall` no longer pays the DB+embedder latency tax synchronously. `Close()` drains via `checkpointWG.Wait()`. Tests get `waitForCheckpoints` / `waitForBackground` for deterministic sync without `time.Sleep`.
  - **M11** `flushSession` runs on a fresh `context.WithTimeout(60s)` instead of inheriting `st.ctx` (which `Close()` had already cancelled by the time the shutdown flush arrived).
  - **M18** `matchCachedFilters` no longer allocates a `map[string]struct{}` from `m.Tags` per memory; new `buildFilterTagSet` builds the filter-tags set once outside the Recall/List/ListLightweight loops, allocations go from O(N) to O(1).
  - **L13** BM25 boost magic numbers (`1.4` / `0.8` / `1.0` / `0.9` / `0.6` / `0.5`) moved into a documented `keywordScoreConfig` struct. No behavioural change; the defaults match prior values exactly.

### Refactor

- **T55 Round 3 Phase 1 dedup quick wins** — ~500 LOC removed across server tools and memory store; no public API change:
  - **server side** — `parseFormat` + `renderFormatted(format, value, textFn)` helpers replace ~14 sites that hand-rolled `if format == "json" { JSON } else { text }`. Steward tools now reject `format=yaml` with explicit `InvalidParams` instead of silently coercing to text. `buildSessionSchema(summaryDesc, extras)` collapses the 35-LOC inline schemas of `review_session_changes` and `accept_session_changes`. `requiredString(args, key)` consolidates 20+ sites of `getString + !ok || TrimSpace == ""` with a consistent `"<key> parameter is required"` message. `boundedLimit` applied to remaining manual `if limit <= 0` clamps in steward.
  - **memory side** — `internal/textfmt.Truncate` is the canonical rune-aware string truncator (fixes the byte-aware `lifecycle.truncate` UTF-8 corruption bug for Cyrillic / CJK / emoji titles). `scanMemoryRow(rowScanner)` + `const memoryColumns` consolidate the ~70-LOC scan path that `Get` and `getBatch` previously duplicated. `parseMetadataJSON(sql.NullString)` replaces five sites of bespoke unmarshalling. `referencedByCount` becomes a thin wrapper over `referencedByCountFromMetadata`. `updateCachedField(id, fn)` consolidates the four `mu.Lock; if cm, ok := memories[id]; ok { ... }; mu.Unlock` sites and fixes a microsecond drift between SQL `updated_at` and cached `UpdatedAt` (two separate `time.Now()` calls per write). `CosineSimilarity` moves to `internal/scoring`; `vectorstore.CosineSimilarity` is a thin alias for back-compat. `newTrackedSession(now)` centralises the start/activity/checkpoint timestamp triple.

### Added

- **T57 Round 3 Phase 3 (partial) — testability infra**:
  - **H19** `Store.SetClock(now func() time.Time)` injects the clock for deterministic temporal tests; all `time.Now()` calls in `*Store` methods route through `ms.now()`. New `TestStore_SetClockInjection` pins the contract.
  - **M24** new `storeAPI` interface in `internal/steward` (mirrors `internal/lifecycle`); `Service` / `RunScanners` / `loadActiveMemories` accept the interface so unit tests can inject fakes without spinning up a full SQLite store.
- **`internal/dbutil` package** — `OpenSQLite(dbPath, logger)` + `ApplyPragmas(db, logger)` helpers shared between memory store and vector store. WAL verification + busy_timeout in one place.
- **`internal/textfmt` package** — `Truncate(s, maxRunes)` rune-aware string truncator with TrimSpace, ellipsis, and proper `maxRunes < 3` handling.
- **`internal/scoring/cosine.go`** — `scoring.CosineSimilarity` is now the canonical implementation; `vectorstore.CosineSimilarity` redirects to it.

### Migration notes

- **No env-var changes.** The new `MCP_*` lookups are explicitly opt-in: previously implicit defaults remain identical when env is unset.
- **CHANGELOG hygiene** — this release also folds the changelog entries that should have been split into `[0.7.0]` / `[0.7.1]` (Wave 1-4, T48-T50, T51) into their dedicated sections below; the formerly-[Unreleased] block is now correctly attributed.

## [0.7.1] - 2026-05-03

### Fixed

- **T51 empty-context duplicate cluster guard** — `internal/steward/scanner.go:groupKey` now returns an empty key when entity, service AND context are all blank. Generic-subject working memories (e.g. multiple "Session close" records from auto-session writers without explicit context) used to hash into one cluster; on a live v0.7.0 steward run this surfaced a 29-record cluster of unrelated tasks waiting for review-required merge — approving it would have collapsed 29 different tasks into one. The guard rejects such clusters at the grouping step so they never enter the review queue. Existing pending items from pre-fix runs can be resolved manually via `resolve_review_item`. Regression tests cover both the suppression and the legitimate same-context cluster case.
- **T50 extractor fan-out test determinism** — bumped async triple-extraction deadline for slow CI runners so tests stop flaking under load.

## [0.7.0] - 2026-05-03

### Added

- **Wave 4 retrieval depth (T49 + T50)** — structure-aware Markdown chunking and multi-hop graph recall.
  - T49 Structure-Aware Chunking: `internal/rag/skeleton.go` parses Markdown headers into a skeleton tree, prefixes every chunk with a breadcrumb `[doc > section > subsection]`, respects section boundaries, and drops noisy sections (Table of Contents / References / Changelog / etc.) at ingest. Escape hatch `MCP_RAG_KEEP_NOISE=true`. Pointer-based retrieval via `Engine.ExpandSection(docPath, sectionKey)` and `SearchResult.SectionPath` / `SectionKey`. `chunker_version` bumped to `skeleton-v1` so existing indices auto-rebuild on next index pass.
  - T50 Multi-hop retrieval: `memory_triples` table (subj/rel/obj/memory_id/link_type/weight) with cascade-on-memory-delete; LLM-backed `TripleExtractor` interface + OpenAI-compatible HTTP impl (`MCP_TRIPLE_EXTRACTOR_{ENABLED,BASE_URL,API_KEY,MODEL,TIMEOUT}`) firing async on every `Store`/`Update` write; retrofit CLI `agent-memory-mcp index-triples [--resume|--force|--limit N|--context X|--dry-run|--json]`; `Store.RecallMultihop` weighted-BFS PPR walk with damping 0.85 and per-result triple paths; new MCP tool `recall_multihop`.
- **T45 server-side dedup gap fix** — `internal/server/session_tracker.go` now applies the same `hooks.Check` content-similarity filter as the CLI hooks before persisting via `SaveRawSummaryWithOptions`. Closes the regression where the in-process auto-session pipeline regenerated near-duplicate session-checkpoint records within minutes of `/memory-cleanup`.
- **T46 hygiene scan** — `Store.StaleDeadEnds(ctx, olderThan)` + `agent-memory-mcp dead-ends-stale [-age 12mo] [-limit N] [-json]` surface dead_end memories whose original constraint may no longer apply, sorted oldest-first.
- **Wave 1 hygiene** — T45 checkpoint-hook content-similarity filter (`MCP_CHECKPOINT_DEDUP_*`, Jaccard dedup over session-checkpoint records); T47 task-lifecycle archive sweep (`sweep-archive` / `end-task` CLI + MCP tools, `MCP_TASK_ARCHIVE_ROOTS`).
- **Wave 2 quality** — T43 RAG eval suite with baseline regression gate (`//go:build eval`, `make eval`, `docs/RAG_EVAL.md`); T46 dead-end tracking (`EngineeringTypeDeadEnd`, `store_dead_end` MCP tool, `mark-dead-end` CLI, retrieval boost + blend on pitfall keywords via `scoring.IsPitfallQuery`); T44 Jina v2 neural reranker (`internal/reranker`, `MCP_RERANK_*`, `JINA_RERANKER_MODEL`, 5s timeout with graceful fallback, `docs/RERANKER_LOCAL.md`).
- **Wave 3 architecture** — T48 memory sedimentation (`sediment_layer` column, `promote_sediment` / `demote_sediment` / `sediment_cycle` MCP tools, `sediment-cycle` CLI, `project_bank_view(view=sediment_candidates)`, `MCP_SEDIMENT_ENABLED`, `docs/SEDIMENTATION.md`).

### Migration notes

- **Markdown re-indexing** — first run on an existing `data/rag-index/vectors.db` after upgrade triggers a full chunker rebuild because `chunker_version` changed from `char-v1` to `skeleton-v1`. Expect the first `index_documents` pass to reprocess every `.md` file. No action needed; auto-index handles it on startup when `MCP_RAG_AUTO_INDEX=true`.
- **Triple graph backfill (optional)** — to populate the multi-hop graph layer for existing memories, set `MCP_TRIPLE_EXTRACTOR_ENABLED=true` along with the matching `BASE_URL`/`API_KEY`/`MODEL`, then run `agent-memory-mcp index-triples`. Idempotent; `--resume` (default) skips memories that already have triples. New writes are extracted automatically.

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
