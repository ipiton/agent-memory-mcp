# Recommended Workflows

Prompt snippets that make agents use memory consistently across sessions.

## Recommended Workflow Snippets

Without these snippets, the agent will only use basic `store_memory` and `recall_memory`. To unlock session close, engineering memory types, project bank, and consolidation, add relevant snippets to your agent's instructions.

Where to put them:

- **Claude Code** — paste into `CLAUDE.md` at the project root
- **Cursor** — paste into `.cursorrules` at the project root
- **Codex** — paste into the system prompt or `AGENTS.md`
- **Claude Desktop** — paste into the system prompt field in the project settings

Pick the snippets that match your workflow. Start with "Start-of-session recall" and "Coding close" — they cover the most common case.

### Start-of-session recall

```text
Before you start, recall the project context for this task.
Then recall recent changes related to the service or component I am touching.
Search for relevant runbooks, RFCs, changelog notes, or incident notes.
Prefer `summarize_project_context` or `project_bank_view view=canonical_overview` for the first pass and then drill into `search_runbooks` or `recall_similar_incidents`.
Summarize the constraints, caveats, and likely risks before making changes.
```

### Coding close

```text
When the coding session ends, call `close_session` with a concise summary, service, and context.
Review the proposed `new`, `update`, `merge`, and `raw_only` actions plus the decision trace.
If the plan looks low risk, use `accept_session_changes`.
If the report is noisy or mostly exploratory, keep `save_raw_only` as the fallback.
Prefer `project_bank_view` at the next session start to confirm what became maintained knowledge.
```

### Incident close

```text
When incident work stabilizes, call `close_session` or `review_session_changes` with `mode=incident`.
Expect stricter review-first behavior for updates, merges, and anything touching canonical operational knowledge.
Capture impact, mitigation, rollback, and unresolved follow-ups in the summary.
Apply only the low-risk actions automatically and leave ambiguous runbook or incident changes in review.
Follow up with `recall_similar_incidents` and `project_bank_view view=incidents` if you need to compare against existing knowledge.
```

### Migration close

```text
When a migration session ends, call `close_session` with `mode=migration`, affected service, and the migration summary.
Prefer explicit notes about prerequisites, sequencing, rollback, and post-deploy verification.
Treat runbook replacements, caveat changes, and supersede proposals as review-first even when the textual match looks strong.
Use `accept_session_changes` only after checking the report for stale or superseded knowledge.
Finish by checking `project_bank_view view=migrations` to see the maintained migration notes.
```

### Raw-only fallback

```text
If the session was exploratory, ambiguous, or too noisy, skip consolidation and save only the raw summary.
Use `close_session` / `review_session_changes` to inspect the plan first, then pick `save_raw_only`.
In CLI mode, `agent-memory-mcp accept-session -raw-only ...` is the explicit override.
This keeps the raw trace without forcing weak knowledge updates into the project bank.
```

### Before-changing-infra check

```text
Before making infra or platform changes, recall similar fixes, migrations, incidents, and known caveats.
Search for runbooks, postmortems, changelog notes, and recent project context related to this component.
Summarize blast radius, rollback options, and operational risks before editing files.
```

### Stewardship run

```text
When memory has grown or a session just ended, run `steward_run` with `dry_run=true` to see what needs attention.
Review the report for duplicates, conflicts, stale entries, and canonical promotion candidates.
Check `steward_inbox` for pending review items and resolve them with `steward_inbox_resolve`.
Use `drift_scan` periodically to catch memories that reference files or docs that have changed.
Use `verification_candidates` to find knowledge that has not been verified recently.
```

### Temporal recall

```text
When you need to understand what was true at a specific point in time, use `recall_as_of` with an RFC3339 timestamp.
To trace how knowledge about a topic evolved over time, use `knowledge_timeline`.
When superseding an old decision or runbook, use `mark_outdated` with the superseding entry ID to build a proper chain.
```
