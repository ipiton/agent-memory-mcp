# Memory Lifecycle & Task-Archive Consolidation

This document describes how memories move through lifecycle states and how the
**archive sweep** consolidates task-scoped working memories once a task is
closed. It is the reference for the T62 consolidation policy.

## Lifecycle states

A memory's lifecycle status is derived, never stored redundantly. It is resolved
by `memory.LifecycleStatusOf` from a single metadata source of truth, evaluated
in priority order (first match wins):

| # | Source (metadata) | Resulting status |
|---|---|---|
| 1 | `canonical=true` or `knowledge_layer=canonical` | `canonical` |
| 2 | explicit `lifecycle_status` | that value |
| 3 | `archived=true` | `superseded` |
| 4 | `status` (mapped) | matching lifecycle |
| — | fallback: `type=working` | `draft` |
| — | fallback: any other type | `active` |

States:

- **draft** — freshly captured working memory, not yet consolidated.
- **active** — durable knowledge in normal use (semantic/procedural/episodic).
- **canonical** — vetted, authoritative. Wins retrieval ranking and trust checks.
- **superseded** — replaced by a newer memory (`superseded_by` points at it).
- **outdated** — no longer valid; retained for audit but demoted in retrieval.

## Why consolidation exists

Every `/end-task` produces 4–7 working memories (`Task started`, per-phase notes,
`Session close`, plus auto-extracted `Review queue / <step>` entries). Without a
consolidation step these linger as `draft`/`active` forever and keep surfacing in
`recall_memory` / `semantic_search` for tasks that are already closed — linear
context-noise growth proportional to the number of tasks ever closed.

The archive sweep is the fix-class solution: when a task's directory moves under
an archive root, its working cohort is consolidated in one pass.

## The consolidation policy (`decide`)

The sweep enumerates memories of type **working** and **procedural** whose
`Context` equals the task slug, and applies the first matching rule:

| Condition | Action |
|---|---|
| not working/procedural type | **skip** (`skipped_non_working`) |
| `record_kind=review_queue_item` | **skip** — workflow records, keeps the sweep idempotent |
| carries the keep tag (`keep-after-archive`) | **skip** (`skipped_keep_tag`) |
| already `outdated` | **skip** (`already_outdated`) |
| `type=procedural` **or** `importance ≥ threshold` (default **0.70**) | **promotion candidate** |
| otherwise | **outdated** (reason: `task archived: <slug>`) |

Rationale for the type/importance split — this is the "re-promote canonical vs
mark outdated" policy decision:

- **Procedural** memories encode reusable patterns/lessons; they outlive the task
  that produced them, so they are always promotion candidates.
- **High-importance** working memories (≥ 0.70) likely captured a durable fact or
  decision worth keeping — promote rather than discard.
- **Everything else** is task-scoped scaffolding (phase notes, status pings) that
  has no value once the task is closed — mark `outdated`.

The threshold is not hardcoded: pass `PromotionThreshold` (or the sweep uses
`DefaultPromotionThreshold = 0.70`).

## What happens to a promotion candidate

The `AutoPromote` flag chooses between two handlings:

- **`auto_promote=false` (default, backward-compatible)** — a `review_queue_item`
  memory is created for human triage. The candidate is *not* mutated. Idempotent:
  re-running the sweep does not create a second review item for the same target.
- **`auto_promote=true`** — the candidate is promoted to `canonical` in place via
  `PromoteToCanonical(..., verified=false)`, owner `archive-sweep`. This keeps the
  inbox from growing proportionally to closed tasks (the original T62 pain).

### Poisoning defense on the auto path (T77)

`auto_promote=true` does **not** blindly canonicalize. `PromoteToCanonical`
enforces a provenance gate: a **conversational-origin** memory
(`provenance=conversational`, the default for anything captured from a session)
cannot be auto-canonicalized and returns `ErrPromotionRequiresVerification`. The
sweep catches this and *falls back to a review-queue item* — never promoting nor
hard-failing. Only memories already marked `provenance=verified` / `external` take
the direct-promotion path. This prevents a compromised or hallucinated session
note from silently entering canonical knowledge.

## Invocation

Both entry points accept `auto_promote` (default `false`) and support `dry_run`:

- **`sweep_archive`** — enumerates every slug under the configured archive roots
  (`MCP_TASK_ARCHIVE_ROOTS`). Bulk / periodic path.
- **`end_task`** — one slug, validated to exist as a subdirectory under a root
  (defense-in-depth against path traversal). Explicit per-task path.

`dry_run=true` reports the exact actions and counters (outdated / promotion
candidates / promoted / skipped) without any writes — always the safe first step,
especially for a legacy backfill.

## Idempotency & safety

- **Idempotent** — already-`outdated` memories and existing review items are
  skipped, so re-running yields zero new actions.
- **Per-slug serialization** — a `sync.Mutex` per slug prevents a concurrent
  `sweep_archive` + `end_task` from double-creating review items.
- **Symlink guard** — archive roots are stat'd with `Lstat`; a symlink under a
  root is treated as non-existent so it cannot redirect the sweep to an unrelated
  slug.
- **Partial-failure reporting** — per-memory write failures are collected in
  `Errors` and surfaced (CLI exits non-zero, MCP response lists them); counters
  reflect only successful writes.

## Legacy backfill (one-time)

For a corpus that accumulated working memories *before* the sweep existed:

1. Run `sweep_archive(dry_run=true, auto_promote=true)` and read the counters —
   confirm the promoted/outdated split matches expectations.
2. Re-run with `dry_run=false` to apply. The type/importance policy above governs
   which entries become canonical and which become outdated.
3. Stale `review_queue_item` entries from earlier (pre-AutoPromote) sweeps are
   *skipped* by the sweep and are cleaned up separately — see the cross-run inbox
   reconcile (T81) and the follow-up work in **T63**.

## Not covered here (T63)

Autonomous triggering (an `fsnotify` watch on `tasks/<slug>/ → tasks/archive/<slug>/`
moves) and the operational legacy backfill against a live consumer corpus are
tracked as **T63**. This document covers the policy those mechanisms apply.
