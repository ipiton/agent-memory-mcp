# agent-memory-mcp

[![Agent Memory MCP on mcprush](https://mcprush.com/badge/ipiton-agent-memory-mcp/score.svg)](https://mcprush.com/to/ipiton-agent-memory-mcp?ref=badge)

Long-term memory and project-docs search for coding agents, in one local MCP server backed by SQLite.

Your agent remembers why a decision was made, finds the runbook that matches an incident, and notices when what it remembers no longer matches the repo.

## Install

**Claude Code.** Nothing else to set up: no service, no model, one local database per project. [Details](docs/PLUGIN.md).

```text
/plugin install agent-memory --marketplace ipiton/agent-memory-mcp
```

**Codex, Cursor, Gemini CLI, OpenCode, VS Code, Windsurf, or Claude Code without the plugin.** A standalone server, which needs one embedding provider. [Details](docs/INSTALLATION.md), including builds without Homebrew.

```bash
brew tap ipiton/tap
brew install agent-memory-mcp
cd your-repo
agent-memory-mcp setup codex --yes
```

Restart the agent afterwards.

## What it does

- **Searches memory and docs together.** Decisions, facts and procedures are ranked alongside your docs, ADRs and runbooks, and every hit says how far to trust it.
- **Keeps memory clean.** `steward_run` finds duplicates, contradictions and stale entries, and sends anything risky to a review inbox.
- **Notices drift.** `drift_scan` flags memories whose source file changed or disappeared.
- **Leaves the last word to you.** Notes become canonical only through `promote_to_canonical`. An agent cannot verify its own notes.
- **Stays local.** One binary and SQLite. An HTTP mode is there for a shared team instance.

```text
> How many replicas should payments-api run, and why?

1. **Why payments-api runs 4 replicas** (relevance: 0.79)
   Trust: layer=raw source=decision confidence=0.96 freshness=1.00 owner=engineering
   Content: Decision 2026-09-12: payments-api runs 4 replicas, not 2. Two replicas dropped
   requests during the September card-network spike. Source: ./deploy/values.yaml.
```

More in [Features](docs/FEATURES.md).

## Documentation

| Topic | Where |
|---|---|
| Claude Code plugin | [PLUGIN.md](docs/PLUGIN.md) |
| Standalone install, connecting agents, team service | [INSTALLATION.md](docs/INSTALLATION.md) |
| Tools and CLI | [MCP_TOOLS.md](docs/MCP_TOOLS.md), [CLI.md](docs/CLI.md) |
| Configuration | [CONFIGURATION.md](docs/CONFIGURATION.md) |
| Stewardship and memory lifecycle | [STEWARDSHIP.md](docs/STEWARDSHIP.md) |
| Architecture and security | [ARCHITECTURE.md](docs/ARCHITECTURE.md), [SECURITY.md](docs/SECURITY.md) |
| Troubleshooting and changes | [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md), [CHANGELOG.md](docs/CHANGELOG.md) |

## Development

```bash
make build
make test
make vet
```

Conventions are in [CONTRIBUTING.md](docs/CONTRIBUTING.md). License: MIT.
