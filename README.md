# agent-memory-mcp

[![Agent Memory MCP on mcprush](https://mcprush.com/badge/ipiton-agent-memory-mcp/score.svg)](https://mcprush.com/to/ipiton-agent-memory-mcp?ref=badge)

Long-term memory and project-docs search for coding agents, in one local MCP server backed by SQLite.

Your agent remembers why a decision was made, finds the runbook that matches an incident, and notices when what it remembers no longer matches the repo. It works with Claude Code, Codex, Cursor, Gemini CLI, OpenCode, VS Code and Windsurf.

## What sets it apart

- **Semantic search over memory and docs at once.** Typed memories (decisions, facts, procedures, working notes) and your indexed docs, ADRs, changelogs and runbooks are ranked together: embeddings plus keyword matching, recency and source type. Every hit carries its trust data: confidence, freshness, owner, last verification.
- **A steward that keeps memory clean.** `steward_run` finds duplicates, contradictions and stale entries and puts anything risky in a review inbox instead of silently rewriting history.
- **Drift detection against the repo.** `drift_scan` compares what memory says with the files it refers to and flags entries whose source changed or disappeared.
- **Canonical knowledge, confirmed by a human.** Raw notes become canonical only through `promote_to_canonical`; canonical entries rank first and are what the agent leans on. An agent cannot mark its own notes as verified or canonical.
- **Local first.** One binary, one SQLite file, stdio transport. Embeddings can stay on your machine (Ollama or llama.cpp). An HTTP mode is there when a team wants a shared instance.

## Install

```bash
brew tap ipiton/tap
brew install agent-memory-mcp
```

Without Homebrew: `go install github.com/ipiton/agent-memory-mcp/cmd/agent-memory-mcp@latest`, or a binary from [Releases](https://github.com/ipiton/agent-memory-mcp/releases). The server needs one embedding provider (Jina, OpenAI, Ollama or llama.cpp); [Installation](docs/INSTALLATION.md) covers the `.env` and local-only setups.

## Connect your agent

Run this from the repository the agent should remember:

| Agent | Command | Writes to |
|---|---|---|
| Claude Code | `agent-memory-mcp setup claude-code --yes` | `~/.claude.json` |
| Codex | `agent-memory-mcp setup codex --yes` | `~/.codex/config.toml` |
| Cursor | `agent-memory-mcp setup cursor --yes` | `~/.cursor/mcp.json` |
| Gemini CLI | `agent-memory-mcp setup gemini --yes` | `~/.gemini/settings.json` |
| OpenCode | `agent-memory-mcp setup opencode --yes` | `~/.config/opencode/opencode.json` |
| VS Code | `agent-memory-mcp setup vscode --yes` | user `mcp.json` |
| Windsurf | `agent-memory-mcp setup windsurf --yes` | `~/.codeium/windsurf/mcp_config.json` |

Without `--yes` the command is a dry run and prints the entry it would add:

```text
$ agent-memory-mcp setup claude-code
Claude Code: would create the file with server "memory" in /Users/you/.claude.json
+ {
+   "args": [
+     "-lc",
+     "cd '/path/to/repo' && MCP_HTTP_MODE=stdio exec '/opt/homebrew/bin/agent-memory-mcp'"
+   ],
+   "command": "/bin/sh",
+   "type": "stdio"
+ }
Dry run: nothing written. Re-run with --yes to apply.
```

The existing config is kept as it is, other servers and keys included, and a timestamped backup is written next to it. Running the command again changes nothing. If a server with the same name already launches something else, the command stops; `--force` replaces only its launch command. Other flags: `--name`, `--root`, `--command`. Restart the agent afterwards.

For Claude Code, `agent-memory-mcp setup` (no agent name) also installs the session hooks that capture knowledge automatically; see [Hooks](docs/HOOKS.md).

## What it looks like

Real output from a demo repository with one runbook, one `deploy/values.yaml`, and one stored decision. Excerpts are trimmed.

**"How many replicas should payments-api run, and why?"** → `recall_memory`

```text
1. **Why payments-api runs 4 replicas** (relevance: 0.79)
   Type: Semantic
   Tags: [decision payments capacity]
   Trust: layer=raw source=decision confidence=0.96 freshness=1.00 owner=engineering
   Content: Decision 2026-09-12: payments-api runs 4 replicas, not 2. Two replicas dropped
   requests during the September card-network spike; the HPA reacts too slowly for
   30-second bursts. Source: ./deploy/values.yaml. Revisit when queue-based retry lands.
```

**"How do I roll back a bad payments deploy?"** → `semantic_search` over the indexed docs. The query shares almost no words with the runbook.

```text
1. **payments-api rollback** (relevance: 0.94)
   Path: docs/runbooks/payments-rollback.md
   Source type: runbook
   1. Find the last good revision: `helm history payments-api -n prod`.
   2. Roll back: `helm rollback payments-api <rev> -n prod`.
   3. Verify: `kubectl rollout status deploy/paymen...
```

**"Is what you remember about payments still true?"** → `drift_scan`, after someone changed `replicas: 4` to `2` in `deploy/values.yaml`

```text
Drift Scan: 3 memories scanned, 1 findings

1. [source_changed] Why payments-api runs 4 replicas
   Evidence: File ./deploy/values.yaml modified 2026-10-31 (memory verified 2026-10-31)
   Suggested: verify (confidence: 0.70)
```

The steward tools (`drift_scan`, `steward_run`, the review inbox) switch on by themselves only in HTTP mode. For an agent connected through `setup`, add `MCP_STEWARD_ENABLED=true` to the project's `.env`. See [Stewardship](docs/STEWARDSHIP.md).

## How it compares

<!-- T158: comparison with other memory MCP servers goes here, backed by a reproducible benchmark. -->

A comparison with other memory servers, measured on a public corpus, is in preparation.

## Documentation

| Topic | Where |
|---|---|
| Features and design goals | [docs/FEATURES.md](docs/FEATURES.md) |
| Install, local-only mode, indexing, team service, manual client config, HTTP mode | [docs/INSTALLATION.md](docs/INSTALLATION.md) |
| Prompt snippets for consistent agent behaviour | [docs/WORKFLOWS.md](docs/WORKFLOWS.md) |
| All MCP tools, with JSON examples | [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md) |
| CLI | [docs/CLI.md](docs/CLI.md) |
| Configuration and environment variables | [docs/CONFIGURATION.md](docs/CONFIGURATION.md) |
| Claude Code hooks | [docs/HOOKS.md](docs/HOOKS.md) |
| Stewardship, sedimentation, memory lifecycle | [docs/STEWARDSHIP.md](docs/STEWARDSHIP.md), [docs/SEDIMENTATION.md](docs/SEDIMENTATION.md), [docs/concepts/lifecycle.md](docs/concepts/lifecycle.md) |
| Architecture and embedding providers | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Shared service, backup, security | [docs/SHARED_SERVICE.md](docs/SHARED_SERVICE.md), [docs/BACKUP_RESTORE.md](docs/BACKUP_RESTORE.md), [docs/SECURITY.md](docs/SECURITY.md), [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) |
| Troubleshooting | [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) |
| Changes | [docs/CHANGELOG.md](docs/CHANGELOG.md) |

## Development

```bash
# Build
make build

# Run
make run

# Test
make test

# Full quality gate (lint + tests + smoke)
make quality-gates
```

For repo layout, code style, and PR conventions see [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md).

## License

MIT
