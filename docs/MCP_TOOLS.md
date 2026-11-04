# MCP Tools

This document shows concrete `tools/call` request payloads and example responses for the most common `agent-memory-mcp` tools. Use it as a copy-paste reference when integrating from a custom MCP client, debugging via raw JSON-RPC, or writing agent prompts.

The catalog comes first; JSON examples for the most common tools follow it.

## MCP tools reference

### Memory tools

| Tool | Description |
|------|-------------|
| `store_memory` | Store a memory with content, type, tags, and importance |
| `recall_memory` | Recall memories by semantic/text query with optional filters and trust-aware ranking |
| `update_memory` | Update an existing memory by ID |
| `delete_memory` | Delete a memory by ID |
| `list_memories` | List all memories with optional type/context filtering |
| `memory_stats` | Get memory statistics (counts by type) |
| `merge_duplicates` | Merge duplicate memories into a primary entry and archive the rest |
| `mark_outdated` | Mark a memory as outdated or superseded so trust-aware recall downranks it |
| `promote_to_canonical` | Promote a memory to canonical knowledge and boost its trust ranking |
| `conflicts_report` | Report duplicate candidates, conflicting statuses, and multiple canonical entries |
| `list_canonical_knowledge` | List canonical knowledge entries projected from confirmed memories |
| `recall_canonical_knowledge` | Recall canonical knowledge only, excluding raw memories from results |
| `recall_multihop` | Multi-hop graph-walk recall over the (subj, rel, obj) triple corpus — returns memories ranked by aggregated path score with the chain of triples that reached each result. Use for cross-memory reasoning queries that single-hop search cannot trace. Requires `MCP_TRIPLE_EXTRACTOR_*` populated; backfill via `index-triples` CLI. |

### RAG tools

| Tool | Description |
|------|-------------|
| `semantic_search` | Hybrid search across indexed documents with optional `source_type`, trust metadata, and `debug` explain mode |
| `index_documents` | Re-index documents for RAG search |

### File tools

| Tool | Description |
|------|-------------|
| `repo_list` | List files and folders under allowlisted paths |
| `repo_read` | Read a file from allowlisted paths |
| `repo_search` | Text search across allowlisted paths |

### Engineering workflow tools

| Tool | Description |
|------|-------------|
| `store_decision` | Store an engineering decision with rationale, status, and consequences |
| `store_incident` | Store an incident with impact, root cause, resolution, service, and severity |
| `store_runbook` | Store a runbook with procedure, trigger, verification, and rollback notes |
| `store_postmortem` | Store a postmortem with root cause and action items |
| `close_session` | Analyze a finished session into raw summary metadata, candidate knowledge items, and review-safe consolidation actions |
| `analyze_session` | Compatibility alias for `close_session` with the same planning and reporting behavior |
| `review_session_changes` | Render the explainable review report for a finished session without forcing writes |
| `accept_session_changes` | Persist the raw summary and auto-apply only low-risk consolidation actions |
| `resolve_review_item` | Resolve a pending review queue item so it disappears from the active inbox while keeping an audit trail |
| `search_runbooks` | Search runbook memories plus indexed runbook docs |
| `recall_similar_incidents` | Recall similar incidents from memory and indexed postmortems |
| `end_task` | Consolidate memory for an archived task slug: outdate working/procedural entries, route high-importance ones to the review queue |
| `sweep_archive` | Pull-mode scan over `MCP_TASK_ARCHIVE_ROOTS` that runs `end_task` on every archived slug |
| `store_dead_end` | Record an attempted approach that failed (plus the why and the alternative used) so retrieval can surface it as a pitfall warning on related queries. **Use this** for standalone failures with no decision context. **Use `store_decision -avoided-dead-end-id <id>`** when the dead end is part of a larger architectural decision and you want to link both records into one rationale chain (T46) |
| `promote_sediment` | Promote a memory to a higher sediment layer (surface → episodic → semantic → character). See `docs/SEDIMENTATION.md` |
| `demote_sediment` | Demote a memory one sediment layer down |
| `sediment_cycle` | Run the sediment transition cycle — auto-applies trivial promotions, routes non-trivial ones to the review queue |
| `summarize_project_context` | Summarize recent decisions, runbooks, incidents, and related docs |
| `project_bank_view` | Show a structured project bank view for canonical knowledge, decisions, runbooks, incidents, caveats, migrations, the review queue, or sediment promotion candidates |

### Stewardship tools

| Tool | Description |
|------|-------------|
| `steward_run` | Run a knowledge stewardship cycle: scan for duplicates, conflicts, stale entries, and canonical promotion candidates |
| `steward_report` | Retrieve the latest stewardship report or a specific one by run ID |
| `steward_policy` | Get or update the stewardship policy that controls detection thresholds, auto-apply rules, and scheduling |
| `steward_status` | Show current stewardship status: policy mode, last run summary, pending review count, next scheduled run |
| `drift_scan` | Compare memory entries against live sources (repo files, docs) to detect drift, missing references, and stale unverified knowledge |
| `verification_candidates` | List memories that need verification, ranked by urgency |
| `verify_entry` | Mark a memory as verified, updating its verification metadata |
| `steward_inbox` | List stewardship inbox items — review-required actions from maintenance runs, drift scans, and session consolidation |
| `steward_inbox_resolve` | Resolve a steward inbox item by applying an action: merge, mark_outdated, promote, verify, suppress, or defer |

### Temporal knowledge tools

| Tool | Description |
|------|-------------|
| `recall_as_of` | Retrieve knowledge that was valid at a specific point in time, filtering by temporal validity |
| `knowledge_timeline` | Show the chronological evolution of knowledge on a topic — how entries were created, superseded, and replaced over time |

### Tool grouping mode (token efficiency)

Every MCP client loads the full JSON schema of every tool at `initialize` time —
before your first message. With ~40 tools that schema payload alone can occupy
tens of KB of the model's context window on every session. Two secondary costs
compound it: LLMs get measurably worse at picking the right tool as the count
climbs past ~20–40, and frequent session reloads re-pay the whole cost.

Set `MCP_TOOL_GROUPING=true` to collapse the core toolset into a handful of
**grouped meta-tools**, each dispatching by a required `action` discriminator:

```jsonc
// Grouped form                          // Equivalent legacy form
{ "name": "memory",                      { "name": "store_memory",
  "arguments": {                           "arguments": { "content": "…" } }
    "action": "store", "content": "…" } }
```

Groups: `repo` · `memory` · `memory_admin` · `engineering` · `search` ·
`session`, plus the `index_documents` and `project_bank_view` singletons — the
default surface drops from **41 tools to 8** (~42% smaller schema payload).

- **Opt-in.** Default is `false`; the flag only changes what `tools/list` returns.
- **Both call forms work on the server.** `tools/call` accepts the grouped form
  (`memory` + `action=store`) *and* the legacy name (`store_memory`) regardless
  of the flag, so scripts calling the server directly keep working.
- **Clients see only the grouped names.** Whatever a client matches by tool name
  stops matching: permission rules (an `ask` on `promote_to_canonical` no longer
  applies when a wildcard allows the group), hook matchers (`store_memory`), and
  tool lists of subagents. A group also mixes reads and writes, so a read-only
  agent cannot be given `memory`. Check these before turning grouping on.
- **Administrative & steward tools stay individual.** Rarely listed in
  high-volume agent runs, and `steward_inbox_resolve` already uses its own
  `action` argument — grouping deliberately leaves them ungrouped.

Trade-off: each grouped call carries a slightly larger per-call schema (the union
of its actions' arguments). Prefer grouping for **high-volume agent runs** where
discovery cost dominates; leave it off for **interactive debugging** where seeing
each tool by name is clearer. Policy reference: [`docs/concepts/lifecycle.md`](concepts/lifecycle.md)
covers the related archive-sweep surface.

## Calling convention

All tools are invoked via the standard MCP `tools/call` JSON-RPC method. Examples below show the `params` block only — wrap them in:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": { … }
}
```

In stdio mode pipe to the binary; in HTTP mode `POST` to `http://localhost:18080/mcp` with `Authorization: Bearer <token>` when bound to a non-loopback host.

Responses always have the MCP envelope `{ "content": [{ "type": "text", "text": "…" }] }`. The `text` body is the structured payload — JSON when the tool returns structured data, plain text otherwise.

## Memory tools

### `store_memory`

```json
{
  "name": "store_memory",
  "arguments": {
    "content": "Ingress rollback uses the previous Helm revision. Run `helm rollback payments-api 0` and verify with `kubectl rollout status deploy/payments-api`.",
    "title": "payments-api ingress rollback",
    "type": "procedural",
    "tags": ["helm", "rollback", "payments", "runbook"],
    "context": "payments-api",
    "importance": 0.8
  }
}
```

Example response (`text` body):

```json
{
  "id": "mem_01HZQ7K3R0PV9C8R…",
  "type": "procedural",
  "embedding_model": "jina-embeddings-v3",
  "stored_at": "2026-05-04T11:42:13Z"
}
```

### `recall_memory`

```json
{
  "name": "recall_memory",
  "arguments": {
    "query": "how do we roll back ingress on payments?",
    "type": "all",
    "context": "payments-api",
    "tags": ["rollback"],
    "limit": 5
  }
}
```

Example response (`text` body, truncated):

```json
{
  "results": [
    {
      "id": "mem_01HZQ7K3R0PV9C8R…",
      "title": "payments-api ingress rollback",
      "content": "Ingress rollback uses the previous Helm revision…",
      "type": "procedural",
      "tags": ["helm", "rollback", "payments", "runbook"],
      "score": 0.87,
      "trust": {
        "layer": "raw",
        "confidence": 0.80,
        "freshness": 0.95,
        "owner": "",
        "verified": "2026-05-03T18:11:00Z"
      },
      "embedding_model": "jina-embeddings-v3"
    }
  ],
  "filters_applied": ["context=payments-api", "tags=rollback"],
  "total": 1
}
```

### `update_memory` / `delete_memory`

```json
{
  "name": "update_memory",
  "arguments": {
    "id": "mem_01HZQ7K3R0PV9C8R…",
    "content": "Updated: also drain connections before `helm rollback` (sev2 incident #2026-05-03).",
    "tags": ["helm", "rollback", "payments", "runbook", "drain-first"]
  }
}
```

```json
{
  "name": "delete_memory",
  "arguments": { "id": "mem_01HZQ7K3R0PV9C8R…" }
}
```

## RAG tools

### `semantic_search` — basic

```json
{
  "name": "semantic_search",
  "arguments": {
    "query": "ingress rollback procedure",
    "limit": 5
  }
}
```

### `semantic_search` — runbook-only with debug

```json
{
  "name": "semantic_search",
  "arguments": {
    "query": "ingress rollback procedure",
    "source_type": "runbook",
    "debug": true,
    "limit": 5
  }
}
```

Example response (debug mode, abridged):

```json
{
  "results": [
    {
      "path": "docs/runbooks/payments-ingress.md",
      "title": "Payments ingress rollback",
      "snippet": "Run `helm rollback payments-api 0` …",
      "source_type": "runbook",
      "score": 0.91,
      "score_breakdown": {
        "semantic": 0.78,
        "keyword_raw": 4.2,
        "keyword_normalized": 0.68,
        "recency_boost": 0.05,
        "source_boost": 0.10,
        "confidence_boost": 0.00,
        "final_score": 0.91
      },
      "applied_boosts": ["keyword_match", "source_type:runbook"],
      "trust": {
        "source": "runbook",
        "confidence": 0.90,
        "freshness": 0.88,
        "verified": "2026-04-30T09:02:00Z"
      }
    }
  ],
  "filters_applied": ["source_type=runbook"],
  "candidates": {
    "indexed": 1842,
    "after_filter": 37,
    "noise_dropped": 4,
    "returned": 5
  }
}
```

### `index_documents`

```json
{
  "name": "index_documents",
  "arguments": {}
}
```

Reindexes per `MCP_INDEX_DIRS` and answers with what the corpus now contains:

```
Indexed 4650 files / 60766 chunks.
  tasks/archive — 3923 files, 36477 chunks
  docs — 678 files, 22967 chunks
  memory-bank — 49 files, 1322 chunks
  ⚠️  runbooks — configured but contributed nothing
```

### What defines the corpus

`MCP_INDEX_DIRS` — a comma-separated list of directories and individual files,
resolved relative to `MCP_ROOT` unless absolute. The code default is `docs`;
nothing outside that list is searchable via `semantic_search`, whatever a
consumer's own documentation may promise about it.

The last line above is the one to read. `semantic_search` has no error state: a
query against a corpus that never included the material returns "no results",
which is indistinguishable from "there is nothing like that". A root that is
configured and contributes zero files is therefore reported by name (T97) —
omitting it would make it look like a root nobody had configured. Common
causes: the path does not exist under `MCP_ROOT`, it holds no supported file
types, or `MCP_INDEX_EXCLUDE_DIRS` / `MCP_INDEX_EXCLUDE_GLOBS` filtered
everything out.

## Engineering workflow tools

### `store_decision`

```json
{
  "name": "store_decision",
  "arguments": {
    "title": "Disable HPA on payments-api",
    "decision": "Disable HorizontalPodAutoscaler on payments-api until we understand the metric storm",
    "rationale": "HPA was scaling on stale custom metrics from Prometheus, causing oscillation between 3 and 18 replicas during incident #2026-05-03",
    "consequences": "Higher idle baseline cost, manual scale-up needed for traffic spikes; revisit after Prometheus relabeling lands",
    "context": "payments-api",
    "service": "payments-api",
    "owner": "platform",
    "status": "accepted",
    "tags": ["k8s", "hpa", "incident-followup"],
    "importance": 0.9
  }
}
```

### `store_decision` linked to a dead end

```json
{
  "name": "store_decision",
  "arguments": {
    "decision": "Use server-side filtering for tag search instead of in-process",
    "rationale": "In-process filtering required loading all memories into RAM; the dead-end attempt is recorded in mem_01HZ…DEAD",
    "context": "agent-memory-mcp",
    "service": "agent-memory-mcp",
    "avoided_dead_end_id": "mem_01HZ…DEAD",
    "tags": ["performance", "ram"],
    "importance": 0.85
  }
}
```

### `store_dead_end` — standalone

```json
{
  "name": "store_dead_end",
  "arguments": {
    "title": "All-memories-in-RAM tag filter",
    "attempted_approach": "Load every memory into a slice and filter tags in Go",
    "why_failed": "OOM at 50k+ memories; GC pressure spiked p99 by 8x",
    "alternative_used": "Server-side SQL filter with `json_extract(tags, '$') LIKE`",
    "related_task_slug": "T-204",
    "context": "agent-memory-mcp",
    "service": "agent-memory-mcp",
    "tags": ["performance", "ram"]
  }
}
```

When to use which:

- `store_dead_end` — record a pitfall on its own. Future retrieval for related queries surfaces it as a warning.
- `store_decision` with `avoided_dead_end_id` — when the dead end is part of a bigger architectural decision and you want both records linked into one rationale chain.

### `store_incident`

```json
{
  "name": "store_incident",
  "arguments": {
    "title": "payments-api 5xx spike during ingress upgrade",
    "summary": "Ingress controller upgrade caused 12 minutes of 5xx on payments-api due to stale TLS session cache",
    "impact": "~3% of checkouts failed; ~$12k revenue impact",
    "root_cause": "ingress-nginx 1.10 changed TLS cache eviction; existing long-lived sessions were terminated",
    "resolution": "Helm rollback to ingress-nginx 1.9.5; pinned chart version, follow-up to test 1.10 in staging",
    "service": "payments-api",
    "severity": "sev2",
    "tags": ["ingress", "tls"]
  }
}
```

### `close_session`

```json
{
  "name": "close_session",
  "arguments": {
    "summary": "Fixed payments-api ingress timeout. Updated runbook docs/runbooks/payments-ingress.md with the drain-first step. Identified follow-up to test ingress-nginx 1.10 in staging.",
    "mode": "incident",
    "context": "payments-api",
    "service": "payments-api",
    "tags": ["sev2", "ingress"],
    "started_at": "2026-05-04T09:30:00Z",
    "ended_at": "2026-05-04T11:55:00Z"
  }
}
```

Example response (abridged JSON when `format: "json"`):

```json
{
  "raw_summary_id": "mem_01HZQ7N0…",
  "actions": [
    {
      "kind": "update",
      "target_id": "mem_01HZ…RUNBOOK",
      "title": "Add drain-first step to payments-ingress runbook",
      "rationale": "session adds a verified mitigation step",
      "risk": "low",
      "auto_apply": true
    },
    {
      "kind": "new",
      "title": "Follow-up: test ingress-nginx 1.10 in staging",
      "rationale": "no matching incident or decision exists",
      "risk": "medium",
      "auto_apply": false
    }
  ],
  "review_queue_items": ["rev_01HZ…"]
}
```

### `accept_session_changes`

Persists the raw summary plus auto-applies the `auto_apply: true` actions from `close_session`. Same input shape; safe alternative when you want one-call ingest:

```json
{
  "name": "accept_session_changes",
  "arguments": {
    "summary": "…",
    "mode": "coding",
    "context": "agent-memory-mcp",
    "service": "agent-memory-mcp"
  }
}
```

### `recall_similar_incidents` / `search_runbooks`

```json
{
  "name": "recall_similar_incidents",
  "arguments": {
    "query": "payments-api 5xx during ingress upgrade",
    "limit": 5
  }
}
```

```json
{
  "name": "search_runbooks",
  "arguments": {
    "query": "rollback ingress",
    "service": "payments-api",
    "limit": 5
  }
}
```

## Project bank and review queue

### `summarize_project_context`

```json
{
  "name": "summarize_project_context",
  "arguments": {
    "context": "payments-api",
    "service": "payments-api",
    "limit": 10
  }
}
```

### `project_bank_view`

```json
{
  "name": "project_bank_view",
  "arguments": {
    "view": "review_queue",
    "limit": 25
  }
}
```

Available views: `canonical_overview`, `decisions`, `runbooks`, `incidents`, `caveats`, `migrations`, `review_queue`, `sediment_promotion_candidates`.

### `resolve_review_item`

```json
{
  "name": "resolve_review_item",
  "arguments": {
    "id": "rev_01HZ…",
    "resolution": "applied",
    "note": "Merged into mem_01HZ…RUNBOOK manually after spot-check",
    "owner": "platform"
  }
}
```

## Stewardship

### `steward_run`

```json
{
  "name": "steward_run",
  "arguments": {
    "dry_run": true,
    "duplicate_threshold": 0.85,
    "stale_days": 30,
    "limit": 200
  }
}
```

### `steward_inbox` / `steward_inbox_resolve`

```json
{
  "name": "steward_inbox",
  "arguments": { "limit": 50 }
}
```

```json
{
  "name": "steward_inbox_resolve",
  "arguments": {
    "id": "stewinbox_01HZ…",
    "action": "merge",
    "primary_id": "mem_01HZ…CANON",
    "note": "Both describe the same payments rollback path"
  }
}
```

Actions: `merge`, `mark_outdated`, `promote`, `verify`, `suppress`, `defer`.

### `drift_scan`

```json
{
  "name": "drift_scan",
  "arguments": { "limit": 100 }
}
```

Detects `source_changed`, `source_missing`, and `stale_unverified` against `MCP_ROOT`.

## Temporal knowledge

### `recall_as_of`

```json
{
  "name": "recall_as_of",
  "arguments": {
    "query": "payments-api scaling strategy",
    "as_of": "2026-01-15T00:00:00Z",
    "limit": 5
  }
}
```

### `knowledge_timeline`

```json
{
  "name": "knowledge_timeline",
  "arguments": {
    "query": "payments-api scaling",
    "limit": 20
  }
}
```

### `mark_outdated` with supersession

```json
{
  "name": "mark_outdated",
  "arguments": {
    "id": "mem_01HZ…OLD",
    "superseded_by": "mem_01HZ…NEW",
    "reason": "Replaced HPA strategy after sev2 incident on 2026-05-03"
  }
}
```

This automatically sets `valid_until` on the old entry and `valid_from` + `replaces` on the new one, building a navigable supersession chain.

## File / repo tools

### `repo_list` / `repo_read` / `repo_search`

```json
{
  "name": "repo_list",
  "arguments": { "path": "docs/runbooks", "depth": 2 }
}
```

```json
{
  "name": "repo_read",
  "arguments": { "path": "docs/runbooks/payments-ingress.md" }
}
```

```json
{
  "name": "repo_search",
  "arguments": {
    "query": "helm rollback",
    "path": "docs",
    "limit": 20
  }
}
```

All three are restricted to `MCP_ROOT` plus `MCP_ALLOW_DIRS`. Path traversal (`..`) and absolute paths outside the allowlist are rejected.

## Multi-hop graph recall (T50)

### `recall_multihop`

```json
{
  "name": "recall_multihop",
  "arguments": {
    "query": "what causes payments-api 5xx during ingress upgrades?",
    "limit": 10,
    "max_hops": 3
  }
}
```

Requires `MCP_TRIPLE_EXTRACTOR_*` and a populated triple corpus. Backfill existing memories with `agent-memory-mcp index-triples`.

Returns memories ranked by aggregated path score plus the chain of `(subj, rel, obj)` triples that reached each result. Use it for cross-memory reasoning that single-hop search cannot trace.

## Tips

- **Always set `context`** for memories tied to a project, task, or service. It lets `recall_memory`, `summarize_project_context`, and `project_bank_view` filter precisely.
- **Use `tags` for cross-cutting facets** (`["k8s", "rollback", "sev2"]`) and `context` for the single owning project — do not put service names in both.
- **`importance` is a float 0.0–1.0**. Default `0.5`. `store_decision` defaults to `0.85` and `store_incident` to `0.9` because those are typically more durable than ad-hoc notes.
- **`type` for `store_memory`**: `episodic` (events), `semantic` (facts), `procedural` (patterns/runbooks), `working` (current task context). The four engineering tools (`store_decision`, `store_runbook`, `store_incident`, `store_postmortem`) write `procedural`/`semantic`/`episodic` automatically — prefer them when they fit.
- **Debug mode for retrieval is opt-in** (`debug: true`). Keep it off in production paths to keep responses small.
- **Idempotency**: `store_*` tools do **not** dedup — they always create a new memory. Use `merge_duplicates` or stewardship to consolidate later. The hooks (`auto-capture`, `checkpoint`) have built-in jaccard dedup; the bare MCP tools do not.

See also:

- [Recommended Workflows](WORKFLOWS.md)
- [docs/HOOKS.md](HOOKS.md)
- [docs/STEWARDSHIP.md](STEWARDSHIP.md)
- [docs/SEDIMENTATION.md](SEDIMENTATION.md)
