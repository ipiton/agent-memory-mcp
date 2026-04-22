# Memory Sedimentation (T48)

Status: Experimental. Foundation landed; transition thresholds run in
"degraded mode" until ~2 months of production access-pattern data validate
them.

## Overview

Sedimentation adds a **layer** dimension to every memory, orthogonal to the
existing `type` (episodic/semantic/procedural/working) field. The layer
governs **retrieval priority**:

| Layer       | Retrieval behaviour                                              |
|-------------|------------------------------------------------------------------|
| `surface`   | Session/task-scoped. Visible only when `filters.Context` matches the memory's `Context`. Default for new memories. |
| `episodic`  | Included in top-K with a small score penalty (−0.05).            |
| `semantic`  | Neutral — standard scoring.                                      |
| `character` | Always surfaced (+0.15 score, bypasses `minScore` cutoff). Intended for load-bearing facts that must always be in the agent's working set. |

Memories migrate through the ladder via the `sediment-cycle` job:
`surface → episodic → semantic → character`, with optional demotion via
`character → semantic` for long-idle canonical entries.

### Why not replace `type`?

`type` describes the *nature* of the memory (event, fact, pattern, scratch).
`sediment_layer` describes *how load-bearing it is*. A single episodic
incident can age into `semantic` and later `character` without losing its
identity as an event record. Keeping both dimensions orthogonal preserves
backward compatibility with every tool that filters by `type`, and leaves
the door open for future orthogonal axes (trust, owner, etc.).

## Architecture

### Schema

New column on the `memories` table:

```sql
sediment_layer TEXT NOT NULL DEFAULT 'surface'
CREATE INDEX idx_memories_sediment_layer ON memories(sediment_layer);
```

Migration is idempotent and runs automatically on server startup
(`ensureMemorySchema`). On first run against an existing DB:

1. `ALTER TABLE memories ADD COLUMN sediment_layer TEXT NOT NULL DEFAULT 'surface'`.
2. Backfill pass — for each row, derive the layer from `(type, metadata)`:
   - `working` → `surface`
   - `episodic` → `episodic`
   - `knowledge_layer=canonical` OR `canonical=true` OR `lifecycle_status=canonical` → `character`
   - everything else → `semantic`
3. Create the index.

The backfill is **Go-side** (not SQL) because metadata is a JSON blob and
the `json_extract` behaviour across modernc/sqlite versions we support has
edge cases around empty/null metadata. At 100–1000 rows/ms this is well
within startup budget.

**Known bound**: the migration loads all rows into memory during backfill.
Validated up to ~100k rows; larger DBs should chunk via a future enhancement.

Running `NewStore` twice on the same DB is a no-op: the column-exists check
skips the backfill, and the new rows already carry the correct layer from
`Validate()`.

### Transition rules (`internal/memory/sediment.go`)

All rules are **pure functions**. `Decide(m, policy)` returns a
`*SedimentTransition` proposal or `nil`. Application is the caller's
responsibility.

| Current     | Next        | Condition                                                              | Auto  | Reason                |
|-------------|-------------|------------------------------------------------------------------------|-------|-----------------------|
| `surface`   | `episodic`  | `age >= 7d` AND `access_count >= 1`                                    | yes   | `aged-surface`        |
| `episodic`  | `semantic`  | `age >= 30d` AND `access_count >= 3`                                   | no    | `aged-episodic`       |
| `semantic`  | `character` | `referenced_by_count >= 20` OR `lifecycle_status == canonical`         | no    | `canonical-promotion` |
| `character` | `semantic`  | `accessed_at` older than 90d                                           | no    | `character-decay`     |

`Auto=true` transitions are applied directly by `sediment-cycle`.
`Auto=false` transitions become `review_queue_item` memories routed through
the existing review pipeline — humans (or future automation) decide whether
to apply them via the `promote_sediment` / `demote_sediment` MCP tools.

Review-queue items (`record_kind=review_queue_item`) are themselves excluded
from `Decide` to avoid the cycle promoting its own bookkeeping records.

### Retrieval weighting (`internal/memory/read.go`)

`Recall` applies layer weighting **only when `MCP_SEDIMENT_ENABLED=true`**.
When the flag is off, the pre-T48 scoring path runs unchanged (the T43 eval
baseline verifies this: HitRate@5=1.0, MRR=0.918 on both flag states).

When the flag is on:

```
weightedScore = score * (baseW + importance*importanceW + confidence*confidenceW)
              + trust.FreshnessScore * freshnessW
              + layerBoost

layerBoost = {
  character:  +0.15  (AND bypasses minScore cutoff so it always surfaces)
  semantic:    0
  episodic:   -0.05
  surface:     excluded unless filters.Context == m.Context
}
```

### Sediment cycle (`internal/memory/sediment_cycle.go`)

A batch job that scans memories and applies transitions. Modeled on the T47
archive-sweep pattern:

1. `List()` all memories (optionally filtered by `SinceDays`).
2. For each memory: `Decide(m, policy)`.
3. If `tr == nil` → skip.
4. If `tr.Auto && !DryRun` → `PromoteSediment(ctx, id, tr.To)`.
5. If `!tr.Auto && !DryRun` → write a `review_queue_item` memory with:
   - `record_kind = review_queue_item`
   - `review_required = true`
   - `review_source = sediment_cycle`
   - `review_target_memory_id = <target ID>`
   - `review_target_layer = <proposed layer>`
   - Tag: `sediment-cycle`
6. Idempotency: before writing a review-queue item, the cycle looks up
   existing ones in the same `Context` with
   `(review_target_memory_id, review_source=sediment_cycle)`. Already-
   resolved items (`review_required=false`) are skipped so a re-run can
   re-propose.

### Invariants

1. **Additive**: T48 does not rename or repurpose any existing column, metadata key, or `type` value.
2. **Idempotent migration**: `ensureMemorySchema` is safe to re-run.
3. **Lock order**: `PromoteSediment`/`DemoteSediment` both take `writeMu` before `mu` per the existing Store convention.
4. **Cache coherence**: all cache updates go through `cacheSetLocked` (or a direct cache mutation under `mu.Lock()`).
5. **Feature-flag dormant**: with `MCP_SEDIMENT_ENABLED=false`, retrieval and scoring are byte-identical to pre-T48 — only the column and backfill are live.
6. **Pure `Decide`**: transition proposals never perform I/O.

## Operations

### Feature flag

```
MCP_SEDIMENT_ENABLED=false   # default — column + backfill only, no retrieval change
MCP_SEDIMENT_ENABLED=true    # enables layer-aware Recall
```

Enabling/disabling the flag at runtime requires a server restart (the flag
is stored atomically on the `Store` and set once at startup from
`cfg.SedimentEnabled`).

### Scheduling

By default, the sediment cycle runs only on explicit invocation (CLI
`sediment-cycle` or the `sediment_cycle` MCP tool).

To run automatically in the background, set:

```
MCP_SEDIMENT_ENABLED=true
MCP_SEDIMENT_SCHEDULE_INTERVAL=1h
```

The scheduler ticks at the configured interval and runs `RunSedimentCycle`
with `DryRun=false, Limit=0`. Results are logged at Info level with
counters (`auto_applied`, `review_queued`, `errors`) and elapsed time.
Errors are logged at Warn and do not crash the server.

The scheduler starts with the server process and stops gracefully on
SIGINT/SIGTERM. `MCP_SEDIMENT_SCHEDULE_INTERVAL=0` (default) keeps
scheduling disabled even when `MCP_SEDIMENT_ENABLED=true`.

For production, 1h is a reasonable starting interval. Monitor the Info
log output: if `auto_applied` grows steadily without `review_queued`
catching up, inspect pending promotions via
`project_bank_view(view=sediment_candidates)` and resolve them manually.

### CLI

```
agent-memory-mcp sediment-cycle [--dry-run] [--since-days N] [--limit N] [--verbose] [--json]
```

- `--dry-run`: show proposed transitions without writing. Under `--dry-run`,
  `AutoApplied` counts transitions that *would* be applied, not mutations
  actually performed. `ReviewQueued` likewise counts review items that
  *would* be written.
- `--since-days N`: only consider memories **older** than N days (`CreatedAt <= now - N*24h`).
  0 = all. Useful for limiting cycle scope to stable memories; the age-based
  transition rules (surface≥7d, episodic≥30d, character-decay≥90d) only
  fire on older memories, so this flag never drops legitimate candidates.
- `--limit N`: cap transitions per run (0 = no cap).
- `--verbose`: print each transition.
- `--json`: emit the raw `SedimentCycleResult`.

Exit code is non-zero if the cycle recorded any per-memory partial
failures. Counters reflect only successful writes.

Example:

```
$ agent-memory-mcp sediment-cycle --dry-run --verbose
Sediment cycle (dry-run):
- Auto applied: 12
- Review queued: 3
- Skipped: 148

Transitions:
- 4f2e...: surface → episodic (aged-surface, auto)
- 9a1b...: semantic → character (canonical-promotion, review)
- ...
```

### MCP tools

- `promote_sediment(id, target_layer)` — apply a specific transition, e.g. after a reviewer accepts a sediment-cycle proposal.
- `demote_sediment(id)` — move one layer closer to surface. No-op at surface.
- `sediment_cycle(dry_run, since_days, limit)` — run the cycle from the MCP surface.
- `project_bank_view(view="sediment_candidates")` — enumerate pending sediment review-queue items for a reviewer.

All four require `memory_store` to be available; they return RPC errors if it is not.

### Monitoring the cycle

Until production access-pattern data is available, operate the cycle in
`--dry-run` mode initially to gauge transition volume. Watch for:

- **Unexpected `semantic → character` volume.** If many memories qualify via
  `referenced_by_count`, the threshold (default 20) may be too low for your
  corpus. Tune `SedimentPolicy.SemanticToCharacterRefs`.
- **Thrashing**: a memory promoted `character → semantic → character` in
  consecutive weekly runs. Indicates threshold boundaries are too close to
  the actual access pattern. Raise thresholds or add hysteresis.
- **Error rate**: any non-empty `SedimentCycleResult.Errors`. Each entry
  names a specific memory ID and the underlying write error.

### Degraded-mode caveat

T48 shipped without 2 months of production access-pattern history. The
thresholds (7d, 30d, 90d, 20 refs, access_count ≥ 1/3) are plausible defaults,
not empirically tuned. Expect to revisit them once the cycle has produced
~8 weeks of data. All thresholds live in `SedimentPolicy` and are wired
through the CLI/MCP tool args for easy override.

## Testing

- Pure rule tests: `internal/memory/sediment_test.go` covers every branch of `Decide`, `NormalizeSedimentLayer`, `IsValidSedimentLayer`, `DemoteOneStep`, `BackfillSedimentLayer`.
- Integration tests: `internal/memory/sediment_integration_test.go` covers schema migration (legacy DB → backfill), idempotency, promote/demote, cycle dry-run, cycle auto-apply, cycle review-queue routing, and retrieval boosts under both flag states.
- MCP tests: `internal/server/tools_sediment_test.go` covers the three new tools end-to-end.
- Dispatch-table completeness: `TestDispatchTableCompleteness` now includes the three new tool names.
- Eval baseline: `go test -tags=eval ./internal/rag/eval/` must stay green. Current baseline: `HitRate@5=1.0000, MRR=0.9180`.
