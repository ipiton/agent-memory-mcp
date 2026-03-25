# Knowledge Stewardship Guide

The stewardship layer turns agent-memory-mcp from passive storage into an active knowledge maintenance service. It detects problems, surfaces review items, and optionally auto-applies safe fixes.

## Quick Start

Enable stewardship and run your first scan:

```bash
# In .env
MCP_STEWARD_ENABLED=true
MCP_STEWARD_MODE=manual
```

Then from an MCP client:

```
steward_run scope=full dry_run=true
```

This scans all memories for duplicates, conflicts, stale entries, and canonical promotion candidates without changing anything. Review the report, then run with `dry_run=false` to apply safe actions and queue the rest for review.

## How It Works

### Maintenance Cycle

A steward run executes four scanners:

1. **Duplicate detection** — groups memories by entity/service/context/subject and flags groups with 2+ entries
2. **Conflict detection** — finds groups with multiple canonical entries or conflicting lifecycle statuses
3. **Stale detection** — flags memories not verified within the configured threshold (default: 30 days)
4. **Canonical promotion** — finds high-importance, active engineering entries without an existing canonical counterpart

Each scanner produces actions. Actions are classified as:

- `safe_auto_apply` — low-risk, applied automatically when `dry_run=false`
- `review_required` — sent to the stewardship inbox for human or agent review

### Canonical Health

When scope includes `canonical` or `full`, the report includes a health summary:

- **Stale canonical** — not verified in 2x the stale threshold
- **Unverified canonical** — promoted but never explicitly verified
- **Conflicting canonical** — multiple canonical entries for the same subject
- **Low support** — importance below the canonical minimum confidence threshold

## Tools Reference

### steward_run

Run a maintenance cycle.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `scope` | string | `full` | `full`, `duplicates`, `conflicts`, `stale`, `canonical` |
| `dry_run` | bool | `true` | Report only, no changes |
| `context` | string | - | Limit scan to a context |
| `service` | string | - | Limit scan to a service |
| `format` | string | `text` | `text` or `json` |

### steward_report

Retrieve a past report.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `run_id` | string | latest | Specific run ID |
| `format` | string | `text` | `text` or `json` |

### steward_policy

Get or update the policy.

| Parameter | Type | Description |
|-----------|------|-------------|
| `action` | string | `get` or `set` |
| `policy` | object | New policy (when action=set) |

Policy fields:

```json
{
  "mode": "manual",
  "schedule_interval": "24h",
  "event_triggers": ["session_close"],
  "duplicate_similarity": 0.85,
  "stale_days": 30,
  "canonical_min_confidence": 0.80,
  "canonical_min_evidence": 2,
  "auto_merge_exact_duplicates": false,
  "auto_mark_stale_beyond_days": 0,
  "auto_refresh_freshness_scores": true
}
```

### steward_status

Show current state: policy mode, last run summary, pending review count, next scheduled run.

### drift_scan

Compare memory against live sources.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `scope` | string | `all` | `all`, `canonical`, `decisions`, `runbooks` |
| `context` | string | - | Context filter |
| `service` | string | - | Service filter |
| `format` | string | `text` | `text` or `json` |

Drift types:

- `source_changed` — referenced file modified after memory was last verified
- `source_missing` — referenced path no longer exists
- `stale_unverified` — not verified within threshold

### verification_candidates

List memories needing verification, ranked by urgency.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `limit` | int | 20 | Max results |
| `scope` | string | `all` | `all`, `canonical`, `decisions`, `runbooks` |
| `min_age_days` | int | - | Only entries older than N days |
| `context` | string | - | Context filter |
| `service` | string | - | Service filter |

Urgency ranking:

- **High**: verification_failed, needs_update, stale canonical, unverified canonical
- **Medium**: stale beyond threshold, never-verified high-importance

### verify_entry

Mark a memory as verified.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `memory_id` | string | required | Memory ID |
| `method` | string | `manual` | `manual`, `source_check`, `repo_scan`, `agent_verified` |
| `status` | string | `verified` | `verified`, `verification_failed`, `needs_update` |
| `note` | string | - | What was checked |

### steward_inbox

List review-required items.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `status` | string | `pending` | `pending`, `resolved`, `deferred`, `all` |
| `kind` | string | - | Filter by kind |
| `limit` | int | 20 | Max results |
| `sort_by` | string | `urgency` | `urgency`, `created_at`, `confidence` |

Item kinds: `duplicate_candidate`, `contradiction_candidate`, `stale_canonical`, `promotion_candidate`, `drift_detected`, and more.

### steward_inbox_resolve

Resolve an inbox item.

| Parameter | Type | Description |
|-----------|------|-------------|
| `item_id` | string | Required. Inbox item ID |
| `action` | string | Required. `merge`, `mark_outdated`, `mark_superseded`, `promote`, `verify`, `suppress`, `defer` |
| `note` | string | Optional resolution note |

## Temporal Knowledge

### recall_as_of

Retrieve knowledge valid at a specific point in time.

| Parameter | Type | Description |
|-----------|------|-------------|
| `query` | string | Required. Search query |
| `as_of` | string | Required. RFC3339 timestamp |
| `context` | string | Optional context filter |
| `limit` | int | Max results (default: 10) |

### knowledge_timeline

Show chronological evolution of knowledge on a topic.

| Parameter | Type | Description |
|-----------|------|-------------|
| `query` | string | Required. Topic to trace |
| `context` | string | Optional context filter |
| `service` | string | Optional service filter |

### Supersession Chains

When `mark_outdated` is called with a `superseded_by` ID:

1. Old entry gets `valid_until = now` and `superseded_by = new_id`
2. New entry gets `valid_from = now` and `replaces = old_id`
3. Both entries remain queryable — the old one is downranked in normal recall but visible in `recall_as_of` and `knowledge_timeline`

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_STEWARD_ENABLED` | auto | Auto-enabled in HTTP mode with memory |
| `MCP_STEWARD_MODE` | `manual` | `off`, `manual`, `scheduled`, `event_driven` |
| `MCP_STEWARD_SCHEDULE_INTERVAL` | `24h` | Interval for scheduled runs |
| `MCP_STEWARD_DUPLICATE_THRESHOLD` | `0.85` | Similarity threshold |
| `MCP_STEWARD_STALE_DAYS` | `30` | Days before stale |
| `MCP_STEWARD_CANONICAL_MIN_CONFIDENCE` | `0.80` | Min confidence for promotion |

### Modes

- **off** — stewardship disabled
- **manual** — run via `steward_run` tool only
- **scheduled** — background runs at `schedule_interval`
- **event_driven** — runs trigger on events (e.g. after `session_close`)

### Auto-Apply Rules

Configured via `steward_policy`:

| Rule | Default | Effect |
|------|---------|--------|
| `auto_refresh_freshness_scores` | on | Recalculate freshness on each run |
| `auto_merge_exact_duplicates` | off | Auto-merge similarity >= 0.95 |
| `auto_mark_stale_beyond_days` | 0 (off) | Auto-mark stale after N days |

## Audit Trail

Every applied action is logged in the `steward_audit` table:

- Run ID, action kind, target memory IDs
- Rationale, evidence, confidence
- Applied timestamp and actor (`steward_auto` or `user`)

Audit entries are linked to their steward run and can be queried by run ID.

## Database Tables

Stewardship uses four SQLite tables in the same database as memories:

- `steward_policy` — current policy configuration
- `steward_runs` — completed run reports with stats and actions
- `steward_audit` — applied action audit trail
- `steward_inbox` — review-required items queue
