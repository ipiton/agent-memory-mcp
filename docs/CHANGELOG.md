# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.10.0] - 2026-08-14

A minor bump rather than a patch, for one reason: recall now scores over
mean-centered embeddings by default, and that changes which memories come back
for a given query. The rest of the release is the retrieval branch getting its
instruments — a strict mode that refuses instead of degrading, a report of what
the corpus actually contains, a usefulness counter that counts usefulness, and
an evaluation harness that had quietly stopped compiling four months ago.

### Added

- **Strict retrieval mode (T99)** — `MCP_RETRIEVAL_STRICT=true` turns every silent degradation on the read path into a failed call: an embedding provider that fell through to the next one, a reranker that timed out, a multihop query with no graph to walk. Graceful degradation stays the production default; what was missing was the ability to ask for the opposite, without which a measurement cannot know it measured what it meant to. Independently of the flag, `semantic_search` responses now carry a `retrieval` block naming the provider that served the query, any providers that failed first, and why reranking was skipped. The text surface prints a line only when something actually degraded.
- **Corpus coverage after indexing (T97)** — `index_documents` answered `"Documents indexed successfully."`, so the one question worth asking afterwards had no answer. It now reports files and chunks per configured root, and a root that contributed nothing is named on its own line — that state is exactly what once read as "there is nothing to find".
- **A usefulness counter that measures usefulness (T113)** — new `targeted_access_count`, incremented only by recalls with a result budget. `access_count` counts every appearance in a result set, sweeps included; on a live bank that drove its median to 110 and made it a function of age rather than use. The sediment promotion gates read the new counter, and both numbers appear in tool output. Deliberately not backfilled: the old values are the defect.
- **`MCP_RECALL_CENTERED` (T76a)**, default `true` — see Changed.
- **A retrieval evaluation instrument with headroom (T119)** — the committed QA set reports Hit@5 = 1.0 with an oracle reranker worth +0.032, so nothing decided "by measured win" could be decided on it. `make eval-corpus` builds a harder set from a local task archive, `make eval-real` runs against it, and `make eval-recall` measures memory recall itself (Hit@5 0.62 before this release's scoring change). The harness can now take a real encoder: the built-in fixture is a hash of ASCII tokens, which on a non-English corpus produces no tokens and therefore pure noise.
- **Secrets from SOPS (T101)** — the service starts through a wrapper that runs `sops exec-env --same-process` when an encrypted secrets file is present, and execs the binary directly when it is not. `--same-process` matters: without it launchd would supervise `sops` rather than the service. See `docs/SECRETS.md`.
- **`docs/EMBEDDING_MIGRATION.md`** — the order of operations for switching encoders, the batch-size failure that reads as a provider error while its real cause sits in the server log, and the reason the scoring code and the model have to ship together.

### Changed

- **Recall scores over mean-centered embeddings (T76a)** — raw cosine on a real corpus is anisotropic: unrelated pairs sat at a median of 0.555 and same-task pairs at 0.786, so most of the scale carried no information. Centering moves unrelated pairs to −0.033 and widens the separation from 0.231 to 0.527. Measured on 345 machine-labelled queries against a live bank: **Hit@5 0.6232 → 0.7217, MRR 0.4922 → 0.5739**. The second effect is on the gate rather than the ranking — `minScore = 0.05` was cleared by 100.0% of a sampled 68 025 candidate comparisons and by 34.1% after centering, so the threshold began doing its job without being retuned. Banks under 100 embeddings keep raw cosine, since a mean over a handful of vectors is dominated by the vectors it must cancel. Set `MCP_RECALL_CENTERED=false` to score the way earlier releases did.
- **The launchd service runs through `libexec/service-wrapper`** instead of the binary directly (see Added, secrets). With no secrets file the wrapper execs exactly what ran before.
- **`make vet` now covers tagged code and formatting** — `go vet` runs for `./...`, `-tags=eval` and `-tags=corpus`, and depends on a new `fmt-check` target. CI calls `make vet` rather than a bare `go vet`.

### Fixed

- **Dry-run promotion forecast was wrong by 15× (T92)** — `sweep_archive --dry-run` counted every aged entry as a promotion while the live path routed most of them to review, so the forecast bore no relation to what a real run would do. The decision is now computed once by the same predicate both paths use, and only the write is conditional.
- **`OPENAI_API_KEY` could travel to a third-party endpoint (T100)** — the triple extractor fell back to it whenever its own key was unset, regardless of where its base URL pointed. The fallback survives only when the extractor addresses the same endpoint as the embeddings config; otherwise the extractor refuses to start with a message naming both variables.
- **The contradiction detector was removed (T105)** — it fired on a keyword appearing in *either* entry of a pair, which made any entry containing "superseded" a universal hub. Measured on a live bank: 13 of 13 findings false. The obvious repair — requiring the marker in both — inverts the semantics, since two entries that agree about a supersession share the vocabulary of agreement. The layer is gone rather than left misfiring; the reasoning sits where the code was.
- **A memory embedding could be silently dropped (T109)** — embeddings are stored as a binary blob, and the loader treated a leading `0x5B` as the start of a legacy JSON array. One byte in 256 collides, and 21 entries in a live bank were affected. The loader now tries JSON only when the blob actually parses as JSON, and falls through to the binary path otherwise.
- **`resolve_review_queue` filters didn't compose (T110)** — `tags` and `created_before` were applied to different candidate sets, so the combination returned nothing and the date cutoff was useless as a guard on a bulk operation.
- **`sweep_archive` stamped `last_verified_at` on entries nobody verified (T111)** — 45 canonical entries in a live bank looked freshly verified without ever passing `verify_entry`. The stamp is now written only when verification actually happened.
- **The evaluation harness had not compiled for four months (T112)** — the config struct was reshaped and the file behind `//go:build eval` kept naming the old fields. Build, vet and test all skip tagged files, so one tag hid broken code from every gate at once. Repaired, and the committed baseline was corrected from a recorded MRR of 0.918 to the actual 0.968 — at the 0.05 tolerance the stale figure let a real regression pass unnoticed.
- **Rune-aware truncation restored across surfaces (T90)** and **reload scope, per-action schema fidelity, and oversized-file splits (T91)** — the tail of the previous review round, unreleased until now.

## [0.9.4] - 2026-08-13

Correctness release, the second wave of the same review that produced 0.9.3.
Two defects stand out because both were silent by construction: a concurrent
writer could have its change overwritten with no error anywhere, and config
hot-reload had never worked through a `.env` file — SIGHUP and the file watcher
both reported success while applying the values from the previous load.

### Fixed

- **Concurrent writes to the same memory could be lost (T89 H2)** — `MarkOutdated` and `PromoteToCanonical` read the memory outside any lock and wrote it back under one, so a writer that committed in between was silently overwritten. `MarkOutdated` was worse still: four independent transactions (the update, temporal fields on the retired entry, temporal fields on the successor, the successor's reference count), any of which could fail on its own, leaving an entry marked superseded while its successor knew nothing about it. Nothing retried and nothing rolled back. Both operations now hold the write lock across the whole read-modify-write and re-read under it, and the supersession pair commits in a single transaction. A `superseded_by` pointing at an entry that is not in the store is still accepted — the retirement is real regardless — but a half-written pair is not.
- **Config hot-reload silently applied nothing (T89 H5)** — the `.env` loader pushed values into the process environment and skipped keys already set. On first load that is the intended precedence: a real environment variable beats the file. On reload every key was "already set" because the first load had set it, so nothing was re-read, the new config compared equal to the old one, and the RAG-restart hook never fired. `LoadFromFile`'s own documentation claimed it cleared and restored `MCP_*` variables; it never did. The file is now parsed into a map consulted only where the real environment is silent — same precedence, but a reload observes the current file. This also removes the `os.Setenv` race between the watcher and the signal handler (M14). Two knobs that reached their readers only through that environment export, `MCP_STEWARD_ENABLED` and `SEMA_MCP_SUPPRESS_REVIEW_QUEUE_WRITES`, now travel through the config; an explicit `MCP_STEWARD_ENABLED=false` in `config.env` would otherwise have started being ignored.
- **A mistyped duration silently became the default (T89 M12)** — `MCP_EMBEDDING_TIMEOUT=abc` resolved to 5s without complaint. Numbers and booleans have failed the load on a typo since the previous hardening round; durations were read as raw strings and parsed after that check, so they kept the old silent-default behaviour. They now fail the same way, naming the offending variable.
- **The hooks reader opened SQLite without a busy timeout (T89 M11)** — `context-inject` built its DSN by string concatenation as `path + "?_journal_mode=WAL&mode=ro"`. This driver's knob is `_pragma`, so `_journal_mode` was ignored and no busy timeout was set at all: the reader returned `SQLITE_BUSY` the instant it met a writer — the exact shape of the incident that made these pragmas centralised in the first place. It now opens through `dbutil.OpenSQLiteReadOnly`.
- **Canonical promotion left the sediment layer behind (T89 M3)** — an entry promoted to canonical stayed at whatever sediment layer it had. The sediment cycle proposes the move to `character` only from `semantic`, and only as a non-automatic suggestion, so anything promoted from `surface` or `episodic` remained evictable while claiming to be load-bearing knowledge. Promotion now moves both axes.
- **Long sweeps ignored cancellation (T89 M6)** — the archive sweep never checked its context, so shutdown had to wait out a full pass and the writes it kept issuing ran on an already-cancelled context. Both loops now stop.
- **`importance` was rejected on one path and silently replaced on another (T89 M9)** — `store_memory` refused an out-of-range value while `store_decision` and its siblings accepted the same argument and quietly substituted their own default. Absent still means "use the default"; invalid is now an error on both paths.
- **Four report paths loaded the whole embedding corpus (T89 M2)** — `StaleDeadEnds`, `ConflictsReport`, `ListCanonical` and `ProjectBankView` went through `List`, round-tripping every memory through SQL with its embedding blob to produce reports that read only cache-resident fields. They now use `ListLightweight`, as the steward paths already did.
- **The steward policy's `updated_at` lagged the database (T108)** — `SavePolicy` stamped the timestamp on its own copy, so callers kept the un-stamped original in memory and `steward_policy get` reported an older time than the stored row until the service restarted. That field is what an operator reads to answer "when was this policy last changed" — it was the evidence that diagnosed the policy-wipe defect in 0.9.3 — so a stale value there is worse than none.

## [0.9.3] - 2026-08-12

Stability release. One crash reachable from ordinary tool arguments, three data
races, and two steward defects that silently undid earlier operator decisions.
The two steward defects were found by a `/memory-cleanup` pass on a live store
whose queue had regrown for the fourth month running even though the tasks
covering that class were closed — the mechanism they fixed was intact, the
configuration behind it had been reset.

### Fixed

- **A short `superseded_by` crashed the server (T88 C1)** — `formatKnowledgeTimeline` sliced `SupersededBy[:8]` unguarded. That value is arbitrary caller input (`MarkOutdated` only trims it), so `mark_outdated(id, superseded_by="x")` followed by `knowledge_timeline` panicked with `slice bounds out of range`. The tool dispatcher had no `recover()`, and in stdio mode the server is the process: one bad argument ended the whole session along with every other in-flight call. Both halves are fixed — the three remaining fixed-width ID slices are now `min(8, len)`, and `invokeTool` converts a panic into a JSON-RPC internal error naming the tool, with the stack written to the diagnostics log.
- **Cache updates raced with lock-free readers (T88 H1)** — `Recall` and `ListLightweight` take `*cachedMemory` pointers from a snapshot that copies pointers, not values, and then read their fields without holding the cache mutex. `flushAccessStats` and `updateCachedField` (access counters, sediment promote/demote, reference recount) edited those published structs in place under `mu.Lock`, which excludes other writers but not those readers. Both paths now clone, mutate the clone, and swap it into the map. The race detector reported four races on the regression test before the change.
- **Archive-sweep scheduler read config while SIGHUP rewrote it (T88 H3)** — the background sweep goroutine read `TaskArchiveRoots`, `RootPath` and `TaskSlugPattern` field by field while `ReloadConfig` reassigned `s.config` under `ragMu.Lock`, in breach of the write-barrier contract documented on `ReloadConfig` itself. Background consumers now take one consistent `configSnapshot()`. Request-path readers of `s.config` are unchanged; migrating them belongs with the config rework.
- **Overlapping steward triggers filed duplicate inbox items (T88 H4)** — the interval loop and every `session_close` event each spawned their own `Service.Run` goroutine with nothing serializing them, so two scans walked the same corpus concurrently and each filed its own items (`CreateInboxItem` does not deduplicate). `Run` is now serialized, and the scheduler drops an overlapping background trigger rather than queueing a second identical full scan behind it. An explicit `steward_run` is never dropped, only serialized.
- **The last session checkpoint could be lost on shutdown (T88 M4)** — a checkpoint registered with the WaitGroup after `Close()` had already drained it and cancelled the context, so it ran on a dead context and wrote nothing. Registration now happens under the same mutex that guards `closed`: either `Close` waits for the checkpoint, or the checkpoint declines and `Close` flushes the session itself.
- **`steward_policy set` wiped every field the caller did not send (T103)** — the handler decoded the request into `Policy` and saved it whole. Every `Policy` field is a value type, so "absent" and "zero" are the same bit pattern: a one-field call reset all the others. On a live store this silently undid earlier operator decisions — `auto_merge_duplicate_min_confidence` to `0` (which disables auto-merge outright), `auto_delete_expired_working` to `false`, both importance cutoffs to `0` — with no error and no audit trail, and the queue it was meant to drain grew instead. `set` now takes a `PolicyPatch` whose fields are pointers, so an omitted field is left alone and an explicitly sent `0`/`false` is still applied; the read-modify-write is atomic, and the call returns the resulting policy. The tool description says patch-not-replace outright, since the old one said nothing and the caller reasonably assumed a patch.
- **A disabled kill-switch queued the work instead of cancelling it (T104)** — with `auto_delete_expired_working: false` the scanner still produced `delete_expired_working` actions and routed them to review. No resolution verb could carry that kind out, so the items could only be suppressed (a lie — they are not false positives) or deferred: 409 such items on the measured run. The switch now revokes the action, and a `delete` verb exists for the entries that legitimately reach review on importance grounds. `executeResolution` takes a typed `ResolutionAction`, and a test asserts every `ActionKind` a scan can produce has a verb able to execute it — the two vocabularies were previously unrelated strings that no compiler tied together.
- **`working_memory_ttl_days: 0` never meant "disabled" (T106)** — the field's comment said it did, while `EffectiveWorkingTTLDays` falls back to 14 for any value ≤0. On 2026-08-12 the contradiction — a policy reading `0` next to an inbox reporting "TTL: 14 days" — read as a counter bug and cost debugging time. The comments now state the actual behaviour: there is no value of this field that turns the TTL off.

### Added

- **Published to the official MCP Registry** — a `server.json` descriptor (`io.github.ipiton/agent-memory-mcp`) and a `publish-registry` job that runs after every tagged release. Namespace ownership is proven by GitHub OIDC, so no secret is involved; the job rewrites `version` in `server.json` from the tag before publishing, which keeps the tag the single source of truth. The release job's `contents: write` moved from workflow scope to job scope so the publish job can hold `id-token: write` without inheriting write access to the repository. The entry currently carries no `packages` block: the binaries ship as GoReleaser tarballs and a Homebrew tap, neither of which is a registry-supported package type, so registry clients get the repository pointer rather than an install command.

## [0.9.2] - 2026-08-11

Retrieval-correctness release. Two silent-loss bugs are fixed: whole markdown
knowledge bases were dropped from the RAG index without a log line, and merged
entries kept competing with the successor that replaced them. Both were reported
from a live deployment ([#19](https://github.com/ipiton/agent-memory-mcp/issues/19),
[#18](https://github.com/ipiton/agent-memory-mcp/issues/18) — thanks
@ch405canova-sudo).

### Fixed

- **Markdown outside `docs/` was silently dropped from the index (#19)** — `classifySourceType` admitted a `.md` file living outside `docs/` only when it carried a `# ` heading, but both call sites passed an empty body, so that branch could never fire; `supportedSourceType` gated the path before the file was read, so such files were skipped without ever being opened. No log line, no counter — the startup entry still echoed `index_dirs` faithfully while the index quietly lacked them. Measured on two live repos: a family-archive checkout lost all 58 files of `memory/` plus `AGENTS.md`; a sibling project lost 43 of 49 files under `memory-bank/`. Fixing the call sites revived the heading heuristic, which measurement showed to be the larger defect — only 6 of 58 memory files carried a `# ` line, because memory-style notes are frontmatter plus prose and carry no heading by design. Markdown reaching the classifier was already walked from a configured index dir, so its presence is the operator's intent; the heuristic is gone and exclusion stays where it belongs, in `IndexExcludeDirs` / `IndexExcludeGlobs`.
- **Superseded entries stayed in semantic recall (#18)** — `Recall` never looked at `superseded_by`, so an entry retired by a merge or by `MarkOutdated` stayed in the result set with its original, unchanged vector and kept competing with — often out-ranking — the successor that replaced it. `MarkOutdated` only downranks (importance capped at `0.25`), which decides nothing when the two embeddings are near-identical. Recall now skips such entries; `List`/`ListLightweight` still return them, so the temporal-history and maintenance views are unchanged. The successor is looked up in the cache rather than trusted: `Delete` does not clear `superseded_by` on predecessors, and an unconditional skip would bury an entry forever once its successor is deleted — an archived entry beats no entry at all. `MarkOutdated` **without** a successor keeps the previous downrank behaviour: nothing replaced the entry, so hiding it would just lose the knowledge.

### Added

- **Protocol-migration telemetry (step 1 of MCP-PROTOCOL-MIGRATION-2026-07-28)** — `handleInitialize` discarded its params and always answered with the server's own `protocolVersion`, so a client moving to a newer MCP revision was unobservable: we would learn about it from a failure rather than from telemetry. The requested version is now logged along with a match flag (logging only — the response is unchanged and a mismatch is not an error). Watching `initialize` alone turned out to be blind, though: measured against a live client, Claude Code reconnects to a restarted HTTP server by going straight to `tools/call` and never re-sends `initialize`, which dispatch accepts because no handshake is required. Unknown methods are therefore logged as the reliable tripwire — a client on revision 2026-07-28 calls `server/discover`, a mandatory RPC this server does not implement, which until now returned method-not-found silently. The line carries the protocol version from `_meta` when present, so it reports which revision the caller speaks rather than only that something unknown was asked for.

## [0.9.0] - 2026-07-10

Security, zero-ops, and architecture release. Canonical promotion is now guarded
against memory poisoning, every write is verified to have landed, and archived
tasks consolidate themselves with no configuration or manual runs. The MCP tool
surface can optionally collapse into grouped meta-tools to cut discovery token
cost, and the Round 3 review split the largest god-files/god-structs into
focused units with no behavior change.

**Upgrade note:** task-memory consolidation now runs automatically by default
(see the Changed section). To keep the previous behavior set
`MCP_ARCHIVE_SWEEP_ENABLED=false`.

### Added

- **Zero-ops task-memory consolidation (T63)** — closing a task used to leave its working memories (`Task started`, per-phase notes, `Session close`, auto-extracted review items) lingering forever, growing linearly and surfacing in recall for done tasks. A background loop (on by default, `MCP_ARCHIVE_SWEEP_INTERVAL`, default 1h) now sweeps archived tasks: durable entries (procedural, or importance ≥ 0.70) are promoted to canonical, the rest are marked outdated. It runs a first pass shortly after startup that also backfills any pre-existing archive. Zero configuration: with `MCP_TASK_ARCHIVE_ROOTS` unset it watches the `<MCP_ROOT>/tasks/archive` convention, and a missing directory is a silent no-op. A periodic ticker is used rather than an `fsnotify` watch so it needs no extra dependency, survives restarts, and cannot miss an archive move that happened while the service was down. Disable with `MCP_ARCHIVE_SWEEP_ENABLED=false`.
- **Opt-in MCP tool grouping (T67)** — every MCP client loads the full JSON schema of every tool at `initialize`, so ~40 tools occupy tens of KB of context before the first message. Set `MCP_TOOL_GROUPING=true` to collapse the core toolset into grouped meta-tools (`repo`/`memory`/`memory_admin`/`engineering`/`search`/`session`) that dispatch by a required `action` discriminator, dropping the default surface from 41 tools to 8 (~42% smaller schema payload). It is purely a discovery-surface transform: `tools/call` always accepts both the grouped form (`memory` + `action=store`) and the legacy name (`store_memory`) regardless of the flag, so existing clients never break. Administrative and steward tools stay individual.
- **Read-after-write verification on the store path (T75)** — a silent write failure (a driver that reports success but persists nothing) would lose a memory without any signal, mirroring the scheduled-cross-agent-injection blind spot. Every writer funnels through `Store.Store`, which now checks `RowsAffected()==1` on insert and reads the row back by id before trusting its cache; a missing row is a hard error, not a silent success. Cost is one indexed primary-key select per write.
- **Memory-poisoning defense: provenance + promotion gate (T77)** — `PromoteToCanonical` used to canonicalize any record unconditionally, making the auto-promote pipelines (steward auto-run, archive sweep) an attack vector: a planted conversational record could become fully-trusted canonical knowledge. Promotion now carries provenance (`conversational` / `verified` / `external`, defaulting to conversational) and enforces a gate — an automated caller cannot canonicalize a conversational-origin record (`ErrPromotionRequiresVerification`); only a human/verified path or an already-trusted provenance passes. The archive sweep routes gated records to the review queue instead of hard-failing, so trusted-provenance promotions still proceed.
- **Bulk-cleanup API gaps closed (T81)** — `resolve_review_queue` gains `created_before` and `kind` filters (a large legacy backlog previously required a raw SQL workaround), a cross-run reconcile auto-resolves inbox items whose targets have all been deleted, and `merge_duplicates` is now idempotent and skips already-missing duplicate ids instead of erroring.

### Changed

- **Consolidation now runs by default (T63)** — the `end_task` and `sweep_archive` tools default `auto_promote=true` (previously `false`), and the background sweep is on by default. The T77 provenance gate keeps this safe: conversational-origin memories are routed to review, never auto-canonicalized. This is a behavior change on upgrade — opt out with `MCP_ARCHIVE_SWEEP_ENABLED=false` and/or an explicit `auto_promote=false`.
- **`steward_inbox_resolve` applies the resolution (T73)** — resolving an inbox item used to only mark it resolved without performing the chosen action (merge/mark_outdated/promote/…). It now executes the action and then records the resolution.
- **Content-free session-hook payloads dropped (T80)** — the SessionEnd / checkpoint hook paths could persist empty "stub" records that carried metadata but no usable content, cluttering the store. Such payloads are now discarded at the shared `hooks.Check` layer (both CLI and server callers).
- **Contradiction detector suppresses more false-positive classes (T82)** — beyond the T72 terminal-pair guard, `hasContradictionSignals` now also suppresses non-terminal kinship (`Pattern:`/`Lesson:` vs a terminal record in the same context), sequential periodic reviews (`Strategy review …`), and task-lifecycle pairs (`Task started:` ↔ `Task complete:` for the same subject).
- **Round 3 architecture split (T57, T58)** — internal refactor with no behavior change: the config god-struct was split into 12 focused sub-structs, `MCPServer.New` decomposed into `bootstrap.go` init helpers, `tools_registry.go` split into per-category schema/dispatch/args files, and the vectorstore/rag/sessionclose god-files broken up. Testability hardening added time injection and narrow store interfaces. A resolved-config golden and a tool-list count guard pin the equivalence.

### Documentation

- **Lifecycle consolidation policy (T62)** — new `docs/concepts/lifecycle.md` documents lifecycle-state derivation, the archive-sweep `decide` policy (working/procedural, 0.70 promotion threshold, type/importance split), the provenance gate on the auto path, idempotency/symlink/traversal guards, and the zero-ops trigger cadence.

## [0.8.9] - 2026-07-01

Steward hygiene release. On an un-maintained store the maintenance steward now
self-cleans instead of growing its review inbox: low-value stale and expired
working entries auto-apply under importance/canonical guards, and the legacy
`Task complete: X` ↔ `Session close / X` record pairs are no longer mis-flagged
as contradictions.

### Changed

- **Contradiction detector suppresses the T71 dual-write class (T72)** — after T71 stopped writing new `Task complete: X` / `Session close / X` duplicate pairs, the steward still flagged ~57 legacy pairs as contradictions: a `/finalize` task-complete summary naturally mentions words like "removed", "switched to" or "previously", tripping the content-signal check even though both records describe the same finished task. `hasContradictionSignals` now short-circuits when both members of a same-subject pair are terminal episodics (a session summary, or a `Task complete:` / `Session close` record), before any keyword or lifecycle signal fires. The terminal-record classification moved from `sessionclose` into a shared `memory.IsTerminalRecord` helper so the T71 idempotent writer and this guard apply the exact same rule.
- **Steward defaults tuned for self-cleaning (T72)** — `DefaultPolicy` now ships aggressive auto-cleanup: `auto_mark_stale_beyond_days` `0 → 30` and `working_delete_importance_cutoff` `0.5 → 0.6` (the effective fallback moves to `0.6` too). New installs, and stores whose persisted policy leaves these fields unset, self-clean out of the box. Auto-apply still only runs in auto/scheduled mode (the default is manual), so the classification change alone deletes nothing. A policy persisted with explicit `0.5`/`0` values keeps them — enabling self-clean there is a one-time `steward_policy` update.

### Added

- **Importance/canonical guards on auto-apply (T72)** — the `mark_stale` auto-apply path (`auto_mark_stale_beyond_days > 0`) now only auto-applies entries below the new `auto_mark_stale_importance_cutoff` policy field (default/fallback `0.6`) that are not canonical; high-importance or canonical knowledge always routes to review even when well past the threshold. Marking stale is reversible (a lifecycle flag), so this is safe to auto-run. The `delete_expired_working` scanner gains a canonical safety-net — a canonical entry never auto-deletes, even if it somehow carries the working type. Unit tests cover terminal-pair suppression (including the keyword case a non-terminal pair still flags) and the stale auto-apply guards (low-importance auto-applies; high-importance and canonical stay in review).

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
