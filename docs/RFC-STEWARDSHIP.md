# RFC: Stewardship Layer

**Status**: proposed
**Date**: 2026-03-25
**Author**: Vitalii Semenov

---

## Goal

Transform agent-memory-mcp from a memory storage + retrieval service into an **always-on stewardship service for living engineering knowledge**.

Memory is not just stored and searched — the service actively maintains knowledge quality: detects drift, resolves conflicts, verifies freshness, and surfaces actionable review items. All of this is transparent, explainable, and policy-governed.

## What We Already Have

| Capability | Status | Implementation |
|---|---|---|
| Typed memory (4 types) | Done | `memory.Store` + SQLite |
| Canonical knowledge layer | Done | promote, lifecycle, trust metadata |
| Duplicate detection | Done | `merge_duplicates`, `conflicts_report` |
| Session consolidation | Done | `sessionclose/` extract→plan→apply pipeline |
| Review queue | Done | `resolve_review_item`, `resolve_review_queue` |
| Trust scoring | Done | confidence, freshness, knowledge_layer, source_type |
| Background session tracker | Done | idle timeout, checkpoints, auto-close |
| Hybrid RAG retrieval | Done | semantic + keyword + source-aware ranking |

## What's Missing

1. **No orchestrated maintenance cycle** — duplicate/conflict/stale detection are separate manual tools with no unified run
2. **No scheduled maintenance** — session tracker runs background jobs, but nothing else does
3. **No drift detection** — memory is never compared against live sources (repo, docs, changelog)
4. **No verification model** — no way to track when/how knowledge was last verified
5. **No stewardship inbox** — review queue exists but is session-scoped, not a global actionable queue
6. **No temporal model** — no valid_from/valid_until, no supersession chains, no as-of retrieval
7. **No unified policy** — session close has safe_auto_apply logic, but there's no global stewardship policy

## Anti-Goals

- **Not a UI project.** All features are MCP tools + JSON endpoints. No dashboards, no web apps.
- **Not multi-tenant.** Single-instance stewardship for one knowledge base.
- **Not AI-powered rewriting.** Stewardship detects and surfaces problems. It does not rewrite or synthesize knowledge — that's the agent's job.
- **Not a replacement for session close.** Session close handles ingest-time consolidation. Stewardship handles ongoing maintenance of the full knowledge base.
- **Not a cron system.** The internal scheduler runs maintenance jobs. It is not a user-facing task scheduler.

---

## Milestone 1 — Stewardship Foundation

**Goal**: One command runs a full maintenance cycle. Results are transparent and policy-governed.

### Tools

#### `steward_run`

Orchestrates a complete maintenance cycle:

1. Scan for duplicate candidates (reuse `conflicts_report` logic)
2. Scan for conflicting entries
3. Scan for stale entries (not verified within threshold)
4. Scan for canonical promotion candidates (repeated, high-confidence, conflict-free)
5. Update freshness scores
6. Generate report

```
steward_run(
  scope:   "full" | "duplicates" | "conflicts" | "stale" | "canonical",
  dry_run: bool,          // default true — report only, no changes
  context: string,        // optional: limit to context
  service: string,        // optional: limit to service
)
→ StewardReport
```

**Behavior with `dry_run: false`:**
- Actions that match `safe_auto_apply` policy are applied immediately
- All other actions go to the stewardship inbox as `review_required`
- Applied actions are logged in the report with full rationale

#### `steward_report`

Returns the last stewardship report or a specific one by ID.

```
steward_report(
  run_id: string,         // optional: specific run, default latest
  format: "summary" | "full" | "json",
)
→ {
  run_id, started_at, completed_at, scope, dry_run,
  stats: { scanned, duplicates_found, conflicts_found, stale_found, promotion_candidates, actions_applied, actions_pending_review },
  actions: [{ kind, handling, state, target_ids, rationale, evidence, confidence }],
  errors: [{ phase, message }],
}
```

#### `steward_policy`

Get or update stewardship policy.

```
steward_policy(
  action: "get" | "set",
  policy: {
    mode: "off" | "manual" | "scheduled" | "event_driven",
    schedule_interval: duration,       // e.g. "24h", "6h"
    event_triggers: [                  // events that trigger a run
      "session_close",
      "bulk_ingest",
      "memory_count_threshold",
    ],
    thresholds: {
      duplicate_similarity: float,     // default 0.85
      stale_days: int,                 // default 30
      canonical_min_confidence: float, // default 0.80
      canonical_min_evidence: int,     // default 2 (source-backed observations)
    },
    auto_apply_rules: {
      merge_exact_duplicates: bool,    // default false
      mark_stale_beyond_days: int,     // default 0 (disabled)
      refresh_freshness_scores: bool,  // default true
    },
  }
)
```

Policy is persisted in SQLite alongside memory data.

#### `steward_status`

Current state of the stewardship system.

```
steward_status()
→ {
  policy_mode,
  last_run: { run_id, started_at, duration, stats_summary },
  pending_review: int,
  next_scheduled_run: timestamp | null,
  jobs_in_queue: [{ type, state, queued_at }],
}
```

### Internal: Background Scheduler

Extend the existing `sessionTracker` pattern to a general-purpose internal scheduler.

```go
type Scheduler struct {
    jobs    map[string]*Job
    ticker  *time.Ticker
    store   *memory.Store
    policy  *StewardPolicy
}

type Job struct {
    Type      JobType   // steward_run, drift_scan, refresh_index, canonical_review
    State     JobState  // queued, running, completed, failed, partial
    CreatedAt time.Time
    StartedAt time.Time
    Result    *StewardReport
}
```

Jobs triggered by:
- Schedule (configurable interval)
- Events (session close, memory count threshold)
- Manual (`steward_run`)

### Config

New config fields:

```
STEWARD_MODE=manual                    # off|manual|scheduled|event_driven
STEWARD_SCHEDULE_INTERVAL=24h
STEWARD_DUPLICATE_THRESHOLD=0.85
STEWARD_STALE_DAYS=30
STEWARD_CANONICAL_MIN_CONFIDENCE=0.80
```

### Acceptance Criteria

- [ ] `steward_run` with `dry_run: true` returns a report without modifying any data
- [ ] `steward_run` with `dry_run: false` applies safe actions and queues the rest
- [ ] `steward_report` returns structured report with per-action rationale
- [ ] `steward_policy` persists and loads policy from SQLite
- [ ] `steward_status` shows last run, pending review count, next scheduled run
- [ ] Scheduler runs steward_run on configured interval when mode=scheduled
- [ ] Event trigger fires steward_run after session_close when mode=event_driven
- [ ] All applied actions have audit trail (who, when, why, confidence)

---

## Milestone 2 — Knowledge Verification & Drift Detection

**Goal**: Memory is checked against live sources. Stale, drifted, or unverified knowledge is surfaced.

### Tools

#### `drift_scan`

Compares memory entries against source-of-truth:
- Repo files (paths referenced in memory)
- Indexed documents (RAG corpus)
- Changelog entries
- Runbook procedures

```
drift_scan(
  scope:   "all" | "canonical" | "decisions" | "runbooks",
  context: string,
  service: string,
)
→ {
  scanned: int,
  drifted: [{ memory_id, title, drift_type, evidence, confidence, suggested_action }],
  unreachable_sources: [{ memory_id, source_path, reason }],
}
```

**Drift types:**
- `source_changed` — referenced file/doc has changed since memory was last verified
- `source_missing` — referenced path/entity no longer exists
- `content_contradicts` — memory content contradicts current source
- `stale_unverified` — memory not verified within threshold

#### `verification_candidates`

Returns memories that need verification, ranked by urgency.

```
verification_candidates(
  limit: int,             // default 20
  scope: "all" | "canonical" | "decisions" | "runbooks",
  min_age_days: int,      // only entries older than N days
)
→ [{
  memory_id, title, type, last_verified_at, age_days,
  reason: "never_verified" | "stale" | "source_changed" | "low_confidence" | "contradicted",
  urgency: "high" | "medium" | "low",
  suggested_action: "verify" | "update" | "mark_outdated" | "investigate",
}]
```

#### `verify_entry`

Explicitly mark a memory as verified.

```
verify_entry(
  memory_id: string,
  method: "manual" | "source_check" | "repo_scan" | "agent_verified",
  status: "verified" | "verification_failed" | "needs_update",
  note:   string,         // optional: what was checked
)
```

### Data Model Changes

Add to memory metadata:

```
verified_at:          timestamp    // last verification time
verified_by:          string       // who/what verified
verification_method:  string       // manual, source_check, repo_scan, agent_verified
verification_status:  string       // verified, failed, needs_update, unverified
```

### Canonical Health

Extend `steward_run` with canonical-specific diagnostics:

- Stale canonical (not verified within 2× stale threshold)
- Conflicting canonical (multiple canonical entries for same topic)
- Unverified canonical (promoted but never verified against source)
- Orphan canonical (source memory deleted or outdated)
- Low-support canonical (single observation, no corroboration)

Surface via `steward_report` under a `canonical_health` section.

### Integration with steward_run

`drift_scan` becomes an optional phase in `steward_run`:

```
steward_run(scope: "full")  // now includes drift scan
steward_run(scope: "drift") // drift scan only
```

### Acceptance Criteria

- [ ] `drift_scan` detects source_changed when a referenced file has newer mtime than memory's verified_at
- [ ] `drift_scan` detects source_missing when referenced paths don't exist
- [ ] `verification_candidates` ranks never-verified canonical entries as high urgency
- [ ] `verify_entry` updates verification metadata and refreshes freshness score
- [ ] Canonical health section in steward_report shows stale/conflicting/unverified canonical
- [ ] drift_scan works with repo files, RAG-indexed documents, and changelog entries

---

## Milestone 3 — Governed Automation & Stewardship Inbox

**Goal**: The stewardship inbox is the single place for all review-required actions. Safe actions auto-apply by policy.

### Tools

#### `steward_inbox`

Global review queue for all stewardship actions (not just session-close).

```
steward_inbox(
  status:  "pending" | "resolved" | "all",
  kind:    string,           // filter by action kind
  limit:   int,
  sort_by: "urgency" | "created_at" | "confidence",
)
→ [{
  item_id, source_run_id, kind, handling, state,
  title, evidence, confidence, urgency,
  recommended_action, safe_flag: bool,
  created_at,
}]
```

**Action kinds:**
- `duplicate_candidate`
- `contradiction_candidate`
- `stale_canonical`
- `outdated_procedural`
- `unverified_runbook`
- `source_mismatch`
- `missing_source_link`
- `superseded_candidate`
- `promotion_candidate`
- `drift_detected`

#### `steward_inbox_resolve`

Resolve one or more inbox items.

```
steward_inbox_resolve(
  item_ids: [string],
  action:   "merge" | "mark_outdated" | "mark_superseded" | "promote" | "verify" | "suppress" | "defer",
  note:     string,
)
```

### Event-Driven Triggers

Stewardship runs fire automatically on events:

| Event | Trigger |
|---|---|
| `session_close` | After session consolidation completes |
| `bulk_ingest` | After ≥N memories stored in <T time window |
| `memory_count_threshold` | When total memories cross configured thresholds (100, 500, 1000) |
| `conflict_spike` | When conflicts_report finds ≥N new conflicts |

Configurable via `steward_policy`.

### Auto-Apply Rules

Policy-driven safe automation:

| Rule | Default | What it does |
|---|---|---|
| `refresh_freshness_scores` | on | Recalculate all freshness scores |
| `merge_exact_duplicates` | off | Auto-merge entries with similarity ≥0.95 |
| `mark_stale_beyond_days` | off | Auto-mark entries as stale after N days unverified |
| `downrank_unverified_canonical` | off | Lower confidence of canonical entries not verified in 2× stale_days |

### Audit Trail

Every stewardship action (auto or manual) is logged:

```go
type StewardAuditEntry struct {
    ID         string
    RunID      string       // steward_run that triggered this
    Action     string       // merge, mark_outdated, promote, etc.
    TargetIDs  []string     // affected memory IDs
    Handling   string       // safe_auto_apply, manual
    Rationale  string       // why this action
    Evidence   []string     // supporting signals
    Confidence float64
    AppliedAt  time.Time
    AppliedBy  string       // "steward_auto" | "user" | agent_id
}
```

Stored in a new `steward_audit` SQLite table.

### Acceptance Criteria

- [ ] `steward_inbox` aggregates items from steward_run, drift_scan, and session_close
- [ ] `steward_inbox_resolve` applies the chosen action and logs audit entry
- [ ] Event-driven triggers fire steward_run after session_close
- [ ] Auto-apply rules execute within steward_run when policy allows
- [ ] Every auto-applied action has a corresponding audit entry
- [ ] `steward_report` includes link to audit entries for applied actions
- [ ] Inbox items have urgency ranking (high/medium/low)

---

## Milestone 4 — Temporal Knowledge Model

**Goal**: Knowledge has time boundaries. The system can answer "what was true at time T" and track how knowledge evolves.

### Data Model Changes

New fields on memory entries:

```
valid_from:     timestamp    // when this knowledge became true
valid_until:    timestamp    // when this knowledge stopped being true (null = still valid)
superseded_by:  string       // ID of the entry that replaced this one
replaces:       string       // ID of the entry this one replaced
observed_at:    timestamp    // when this was first observed (may differ from created_at)
```

### Tools

#### `recall_as_of`

Retrieve knowledge that was valid at a specific point in time.

```
recall_as_of(
  query:   string,
  as_of:   timestamp,
  type:    string,
  context: string,
  limit:   int,
)
→ [memories valid at as_of, ranked by relevance]
```

#### `knowledge_timeline`

Show how knowledge about a topic evolved over time.

```
knowledge_timeline(
  query:   string,
  context: string,
  service: string,
)
→ [{
  memory_id, title, valid_from, valid_until,
  superseded_by, replaces,
  status, confidence,
}]
```

### Supersession Chains

When `mark_outdated` or `steward_run` supersedes an entry:
- Set `valid_until` on old entry
- Set `superseded_by` → new entry ID
- Set `replaces` → old entry ID on new entry
- Set `valid_from` on new entry

### Integration

- `steward_run` builds supersession chains when detecting stale/updated entries
- `drift_scan` sets `valid_until` when source contradicts memory
- `recall_memory` optionally filters by `valid_at` parameter
- `knowledge_timeline` helps agents understand knowledge evolution

### Acceptance Criteria

- [ ] `recall_as_of` returns only entries where valid_from ≤ as_of AND (valid_until IS NULL OR valid_until > as_of)
- [ ] `knowledge_timeline` shows chronological chain of entries on a topic
- [ ] `mark_outdated` with superseding entry creates proper supersession links
- [ ] Supersession chains are navigable (entry → superseded_by → next entry)

---

## Milestone 5 — Documentation & Agent Integration Examples

**Goal**: Documentation отражает stewardship capabilities. Пользователи получают готовые skill-шаблоны для интеграции в своих агентов.

### What to update

#### README.md

- **Features section**: добавить stewardship capabilities (scheduled maintenance, drift detection, verification, inbox)
- **Architecture diagram**: добавить Steward layer между MCP Protocol Layer и Memory/RAG
- **MCP tools reference**: добавить секцию "Stewardship tools" с новыми инструментами
- **CLI commands**: добавить `steward` subcommands
- **Configuration**: добавить `STEWARD_*` переменные
- **Workflow snippets**: добавить snippets для stewardship (запуск maintenance, review inbox, drift check)

#### New docs

- `docs/STEWARDSHIP.md` — подробный гайд: что такое stewardship, как настроить policy, как читать reports, как работать с inbox
- `docs/examples/agent-memory-skill.md` — готовый skill-шаблон для Claude Code / Cursor / Codex (см. ниже)

#### Existing docs

- `docs/SHARED_SERVICE.md` — добавить секцию про scheduled stewardship в service mode
- `docs/SECURITY.md` — добавить trust model для stewardship auto-apply actions
- `docs/THREAT_MODEL.md` — добавить threat: steward auto-apply corrupts canonical knowledge

### Agent Skill Template

Готовый skill для подключения в `CLAUDE.md`, `.cursorrules`, `AGENTS.md` — показывает агенту как правильно использовать memory MCP в полном цикле:

- Когда и что сохранять (типы, теги, importance)
- Session lifecycle (start recall → work → close session)
- Правила тегирования
- Fallback для шумных сессий

Файл: `docs/examples/agent-memory-skill.md`

Этот шаблон также служит как reference implementation для пользователей, которые хотят написать свой skill поверх agent-memory-mcp.

### Acceptance Criteria

- [ ] README.md содержит секцию Stewardship tools в MCP tools reference
- [ ] README.md содержит `STEWARD_*` config variables
- [ ] README.md содержит workflow snippet для stewardship
- [ ] `docs/STEWARDSHIP.md` — self-contained guide для stewardship features
- [ ] `docs/examples/agent-memory-skill.md` — working skill template
- [ ] Existing docs updated with stewardship-related security/deployment notes
- [ ] Architecture diagram updated

---

## Implementation Phases

### Phase 1 — MVP (Milestone 1)

**Scope**: steward_run, steward_report, steward_policy, steward_status, basic scheduler

**Estimate**: This is the foundation. Everything else depends on it.

**Key decisions**:
- Scheduler lives in `internal/steward/` package
- Policy stored in `steward_policy` SQLite table
- Reports stored in `steward_runs` SQLite table
- Reuse existing `conflicts_report` and `merge_duplicates` logic

**Package structure**:
```
internal/
  steward/
    steward.go        // Steward service, orchestrates runs
    policy.go         // Policy CRUD and defaults
    scanner.go        // Duplicate, conflict, stale, canonical scanners
    report.go         // Report generation and storage
    scheduler.go      // Background job scheduler
    audit.go          // Audit trail
    types.go          // Shared types
```

### Phase 2 — Strong Differentiator (Milestones 2 + 3)

**Scope**: drift_scan, verification, canonical health, stewardship inbox, event triggers

**Depends on**: Phase 1 complete

**Key decisions**:
- drift_scan compares memory metadata against file mtimes and RAG index
- Inbox items are a new SQLite table `steward_inbox`
- Verification metadata stored in existing memory metadata JSON field

### Phase 3 — Advanced (Milestone 4)

**Scope**: temporal model, as-of retrieval, supersession chains, knowledge timeline

**Depends on**: Phase 2 complete

**Key decisions**:
- New columns on memories table (valid_from, valid_until, superseded_by, replaces)
- Migration adds columns with NULL defaults — backward compatible
- recall_as_of is a new tool, not a modification of recall_memory

### Phase 4 — Documentation (Milestone 5)

**Scope**: README update, STEWARDSHIP.md guide, agent skill template, existing docs updates

**When**: Incrementally after each phase. Full pass after Phase 2.

**Key decisions**:
- Skill template updated after each milestone to cover new tools
- STEWARDSHIP.md is the single deep-dive doc, README gets summary + link
- Examples always show real tool calls, not abstract descriptions

---

## Product Positioning

**Before**: "agent-memory-mcp is a memory + docs + repo context layer for engineering agents"

**After**: "agent-memory-mcp is an always-on stewardship service for living engineering knowledge — it doesn't just store what agents learn, it actively maintains knowledge quality so agents can trust what they recall"

### Key differentiators after stewardship layer

| Capability | Basic memory servers | agent-memory-mcp |
|---|---|---|
| Store & recall | Yes | Yes |
| Typed knowledge | Some | Yes (4 types + engineering entities) |
| Session consolidation | No | Yes (extract→plan→apply) |
| Duplicate/conflict detection | No | Yes (manual + automated) |
| Scheduled maintenance | No | Yes (steward_run + scheduler) |
| Drift detection | No | Yes (repo/docs/changelog comparison) |
| Knowledge verification | No | Yes (verification model + candidates) |
| Canonical knowledge governance | No | Yes (promotion rules + health checks) |
| Stewardship inbox | No | Yes (actionable review queue) |
| Temporal knowledge | No | Yes (valid_from/until, supersession chains) |
| Full audit trail | No | Yes (every action traced) |
| Policy-governed automation | No | Yes (configurable thresholds + auto-apply rules) |
