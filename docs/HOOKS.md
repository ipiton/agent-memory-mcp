# Claude Code Hooks Integration

`agent-memory-mcp` ships with three Claude Code hooks that capture session knowledge automatically — no manual `store_memory` calls required.

| Hook | When it fires | What it does |
|------|---------------|--------------|
| `SessionStart`  | Claude Code starts a session in the project | Injects recent memories and pending raw summaries into the system prompt via `agent-memory-mcp context-inject` |
| `SessionEnd`    | Session ends (user closes, agent exits) | Reads the transcript from stdin, runs the extract → plan → apply pipeline via `agent-memory-mcp auto-capture --stdin` |
| `PreCompact`    | Claude Code is about to compress context | Saves a raw checkpoint (`agent-memory-mcp checkpoint --boundary pre_compact --stdin`) so nothing is lost when the conversation is summarised |

Together these turn Claude Code into a memory-aware shell: every session contributes back to the project memory, every new session starts with relevant context surfaced.

## Quick install

```bash
agent-memory-mcp setup
```

This merges the hook entries into `~/.claude/settings.json`, preserving any existing hooks. **Restart Claude Code** afterwards — hooks load at process start.

Useful flags:

- `--dry-run` — print the resulting `settings.json` without writing it
- `--force` — overwrite existing entries (use after `brew upgrade` so the binary path is refreshed)
- `--command /custom/path/to/agent-memory-mcp` — override the binary path (default: the binary that's running `setup`)

After install, verify:

```bash
jq '.hooks' ~/.claude/settings.json
```

## Manual install

If you prefer to copy-paste into `settings.json` yourself:

```bash
agent-memory-mcp hooks-config --json
```

This prints a JSON fragment ready to merge into the `hooks` block. Example output:

```json
{
  "SessionStart": [
    {
      "type": "command",
      "command": "/usr/local/bin/agent-memory-mcp context-inject"
    }
  ],
  "SessionEnd": [
    {
      "type": "command",
      "command": "/usr/local/bin/agent-memory-mcp auto-capture --stdin"
    }
  ],
  "PreCompact": [
    {
      "type": "command",
      "command": "/usr/local/bin/agent-memory-mcp checkpoint --boundary pre_compact --stdin"
    }
  ]
}
```

The `setup` subcommand is just `hooks-config` plus a JSON merge into `~/.claude/settings.json` — they produce the same hook entries.

## What each hook actually does

### `SessionStart` — `context-inject`

Lightweight read-only path: opens `MCP_MEMORY_DB_PATH` in WAL `mode=ro` and prints two sections to stdout:

1. **Session Context** — recent knowledge items (excludes session summaries / checkpoints / review-queue records), filtered by `--context` and `--service` if provided. Default `--limit 10`, content truncated to 500 chars per entry.
2. **Pending session summaries** — raw `session_summary` records that are not yet tagged `compiled`. Includes inline instructions for the agent to extract reusable knowledge with `store_decision` / `store_memory` and tag the source as `compiled` once processed.

Why: Claude Code injects stdout into the system prompt, so the agent starts the session already aware of recent work.

### `SessionEnd` — `auto-capture`

Reads the full transcript from stdin and runs the `sessionclose` pipeline:

1. **Dedup check** — if the summary is too short (`MCP_CHECKPOINT_DEDUP_MIN_CHARS`) or near-duplicate of a recent one in the same context (`MCP_CHECKPOINT_DEDUP_THRESHOLD` jaccard within `MCP_CHECKPOINT_DEDUP_WINDOW`), it is skipped.
2. **Extract** — the raw summary is saved as a `session_summary` memory record.
3. **Plan** — proposed `new` / `update` / `merge` / `outdate` / `raw_only` actions are computed with rationale.
4. **Apply** — low-risk actions auto-apply; risky ones go to the review queue (visible via `project_bank_view view=review_queue`).

CLI flags mirror the `close-session` subcommand (`--mode coding|incident|migration|research|cleanup`, `--context`, `--service`, `--tags`).

### `PreCompact` — `checkpoint`

Saves a `session_checkpoint` memory record with `boundary=pre_compact`. This is a crash/compression safety net: even if the in-conversation summary is later compressed away, the raw checkpoint stays in the memory store and can be recalled with `recall_memory tags=session-checkpoint`.

Same dedup rules as `auto-capture`. Disable globally with `MCP_CHECKPOINT_DEDUP_DISABLED=true` if needed.

## Tuning

The hook commands inherit env from your shell, so any `MCP_*` env that affects the CLI applies. Most relevant:

| Env | What it changes |
|-----|------------------|
| `MCP_MEMORY_DB_PATH` | Which DB the hooks read/write |
| `MCP_CHECKPOINT_DEDUP_THRESHOLD` | Jaccard cutoff above which `auto-capture` and `checkpoint` skip the write (default `0.9`) |
| `MCP_CHECKPOINT_DEDUP_WINDOW` | Look-back window for dedup comparison (default `10m`) |
| `MCP_CHECKPOINT_DEDUP_MIN_CHARS` | Minimum content length before a checkpoint is eligible to save (default `100`) |
| `MCP_CHECKPOINT_DEDUP_DISABLED` | Bypass dedup for the hook CLI (default `false`) |

To pin a specific project root for the hook (useful if Claude Code's CWD is unpredictable), wrap the command:

```json
{
  "type": "command",
  "command": "cd /path/to/project && /usr/local/bin/agent-memory-mcp context-inject"
}
```

## Uninstall / rollback

There is no destructive uninstall — the hooks live in `~/.claude/settings.json`, so editing that file rolls them back:

```bash
# Backup before editing
cp ~/.claude/settings.json ~/.claude/settings.json.bak

# Drop the three keys
jq 'del(.hooks.SessionStart, .hooks.SessionEnd, .hooks.PreCompact)' \
  ~/.claude/settings.json > ~/.claude/settings.json.tmp \
  && mv ~/.claude/settings.json.tmp ~/.claude/settings.json
```

Memory data itself is unaffected — `MCP_MEMORY_DB_PATH` keeps everything captured by previous hook runs.

## Troubleshooting

**Hooks do not run after install.** Restart Claude Code. Hooks are loaded at process start.

**Hooks run but nothing appears in memory.** Check the binary path is correct and resolves from Claude Code's `$PATH`:

```bash
agent-memory-mcp setup --dry-run | jq '.hooks.SessionStart[0].command'
```

If the path is `agent-memory-mcp` (no leading `/`) and Claude Code can't find it, re-run `agent-memory-mcp setup --force --command "$(which agent-memory-mcp)"`.

**`auto-capture` reports "skipped: similar to …"** — that's the dedup. Lower `MCP_CHECKPOINT_DEDUP_THRESHOLD` or set `MCP_CHECKPOINT_DEDUP_DISABLED=true` if you want every session to land.

**`context-inject` returns nothing on a fresh project.** It only reads existing knowledge. Store one entry first (`agent-memory-mcp store -content "…" -type semantic`) and the next session start will pick it up.

## See also

- [README — Recommended Workflow Snippets](../README.md#recommended-workflow-snippets) — what to paste into `CLAUDE.md` so the agent uses these tools well
- [README — CLI commands](../README.md#cli-commands) — full reference for `setup`, `hooks-config`, `context-inject`, `auto-capture`, `checkpoint`
- [docs/MCP_TOOLS.md](MCP_TOOLS.md) — JSON examples for the underlying memory tools
