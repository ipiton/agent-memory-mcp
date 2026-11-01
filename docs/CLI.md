# CLI

Every MCP workflow is also available from the shell.

## CLI Mode

The binary also works as a standalone CLI:

```bash
# Memory operations
agent-memory-mcp store -content "Project uses chi router" -type procedural -tags "go,chi"
agent-memory-mcp recall "router middleware"
agent-memory-mcp list -type procedural
agent-memory-mcp delete <memory-id>

# RAG search
agent-memory-mcp search "authentication flow"
agent-memory-mcp search -source-type runbook "ingress rollback"
agent-memory-mcp search -source-type runbook -debug "ingress rollback"
agent-memory-mcp index

# Project bank and session close
agent-memory-mcp project-bank canonical_overview
agent-memory-mcp close-session -summary "Updated payments rollback runbook after fixing ingress timeout" -context payments-api -service payments-api
agent-memory-mcp review-session -mode incident -stdin < notes/session.txt
agent-memory-mcp accept-session -summary "Added migration caveat for billing schema rename" -mode migration -context billing -service billing-api
agent-memory-mcp accept-session -raw-only -summary "Exploratory notes that are too noisy for consolidation"

# Utilities
agent-memory-mcp stats
agent-memory-mcp config claude-desktop
agent-memory-mcp reembed
agent-memory-mcp export > backup.json
agent-memory-mcp import backup.json

# JSON output for scripting
agent-memory-mcp recall "test" -json
agent-memory-mcp stats -json
```

Run `agent-memory-mcp <command> -help` for details on any command.

CLI memory commands and MCP memory tools now share the same validation and normalization rules:

- invalid memory types are rejected consistently
- comma-separated tags are trimmed and deduplicated the same way
- zero `verified` timestamps are hidden from trust summaries in both CLI and MCP output

When no command is given (or flags start with `-`), the binary starts the MCP server as before -- full backward compatibility.
## CLI commands

| Command | Description |
|---------|-------------|
| `serve` | Start MCP server (stdio/http) -- default when no command given |
| `store` | Store a memory (`-content`, `-title`, `-type`, `-tags`, `-context`, `-importance`, `-stdin`) |
| `recall` | Memory recall with trust-aware ranking (positional query, `-type`, `-tags`, `-limit`, `-json`) |
| `list` | List memories (`-type`, `-context`, `-limit`, `-json`) |
| `delete` | Delete a memory by ID (positional) |
| `search` | RAG hybrid search with trust metadata (positional query, `-limit`, `-source-type`, `-debug`, `-json`) |
| `index` | Re-index documents for RAG |
| `close-session` | Analyze an end-of-session summary and produce a close-session report (`-summary`, `-stdin`, `-mode`, `-context`, `-service`, `-tags`, `-metadata`, `-started-at`, `-ended-at`, `-raw-only`, `-json`) |
| `review-session` | Review-oriented alias for `close-session` with the same inputs and report surface |
| `accept-session` | Save the raw summary and auto-apply low-risk session changes (`-summary`, `-stdin`, `-mode`, `-context`, `-service`, `-tags`, `-metadata`, `-started-at`, `-ended-at`, `-raw-only`, `-json`) |
| `stats` | Show memory statistics (`-json`) |
| `config` | Generate ready MCP client config snippets |
| `project-bank` | Show structured project bank views (`canonical_overview`, `decisions`, `runbooks`, `incidents`, `caveats`, `migrations`, `review_queue`) |
| `resolve-review-item` | Resolve a pending review queue item (`<id>`, `-resolution`, `-note`, `-owner`, `-json`) |
| `reembed` | Re-generate memory embeddings with the active model (`-json`; `-truncated` also re-encodes vectors built from a body's opening) |
| `export` | Export all memories to JSON (`-o` file, default stdout) |
| `import` | Import memories from JSON (positional file or stdin) |
| `index-triples` | Retrofit (subj, rel, obj) triples for memories that lack them (`-resume`, `-force`, `-limit`, `-context`, `-dry-run`, `-progress-every`, `-json`). Powers the `recall_multihop` MCP tool — see `MCP_TRIPLE_EXTRACTOR_*` envs. |
| `dead-ends-stale` | List dead_end memories older than `-age` (default 12 months) for re-evaluation (`-limit`, `-json`) |
| `setup` | Auto-configure Claude Code hooks in `~/.claude/settings.json` (`-command`, `-dry-run`, `-force`). See [docs/HOOKS.md](HOOKS.md) |
| `hooks-config` | Print Claude Code hooks JSON for manual paste into `settings.json` (`-command`, `-json`) |
| `context-inject` | SessionStart hook payload: recent memories + pending raw summaries (`-limit`, `-pending-limit`, `-context`, `-service`) |
| `auto-capture` | SessionEnd hook: read transcript from stdin, run extract → plan → apply pipeline (`-stdin`, `-summary`, `-mode`, `-context`, `-service`, `-tags`, `-dry-run`, `-json`) |
| `checkpoint` | PreCompact hook: save a raw session checkpoint before context compression (`-stdin`, `-summary`, `-boundary`, `-context`, `-service`, `-tags`) |
| `sweep-archive` | Scan `MCP_TASK_ARCHIVE_ROOTS` and run `end-task` on every archived slug (T47) |
| `end-task` | Consolidate working/procedural memories tied to one archived task slug (T47) |
| `mark-dead-end` | Record an abandoned approach with its failure rationale (T46) |
| `sediment-cycle` | Apply layer transitions for memory sedimentation: trivial promotions auto-apply, the rest queue for review (T48) |
| `recount-refs` | Backfill `referenced_by_count` metadata from existing cross-memory edges (idempotent) |
