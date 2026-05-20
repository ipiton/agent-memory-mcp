# agent-memory-mcp

A memory, docs, and repo context layer for engineering agents.

`agent-memory-mcp` helps agents work with live engineering context, not just isolated notes. It combines typed memory, document retrieval, and repository-aware tools so Claude, Cursor, Codex, and other MCP clients can recall decisions, search runbooks, inspect project docs, and reuse operational knowledge across sessions.

It is designed for engineering workflows such as:

- DevOps and platform operations
- infrastructure changes and rollback planning
- runbooks, changelogs, RFCs, and postmortems
- project-level memory that stays attached to the repo

## Who This Is For

- teams using AI agents on real codebases, docs, and operational workflows
- DevOps, platform, and infra engineers who need more than chat history
- projects that want local-first memory today and a shared service path later

## Why Not Just A Memory Tool

Most memory MCP servers focus on "store a note, recall a note."

`agent-memory-mcp` is aimed at a wider engineering context layer:

- typed memory for decisions, facts, patterns, and working context
- RAG indexing for project docs, changelogs, and knowledge files
- repo/file tools for reading and searching allowed project paths
- local SQLite storage with stdio today and HTTP/JSON-RPC when you need to share it

This makes it a better fit when the agent needs to answer questions like:

- "Why did we disable HPA on this service?"
- "What changed recently that could explain this regression?"
- "Which runbook or RFC matches this incident?"

## Table of Contents

- [Who This Is For](#who-this-is-for)
- [Why Not Just A Memory Tool](#why-not-just-a-memory-tool)
- [Features](#features)
- [What Improved For Users](#what-improved-for-users)
- [Start Local In 3 Minutes](#start-local-in-3-minutes)
- [Local-Only Mode](#local-only-mode)
- [Index Your Repo In 2 Commands](#index-your-repo-in-2-commands)
- [Turn It Into A Team Service Later](#turn-it-into-a-team-service-later)
- [Installation Options](#installation-options) — Homebrew, binary, source, Docker
- [CLI Mode](#cli-mode)
- [MCP client configuration](#mcp-client-configuration) — Claude Desktop, Cursor, Codex
- [Recommended Workflow Snippets](#recommended-workflow-snippets) — what to paste into `CLAUDE.md` / `.cursorrules`
- [CLI commands](#cli-commands)
- [MCP tools reference](#mcp-tools-reference) — and JSON examples in [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md)
- [Configuration](#configuration) — env vars, hot-reload, indexing safety
- [Security And Operations](#security-and-operations)
- [Architecture](#architecture)
- [macOS service installation](#macos-service-installation)
- [Troubleshooting / FAQ](#troubleshooting--faq)
- [Development](#development)

Reference docs: [HOOKS](docs/HOOKS.md) · [MCP_TOOLS](docs/MCP_TOOLS.md) · [SHARED_SERVICE](docs/SHARED_SERVICE.md) · [STEWARDSHIP](docs/STEWARDSHIP.md) · [SEDIMENTATION](docs/SEDIMENTATION.md) · [BACKUP_RESTORE](docs/BACKUP_RESTORE.md) · [SECURITY](docs/SECURITY.md) · [THREAT_MODEL](docs/THREAT_MODEL.md) · [CONTRIBUTING](docs/CONTRIBUTING.md) · [CHANGELOG](docs/CHANGELOG.md)

## Features

- **Automatic session capture** — Claude Code hooks auto-capture knowledge at session end, save checkpoints before context compression, and compile pending summaries at session start
- **Typed persistent memory** with 4 types: episodic, semantic, procedural, working
- **Hybrid retrieval** that combines embeddings with keyword/BM25-like ranking
- **RAG indexing** for project docs, changelogs, and knowledge archives (enabled by default in stdio/CLI mode; **disabled by default in the Homebrew service preset** — see [Installation Options](#installation-options))
- **Repo-aware file tools** for listing, reading, and searching allowlisted paths
- **Knowledge stewardship** — automated maintenance: duplicate detection, conflict resolution, stale detection, drift scanning, and a review inbox
- **Temporal knowledge model** — track when knowledge was valid, build supersession chains, and query "what was true at time T"
- **Dual transport**: stdio for MCP clients, HTTP/JSON-RPC for APIs and shared setups
- **SQLite storage** for both memory and vector index -- no external databases needed
- **Auto-indexing** with file watcher for long-running local or service mode

## What Improved For Users

- **Lower memory usage**: memory store now reads from SQLite directly instead of loading everything into RAM — large memory banks no longer risk OOM
- **Opinionated solo-local setup**: one recommended layout, one data directory, one quick smoke path
- **Auto-loaded `.env`**: run from your project root without manually sourcing environment variables
- **Local-only embedding mode**: keep hosted providers disabled and send text only to your local Ollama endpoint
- **Safer semantic recall**: memories from a different embedding model no longer produce misleading matches
- **Explicit migration flow**: use `agent-memory-mcp reembed` for memory migration and `agent-memory-mcp index` for RAG rebuilds after switching models
- **Better visibility**: `stats` and `memory_stats` now show how many memories belong to each embedding model
- **Ready MCP client configs**: generate copy-paste snippets for Claude Desktop, Cursor, and Codex
- **Safer indexing defaults**: built-in directory excludes, optional per-path exclude globs, and secret redaction before documents are indexed
- **Source-aware retrieval**: docs, ADRs, RFCs, changelogs, runbooks, postmortems, CI configs, Helm, Terraform, and K8s files are classified and surfaced with source metadata
- **Hybrid ranking for search**: semantic similarity is now combined with keyword matches, recency, and source-aware weighting instead of cosine similarity alone
- **Trust-aware retrieval**: memory and document results now expose `source_type`, `confidence`, `freshness`, `owner`, and `last_verified_at`, and ranking uses trust/freshness instead of similarity alone
- **Explainable retrieval**: opt-in debug output shows filters, score components, and applied boosts for every result
- **DevOps-first tools**: store decisions, incidents, runbooks, and postmortems with domain-specific MCP tools instead of generic memory calls
- **Memory lifecycle**: memories move through statuses — active, outdated, superseded, canonical — so stale knowledge gets downranked automatically instead of polluting recall
- **Manual consolidation workflow**: merge duplicates, mark outdated notes, promote canonical entries, and inspect conflict groups without deleting history
- **Explicit canonical knowledge layer**: list and recall confirmed knowledge separately from raw memory, and surface canonical context first in project summaries
- **Project bank views**: see maintained knowledge organized by category — decisions, runbooks, incidents, caveats, migrations, review queue — instead of a flat memory list
- **Session close pipeline**: when a session ends, memory is analyzed, classified, and consolidated with existing knowledge instead of blindly appended
- **Explainable consolidation**: session close reports show what will be added, merged, outdated, or promoted, with a decision trace and risk level for each action
- **DevOps session modes**: close-session adapts behavior based on session type — incident and migration sessions get stricter review-first policy, coding sessions auto-apply low-risk updates
- **Shared service packaging**: a working Docker Compose recipe, shared env template, nginx reverse proxy example, and a dedicated shared deployment guide
- **Built-in retrieval console**: inspect hybrid ranking, trust, and normal-vs-debug retrieval in a lightweight HTTP UI at `/console`
- **Safer HTTP defaults**: HTTP mode binds to `127.0.0.1` by default; non-loopback binds require auth unless you explicitly opt into unsafe unauthenticated access
- **Consistent CLI and MCP behavior**: memory type validation, tag normalization, query/content limits, and trust summaries now follow the same policy across both interfaces
- **Knowledge stewardship**: `steward_run` executes a full maintenance cycle — duplicate detection, conflict resolution, stale entry scanning, and canonical promotion candidates — with a single command
- **Stewardship inbox**: review-required actions from maintenance runs, drift scans, and session consolidation land in one actionable queue instead of being silently applied or lost
- **Drift detection**: `drift_scan` compares memory entries against live repo files and docs to find stale, missing, or changed references
- **Verification model**: `verify_entry` and `verification_candidates` let agents and users track when knowledge was last verified and what needs attention
- **Canonical health diagnostics**: steward runs now include a health summary for canonical entries — stale, unverified, conflicting, and low-support
- **Policy-governed automation**: stewardship thresholds, auto-apply rules, and scheduling are configurable via `steward_policy` and environment variables
- **Temporal knowledge**: memories can carry `valid_from` / `valid_until` timestamps, and `recall_as_of` retrieves knowledge that was valid at a specific point in time
- **Supersession chains**: `mark_outdated` with a superseding entry automatically builds bidirectional links (`superseded_by` / `replaces`) and sets temporal boundaries
- **Knowledge timeline**: `knowledge_timeline` shows the chronological evolution of knowledge on a topic

## Start Local In 3 Minutes

The recommended path is: run locally first, prove value on one repo, then expand.

Run these commands from your project root.

### Prerequisites

Install the binary with one of these options:

```bash
# Homebrew (macOS/Linux) — recommended, auto-configures Claude Code hooks
brew tap ipiton/tap
brew install agent-memory-mcp
```

```bash
# go install
go install github.com/ipiton/agent-memory-mcp/cmd/agent-memory-mcp@latest
```

Then configure one embedding provider:

- [Jina AI API key](https://jina.ai/) for the quickest hosted setup
- [OpenAI API key](https://platform.openai.com/) or another OpenAI-compatible endpoint
- [Ollama](https://ollama.ai/) with `bge-m3` for a local setup

### 1. Configure local mode

```bash
cp .env.example .env
# Edit .env:
# - keep the solo-local defaults unless you need to change them
# - enable at least one embedding provider
#   JINA_API_KEY, OPENAI_API_KEY, or OLLAMA_BASE_URL
```

The binary auto-loads `.env` from the current directory, so you do not need `source .env`.

The recommended solo-local preset keeps all runtime state inside one directory:

```text
.agent-memory/
  rag-index/
  memory-store/
  logs/
```

## Local-Only Mode

Use local-only mode when you want embeddings without sending text to hosted APIs.

```bash
cp .env.example .env
# Then set:
# MCP_EMBEDDING_MODE=local-only
# JINA_API_KEY=
# OPENAI_API_KEY=
```

In `local-only` mode:

- `agent-memory-mcp` never calls Jina AI
- `agent-memory-mcp` never calls OpenAI-compatible embedding APIs
- embeddings are generated only through your local Ollama endpoint

What still uses the network:

- the local Ollama HTTP endpoint, typically `http://localhost:11434`

If Ollama is not running or no supported local model is available, embedding requests fail with a local-only specific error telling you to start Ollama or disable `MCP_EMBEDDING_MODE=local-only`.

### 2. Start the local server

For MCP clients such as Claude Desktop, Cursor, or Codex:

```bash
agent-memory-mcp
```

For direct CLI use, the same binary already works without an MCP client:

```bash
agent-memory-mcp store -content "Ingress rollback uses previous Helm revision" -type procedural -tags "helm,rollback"
agent-memory-mcp recall "helm rollback"
agent-memory-mcp stats
```

### 3. Run a smoke check

```bash
agent-memory-mcp store -content "Solo local smoke check" -type working -tags "smoke,local"
agent-memory-mcp recall "solo local smoke"
agent-memory-mcp index
agent-memory-mcp search "agent memory"
```

If you are working from the source checkout, you can run the same flow with:

```bash
make local-smoke
```

## Index Your Repo In 2 Commands

Once local mode is running against a project, index docs and search them:

```bash
agent-memory-mcp index
agent-memory-mcp search "recent ingress change"
```

Typical high-value sources include:

- `docs/`
- `README.md`
- `CHANGELOG.md`
- RFC / ADR folders
- runbooks and incident notes

## Turn It Into A Team Service Later

When local mode proves useful, move in three steps:

1. solo local
2. team laptop with auto-indexing and file watching
3. shared service with HTTP mode, auth token, and reverse proxy

Fastest shared-service path:

```bash
cd deploy/docker
cp .env.shared.example .env.shared
# edit MCP_HTTP_AUTH_TOKEN and MCP_PROJECT_ROOT
docker compose --env-file .env.shared up -d --build
```

This keeps the same retrieval stack, but packages it for team use.

Reference docs:

- [Shared Service Guide](docs/SHARED_SERVICE.md)
- [Security Policy](docs/SECURITY.md)
- [Backup And Restore](docs/BACKUP_RESTORE.md)

## Installation Options

### Homebrew (recommended for macOS)

```bash
brew tap ipiton/tap
brew install agent-memory-mcp
brew services start agent-memory-mcp
```

This installs the binary, creates a default config, and starts the service on `127.0.0.1:18080` with memory enabled. RAG document search is disabled by default — enable it by editing the config:

```bash
# Edit config
nano $(brew --prefix)/etc/agent-memory-mcp/config.env
```

Set `MCP_RAG_ENABLED=true`, `MCP_ROOT=/path/to/your/project`, and `MCP_INDEX_DIRS=docs,README.md`. Changes are picked up automatically within ~30 seconds, or force reload with `kill -HUP $(pgrep agent-memory-mcp)`.

Manage the service:

```bash
brew services restart agent-memory-mcp
brew services stop agent-memory-mcp
brew services info agent-memory-mcp
```

If you previously installed via Cask and want `brew services`:

```bash
brew uninstall --cask agent-memory-mcp
brew install ipiton/tap/agent-memory-mcp
```

### Download a binary

Download a prebuilt archive from the [Releases](https://github.com/ipiton/agent-memory-mcp/releases) page.

The release archives include the version in their filename, so resolve the latest tag first:

```bash
# Resolve latest version once
VERSION=$(curl -fsSL https://api.github.com/repos/ipiton/agent-memory-mcp/releases/latest \
  | grep '"tag_name"' | head -1 | cut -d'"' -f4 | sed 's/^v//')

# macOS (Apple Silicon)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-darwin-arm64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/

# macOS (Intel)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-darwin-amd64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/

# Linux (x86_64)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-linux-amd64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/

# Linux (arm64)
curl -fsSL "https://github.com/ipiton/agent-memory-mcp/releases/download/v${VERSION}/agent-memory-mcp-${VERSION}-linux-arm64.tar.gz" | tar xz
sudo mv agent-memory-mcp /usr/local/bin/
```

### Build from source

```bash
git clone https://github.com/ipiton/agent-memory-mcp.git
cd agent-memory-mcp
go build -o bin/agent-memory-mcp ./cmd/agent-memory-mcp
```

### Docker

```bash
docker build -f deploy/docker/Dockerfile -t agent-memory-mcp .
docker run -p 18080:18080 \
  -v memory-data:/data \
  -e MCP_HTTP_MODE=http \
  -e MCP_HTTP_HOST=0.0.0.0 \
  -e MCP_HTTP_AUTH_TOKEN=replace-with-long-random-token \
  agent-memory-mcp
```

Or with docker compose:

```bash
cd deploy/docker
cp .env.shared.example .env.shared
docker compose --env-file .env.shared up -d --build
```

The MCP HTTP endpoint will be available at `http://localhost:18080/mcp`.

By default, bare-metal HTTP mode now binds to `127.0.0.1`. For shared/container deployments, set `MCP_HTTP_HOST=0.0.0.0` and a bearer token.

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

## MCP client configuration

Use the built-in generator to produce a project-local config that starts the server from your repo root.

This is the recommended path because it:

- keeps `.env` loading working without duplicating settings into every MCP client
- keeps `.agent-memory/` relative to the project root
- gives you one copy-paste snippet per client

You can override the detected project root or binary path with `-root` and `-command`.

### Claude Desktop

Paste into `~/Library/Application Support/Claude/claude_desktop_config.json`:

```bash
agent-memory-mcp config claude-desktop
```

Example generated output:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/bin/sh",
      "args": [
        "-lc",
        "cd '/path/to/your/project' && exec '/absolute/path/to/agent-memory-mcp'"
      ]
    }
  }
}
```

### Cursor

Paste into `~/.cursor/mcp.json`:

```bash
agent-memory-mcp config cursor
```

Example generated output:

```json
{
  "mcpServers": {
    "memory": {
      "command": "/bin/sh",
      "args": [
        "-lc",
        "cd '/path/to/your/project' && exec '/absolute/path/to/agent-memory-mcp'"
      ]
    }
  }
}
```

### Codex

Paste into `~/.codex/config.toml`:

```bash
agent-memory-mcp config codex
```

Example generated output:

```toml
[mcp_servers.memory]
command = "/bin/sh"
args = ["-lc", "cd '/path/to/your/project' && exec '/absolute/path/to/agent-memory-mcp'"]
```

### Rename the server or override paths

```bash
agent-memory-mcp config claude-desktop \
  -name engineering-memory \
  -root /path/to/your/project \
  -command /absolute/path/to/agent-memory-mcp
```

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

### HTTP mode (Docker, remote server, shared instance)

Start the server in HTTP mode:

```bash
# Standalone
MCP_HTTP_MODE=http \
MCP_HTTP_HOST=127.0.0.1 \
MCP_HTTP_PORT=18080 \
MCP_HTTP_AUTH_TOKEN=replace-with-long-random-token \
agent-memory-mcp

# Or with Docker
cd deploy/docker
docker compose --env-file .env.shared up -d --build
```

Then point your HTTP-capable MCP client or proxy at:

```text
http://localhost:18080/mcp
```

For retrieval inspection in a browser, open:

```text
http://localhost:18080/console
```

The console is a lightweight UI for:

- running document, raw-memory, and canonical-knowledge queries
- comparing normal vs debug mode for document retrieval
- inspecting source types, trust/freshness, and score breakdowns

In shared mode, the page itself is static, but live queries from the console still require the same bearer token as `/mcp`.

For shared HTTP mode:

- default bare-metal bind is `MCP_HTTP_HOST=127.0.0.1`; this is the safe local default
- for shared/container deployments set `MCP_HTTP_HOST=0.0.0.0`
- set `MCP_HTTP_AUTH_TOKEN` to require `Authorization: Bearer <token>` on `/mcp`
- startup now fails on non-loopback binds without `MCP_HTTP_AUTH_TOKEN`, unless you explicitly set `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true`
- keep `/health` for load balancer or container health checks
- terminate TLS at a reverse proxy or load balancer
- do not expose the service directly on the public internet without auth and TLS
- use [deploy/nginx/agent-memory-mcp.conf](deploy/nginx/agent-memory-mcp.conf) as the starting reverse proxy recipe
- use [docs/SHARED_SERVICE.md](docs/SHARED_SERVICE.md) for the full `local -> team laptop -> shared service` path

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
| `reembed` | Re-generate memory embeddings with the active model (`-json`) |
| `export` | Export all memories to JSON (`-o` file, default stdout) |
| `import` | Import memories from JSON (positional file or stdin) |
| `index-triples` | Retrofit (subj, rel, obj) triples for memories that lack them (`-resume`, `-force`, `-limit`, `-context`, `-dry-run`, `-progress-every`, `-json`). Powers the `recall_multihop` MCP tool — see `MCP_TRIPLE_EXTRACTOR_*` envs. |
| `dead-ends-stale` | List dead_end memories older than `-age` (default 12 months) for re-evaluation (`-limit`, `-json`) |
| `setup` | Auto-configure Claude Code hooks in `~/.claude/settings.json` (`-command`, `-dry-run`, `-force`). See [docs/HOOKS.md](docs/HOOKS.md) |
| `hooks-config` | Print Claude Code hooks JSON for manual paste into `settings.json` (`-command`, `-json`) |
| `context-inject` | SessionStart hook payload: recent memories + pending raw summaries (`-limit`, `-pending-limit`, `-context`, `-service`) |
| `auto-capture` | SessionEnd hook: read transcript from stdin, run extract → plan → apply pipeline (`-stdin`, `-summary`, `-mode`, `-context`, `-service`, `-tags`, `-dry-run`, `-json`) |
| `checkpoint` | PreCompact hook: save a raw session checkpoint before context compression (`-stdin`, `-summary`, `-boundary`, `-context`, `-service`, `-tags`) |
| `sweep-archive` | Scan `MCP_TASK_ARCHIVE_ROOTS` and run `end-task` on every archived slug (T47) |
| `end-task` | Consolidate working/procedural memories tied to one archived task slug (T47) |
| `mark-dead-end` | Record an abandoned approach with its failure rationale (T46) |
| `sediment-cycle` | Apply layer transitions for memory sedimentation: trivial promotions auto-apply, the rest queue for review (T48) |
| `recount-refs` | Backfill `referenced_by_count` metadata from existing cross-memory edges (idempotent) |

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

## Configuration

All configuration is via environment variables. See [`.env.example`](.env.example) for the full list.

Config files are loaded in this order (each file only fills in values not already set):

1. `--config /path/to/file` (explicit path, skips chain)
2. `.env` in the current directory
3. `~/.config/agent-memory-mcp/config.env` (XDG)
4. `$(brew --prefix)/etc/agent-memory-mcp/config.env` (Homebrew)

For solo local mode, copy `.env.example` to `.env` in your project root. For `brew services`, the config is auto-created at `$(brew --prefix)/etc/agent-memory-mcp/config.env`.

### Hot-reload

When running as a service (HTTP mode), the config file is watched for changes every 30 seconds. RAG-related settings (index dirs, embedding keys, enabled/disabled) are applied without restart. HTTP settings (port, host) require a full restart.

You can also force an immediate reload:

```bash
kill -HUP $(pgrep agent-memory-mcp)
```

### Key variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MCP_ROOT` | Current dir | Project root path |
| `MCP_ALLOW_DIRS` | `""` (only `MCP_ROOT`) | Comma-separated extra repo-relative paths the file tools (`repo_list`, `repo_read`, `repo_search`) may read. Paths must stay under `MCP_ROOT`; absolute paths or `..` traversal are rejected at config load. **Critical for shared/HTTP mode** — keep narrow |
| `MCP_MAX_FILE_BYTES` | `2097152` | Max file size (bytes) `repo_read` will return; larger files are rejected |
| `MCP_MAX_SEARCH_RESULTS` | `200` | Hard cap for `repo_search` result count |
| `MCP_MAX_DEPTH` | `3` | Max directory recursion depth for `repo_list` |
| `MCP_STDIO_MODE` | `line` | Stdio framing: `line` (newline-delimited) or `lsp` (Content-Length headers) |
| `MCP_MEMORY_ENABLED` | `true` | Enable memory tools |
| `MCP_MEMORY_PREVIEW_RUNES` | `0` | Override the per-surface truncation cap (rune-based) for memory content/summary fields in MCP tool responses (`recall_memory`, `list_memories`, `search_runbooks`, …). `0` keeps the built-in caps (150/220/300); a positive value forces that single cap on all surfaces; a negative value disables truncation (full text). |
| `MCP_RAG_ENABLED` | `true` | Enable RAG/search tools (Homebrew service preset overrides this to `false` until you set `MCP_ROOT`) |
| `MCP_HTTP_MODE` | `stdio` | Transport: `stdio` or `http` |
| `MCP_HTTP_HOST` | `127.0.0.1` | HTTP bind host; set `0.0.0.0` for shared/container deployments |
| `MCP_HTTP_PORT` | `18080` | HTTP port (when in HTTP mode) |
| `MCP_HTTP_AUTH_TOKEN` | - | Bearer token required for non-loopback/shared HTTP mode |
| `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED` | `false` | Explicit unsafe override for non-loopback HTTP without auth |
| `JINA_API_KEY` | - | Jina AI API key for embeddings |
| `OPENAI_API_KEY` | - | OpenAI API key (or compatible: Together, Mistral) |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `OPENAI_EMBEDDING_MODEL` | `text-embedding-3-small` | Embedding model name |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama URL (local fallback) |
| `MCP_EMBEDDING_MODE` | `auto` | Embedding mode: `auto` or `local-only` |
| `MCP_EMBEDDING_DIMENSION` | `1024` | Vector dimension (change requires re-indexing) |
| `MCP_INDEX_DIRS` | `docs` | Comma-separated directories and individual files to index for RAG. Code fallback is `docs`; the shipped `.env.example` preset sets `docs,README.md,CHANGELOG.md` for a typical project layout |
| `MCP_RAG_AUTO_INDEX` | `true` | Index documents on startup. Code default is `true` (good for HTTP/service mode); the solo-local `.env.example` preset turns it off so you control indexing with explicit `agent-memory-mcp index` runs |
| `MCP_RAG_FILE_WATCHER` | `false` | Watch `MCP_INDEX_DIRS` for changes and reindex incrementally; useful for long-running shared/service instances |
| `MCP_INDEX_EXCLUDE_DIRS` | built-in defaults | Extra directory names or repo-relative paths to exclude from RAG indexing |
| `MCP_INDEX_EXCLUDE_GLOBS` | - | Extra glob patterns matched against repo-relative paths, for example `docs/internal/*.md` |
| `MCP_REDACT_SECRETS` | `true` | Redact common secret-like content before documents are indexed |
| `MCP_SESSION_TRACKING_ENABLED` | `true` | Enable background session tracking, auto raw summaries, and low-risk close-session orchestration |
| `MCP_SESSION_IDLE_TIMEOUT` | `10m` | Idle timeout before the active background session auto-closes |
| `MCP_SESSION_CHECKPOINT_INTERVAL` | `30m` | Interval for periodic raw checkpoint snapshots during active sessions |
| `MCP_SESSION_MIN_EVENTS` | `2` | Minimum tracked MCP tool calls before background auto-close runs |
| `MCP_DATA_PATH` | `data` | Base path for data storage |
| `MCP_RAG_INDEX_PATH` | `<MCP_DATA_PATH>/rag-index` | Override the SQLite vector index location |
| `MCP_MEMORY_DB_PATH` | `<MCP_DATA_PATH>/memory-store/memories.db` | Override the SQLite memory database path |
| `MCP_LOG_PATH` | `<MCP_DATA_PATH>/logs/mcp-diagnostics.log` | Override the diagnostics log file path |
| `MCP_STATS_ENABLED` | `false` | Append per-call usage records (jsonl) for self-observability |
| `MCP_STATS_PATH` | `<MCP_DATA_PATH>/logs/mcp-usage.jsonl` | Stats jsonl output path |
| `MCP_STATS_SAMPLE_RATE` | `1.0` | Fraction (0.0–1.0) of calls to record when stats are enabled |
| `MCP_STEWARD_ENABLED` | auto | Enable knowledge stewardship (auto-enabled in HTTP mode with memory) |
| `MCP_STEWARD_MODE` | `manual` | Stewardship mode: `off`, `manual`, `scheduled`, `event_driven` |
| `MCP_STEWARD_SCHEDULE_INTERVAL` | `24h` | Interval between scheduled stewardship runs |
| `MCP_STEWARD_DUPLICATE_THRESHOLD` | `0.85` | Similarity threshold for duplicate detection |
| `MCP_STEWARD_STALE_DAYS` | `30` | Days before a memory is considered stale |
| `MCP_STEWARD_CANONICAL_MIN_CONFIDENCE` | `0.80` | Minimum confidence for canonical promotion candidates |
| `MCP_CHECKPOINT_DEDUP_THRESHOLD` | `0.9` | Jaccard similarity threshold above which a checkpoint is considered a duplicate of the previous one in the same context |
| `MCP_CHECKPOINT_DEDUP_WINDOW` | `10m` | Time window for the dedup lookup — only checkpoints newer than this are compared |
| `MCP_CHECKPOINT_DEDUP_MIN_CHARS` | `100` | Minimum content length (chars) before a checkpoint is eligible to be saved; shorter content is dropped as empty |
| `MCP_CHECKPOINT_DEDUP_DISABLED` | `false` | Escape hatch: disable checkpoint-hook deduplication entirely |
| `MCP_TASK_ARCHIVE_ROOTS` | - | Colon-separated archive roots for `sweep-archive` / `end-task` (e.g. `/home/you/tasks/archive`). Empty disables the feature |
| `MCP_TASK_SLUG_PATTERN` | - | Optional regex filtering archive subdirectory names; invalid regex fails config load |
| `MCP_RERANK_ENABLED` | `false` | Master gate for the neural reranker stage after hybrid search. Must be `true` AND `MCP_RERANK_PROVIDER` must be a real provider (`jina`) for the reranker to run |
| `MCP_RERANK_PROVIDER` | `disabled` | Reranker provider: `jina` or `disabled`. With `disabled` (or empty) the pipeline degrades to hybrid-only ranking even when `MCP_RERANK_ENABLED=true` |
| `JINA_RERANKER_MODEL` | `jina-reranker-v2-base-multilingual` | Jina reranker model id |
| `MCP_RERANK_TIMEOUT` | `5s` | Hard timeout for one rerank call; on timeout the hybrid order is kept and `rerank_failed:timeout` is added to debug signals |
| `MCP_RERANK_TOP_N` | `40` | Number of top hybrid candidates sent to the reranker; clamped to `100` at call time |
| `MCP_SEDIMENT_ENABLED` | `false` | Enable layer-aware retrieval scoring (character always surfaced, surface excluded outside context). Schema migration + backfill always run; only retrieval weighting is gated. See `docs/SEDIMENTATION.md` |
| `MCP_RAG_KEEP_NOISE` | `false` | T49 escape hatch: keep noisy Markdown sections (Table of Contents / References / Changelog / etc.) in the index instead of dropping them at chunking time |
| `MCP_TRIPLE_EXTRACTOR_ENABLED` | `false` | T50 knowledge-graph layer. Enable to fire an async LLM call on every memory write that extracts 3-7 (subj, rel, obj) triples powering `recall_multihop` |
| `MCP_TRIPLE_EXTRACTOR_BASE_URL` | - | OpenAI-compatible `/chat/completions` endpoint (DeepSeek, Together, Groq, Qwen, …); falls back to `OPENAI_BASE_URL` when empty |
| `MCP_TRIPLE_EXTRACTOR_API_KEY` | - | Bearer token for the extractor; falls back to `OPENAI_API_KEY` when empty |
| `MCP_TRIPLE_EXTRACTOR_MODEL` | - | Model id passed to the extractor (e.g. `deepseek-chat`, `qwen2.5-72b-instruct`) |
| `MCP_TRIPLE_EXTRACTOR_TIMEOUT` | `30s` | Per-request timeout for the extractor HTTP call |

### Data paths

The server creates these directories under `MCP_DATA_PATH`:

- `rag-index/` -- SQLite vector index for document search
- `memory-store/` -- SQLite database for agent memories

The recommended solo-local preset stores them under `.agent-memory/`.

### Indexing safety controls

RAG indexing scans supported docs and engineering text files, but you can further reduce risk with explicit controls:

- built-in excluded directories such as `.git`, `.agent-memory`, `node_modules`, `logs`, and `.terraform`
- `MCP_INDEX_EXCLUDE_DIRS` for repo-relative path excludes such as `docs/private,runbooks/internal`
- `MCP_INDEX_EXCLUDE_GLOBS` for glob-style excludes such as `docs/internal/*.md`
- `MCP_REDACT_SECRETS=true` to redact common secret-like lines and private key blocks before indexing

This is especially important if you use hosted embedding providers or shared HTTP mode.

### Automatic session tracking

When the MCP server is running with the default session-tracking policy, it keeps a lightweight background session buffer.

Current behavior:

- successful MCP tool calls are grouped into an active session automatically
- idle timeout or server shutdown triggers a background `close_session` run
- clients can explicitly flush or checkpoint the active session with `notifications/session_event` and `event=task_done|final_summary|checkpoint|reset`
- raw session summaries are persisted automatically
- low-risk updates can auto-apply under the existing `safe_auto_apply` policy
- risky or ambiguous changes are stored as review inbox items instead of silently rewriting maintained knowledge
- periodic raw checkpoints provide crash-recovery breadcrumbs during long sessions

To inspect the inbox, use `project_bank_view view=review_queue` or `agent-memory-mcp project-bank -view review_queue`.
To close an item after manual review, use `resolve_review_item` or `agent-memory-mcp resolve-review-item <id>`.

Example notification payload:

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/session_event",
  "params": {
    "event": "task_done",
    "summary": "Incident stabilized, workaround verified, follow-up is to replace the temporary fix.",
    "context": "payments",
    "service": "api",
    "mode": "incident",
    "tags": ["done", "verification"]
  }
}
```

If you want to tune or disable this behavior, use `MCP_SESSION_TRACKING_ENABLED`, `MCP_SESSION_IDLE_TIMEOUT`, `MCP_SESSION_CHECKPOINT_INTERVAL`, and `MCP_SESSION_MIN_EVENTS`.

### Index integrity and recovery

Document indexing now treats chunk updates and tracking metadata as one logical state.

- each run marks the index state as `dirty` before changing chunks
- a successful final commit flips the state back to `ready` together with `indexed_files`, `embedding_model`, and `last_indexed`
- if a run is interrupted or the final tracking-state commit fails, the next `index_documents` / `agent-memory-mcp index` run detects the dirty state and forces a rebuild

This makes incremental indexing more predictable after crashes, provider interruptions, or storage errors.

### Source-aware ingestion

The indexer now classifies engineering sources and carries that metadata into retrieval.

Supported source types:

- `docs` for `README.md` and general Markdown docs
- `adr` and `rfc` for architecture decision and RFC-style documents
- `changelog` for `CHANGELOG.md` and release-note style docs
- `runbook` and `postmortem` for operational knowledge
- `ci_config` for GitHub Actions, GitLab CI, and Jenkins pipeline files
- `helm`, `terraform`, and `k8s` for source-aware infra files

Use `source_type` when you want to narrow retrieval to a specific class of knowledge:

```bash
agent-memory-mcp search -source-type runbook "ingress rollback"
agent-memory-mcp search -source-type adr "cache invalidation decision"
```

The MCP `semantic_search` tool also accepts `source_type` and `debug`.

### Hybrid retrieval

Search now uses multiple ranking signals instead of cosine similarity alone.

Current ranking signals:

- semantic similarity from the active embedding model
- keyword/BM25-like scoring across chunk title, path, and content
- `source_type` filtering when you want a narrower retrieval set
- recency boost for recently updated operational context
- source-aware weighting so runbooks, changelogs, ADRs, and other source classes can rank higher for matching query intent

The retrieval pipeline now works in two stages:

- semantic top-K candidate generation from the vector index
- keyword top-K candidate generation from a precomputed in-memory keyword index

Only the merged candidate set is reranked. This keeps shared-service retrieval more predictable as the indexed corpus grows.

This means a strong keyword hit in a runbook or changelog can outrank a semantically similar but less task-relevant document.

### Trust-aware retrieval

Retrieval now carries explicit trust metadata for both stored memories and indexed docs.

Each result can expose:

- `source_type`
- `confidence`
- `last_verified_at`
- `owner`
- `freshness_score`

What this means in practice:

- accepted decisions and verified runbooks rank above draft or low-confidence notes when the textual match is similar
- ADRs, runbooks, postmortems, and changelogs can carry different trust weights even before you add a full canonical layer
- CLI `search` / `recall` and MCP `semantic_search` / `recall_memory` now show trust summaries in human-readable output

Engineering workflow tools also stamp stored entries with `last_verified_at` so fresh operational knowledge is easier to trust and rank.

### Explainable retrieval

Use debug mode when you want retrieval to explain why a document was returned.

CLI:

```bash
agent-memory-mcp search -source-type runbook -debug "ingress rollback"
```

MCP:

- call `semantic_search` with `debug: true`
- keep `debug` unset or `false` for the normal compact response

Debug mode adds:

- applied filters such as `source_type=runbook`
- ranking signals used for the response
- candidate counts: indexed, filtered out, discarded as noise, returned
- per-result trust summary: `source`, `confidence`, `freshness`, `owner`, `verified`
- per-result score breakdown: `semantic`, `keyword_raw`, `keyword_normalized`, `recency_boost`, `source_boost`, `confidence_boost`, `final_score`
- per-result applied boosts, for example `keyword_match` or `source_type:runbook`

### Retrieval console

If you want a faster inspection workflow than raw CLI or JSON-RPC calls, use the built-in console in HTTP mode:

```text
/console
```

What it is good for:

- compare normal vs debug retrieval side by side
- inspect document search, raw memory recall, and canonical knowledge recall in one place
- see source type, trust layer, confidence, freshness, owner, and verification time
- open raw JSON for the same structured response the UI is rendering

The console is intentionally lightweight and does not replace MCP tools or CLI workflows.

### Engineering workflow tools

These MCP tools map domain-specific workflows onto the existing memory and retrieval backends.

Recommended starting points:

- `store_decision` for architectural or operational choices such as disabling HPA or pinning an ingress version
- `store_incident` for short-lived operational facts you want to recall during active debugging
- `store_runbook` for procedural steps, rollback instructions, and verification notes
- `store_postmortem` for durable incident learnings and action items
- `close_session` when you want an explicit end-of-session plan with rationale, traces, and review-safe actions
- `accept_session_changes` when the close-session report is low risk and you want to persist the raw summary plus apply safe updates
- `resolve_review_item` when the background inbox already contains a reviewed item and you want to clear it without deleting the audit trail
- `search_runbooks` when you need a fix path and want both memory-stored runbooks and indexed runbook docs
- `recall_similar_incidents` when you are triaging an outage or regression
- `summarize_project_context` at session start to get a compact operational briefing
- `project_bank_view` when you want maintained knowledge by view instead of raw recall results, including `review_queue` for pending background decisions

These workflow tools also add verification metadata so that retrieval can treat newly stored operational knowledge as fresher and more trustworthy than anonymous raw notes.

### Memory consolidation

The memory layer now supports a manual consolidation workflow without deleting historical notes.

Use these MCP tools when the same project knowledge starts to drift:

- `merge_duplicates` to consolidate repeated notes into one primary memory and archive the rest as merged duplicates
- `mark_outdated` to demote stale runbooks, superseded decisions, or obsolete incident notes without losing them
- `promote_to_canonical` to mark the current best memory as canonical knowledge
- `conflicts_report` to surface `duplicate_candidates`, `status_conflict`, and `multiple_canonical` groups

Current behavior:

- merged or archived memories stay accessible, but trust-aware recall pushes them down
- canonical memories get a trust boost and higher minimum importance
- outdated or superseded memories are automatically archived and downranked
- conflict reporting is manual and safe: nothing is deleted unless you explicitly choose to delete it

### Canonical knowledge layer

The project now exposes two distinct layers:

- `raw memory`: captured notes, incidents, decisions, and procedural memories as they were stored
- `canonical knowledge`: confirmed entries projected from memories promoted with `promote_to_canonical`

What this changes:

- `list_canonical_knowledge` gives you the current confirmed knowledge set without raw noise
- `recall_canonical_knowledge` searches only canonical entries
- `summarize_project_context` surfaces canonical knowledge before raw memory sections when canonical entries exist
- trust summaries now show `layer=raw`, `layer=canonical`, or `layer=document`

Migration story:

- existing memories do not need a schema migration
- promote any high-value existing memory with `promote_to_canonical`
- legacy workflow memories that only have tags like `decision` or `service:api` are still recognized by the canonical layer

### Knowledge stewardship

The stewardship layer provides automated and manual knowledge maintenance.

`steward_run` executes a full maintenance cycle in one call:

- scans for duplicate candidates (memories with matching entity/service/context/subject)
- detects conflicting entries (multiple canonical, status disagreements)
- flags stale entries (not verified within the configured threshold)
- suggests canonical promotion candidates (high-importance, active, recognized engineering type)
- generates a structured report with per-action rationale
- with `dry_run=false`, applies safe actions and sends the rest to the stewardship inbox

`drift_scan` compares memory entries against live repo files:

- detects `source_changed` when a referenced file was modified after the memory was last verified
- detects `source_missing` when a referenced file path no longer exists
- flags `stale_unverified` when entries exceed the stale threshold

`verification_candidates` ranks memories that need verification:

- never-verified canonical entries: high urgency
- entries with `verification_failed` or `needs_update` status: high urgency
- stale entries beyond threshold: medium urgency

`steward_inbox` is the single place for all review-required actions. Resolve items with `steward_inbox_resolve` using actions like merge, mark_outdated, promote, verify, suppress, or defer.

Stewardship is auto-enabled in HTTP mode when memory is available. Configure thresholds and mode via `MCP_STEWARD_*` environment variables. See `steward_policy` for runtime configuration.

For a detailed guide, see [Stewardship Guide](docs/STEWARDSHIP.md).

### Temporal knowledge

Memories can carry temporal metadata that tracks when knowledge was valid and how it evolved:

- `valid_from` / `valid_until` — the time window during which this knowledge was true
- `superseded_by` / `replaces` — bidirectional links forming supersession chains
- `observed_at` — when knowledge was first observed (may differ from `created_at`)

`recall_as_of` retrieves knowledge that was valid at a specific timestamp. This is useful for questions like "what was our database strategy in January?" or "what changed between these two dates?"

`knowledge_timeline` shows the chronological evolution of entries matching a query, ordered by `valid_from`.

When `mark_outdated` is called with a superseding entry, the system automatically sets `valid_until` on the old entry and `valid_from` + `replaces` on the new entry, building a navigable chain.

## Security And Operations

Core deployment guidance:

- solo local: keep `MCP_HTTP_MODE=stdio`, prefer `MCP_EMBEDDING_MODE=local-only` if you need no-send semantics
- shared HTTP mode: set `MCP_HTTP_HOST=0.0.0.0`, set `MCP_HTTP_AUTH_TOKEN`, keep TLS at the reverse proxy, and scope `MCP_ALLOW_DIRS` narrowly
- indexing: exclude private runbooks or credentials docs with `MCP_INDEX_EXCLUDE_DIRS` / `MCP_INDEX_EXCLUDE_GLOBS`
- backup: either copy `.agent-memory/` or use `agent-memory-mcp export` for memory-only backups

Reference docs:

- [Stewardship Guide](docs/STEWARDSHIP.md)
- [Security Policy](docs/SECURITY.md)
- [Threat Model](docs/THREAT_MODEL.md)
- [Backup And Restore](docs/BACKUP_RESTORE.md)
- [Shared Service Guide](docs/SHARED_SERVICE.md)

## Architecture

```
┌──────────────────────────────────────────────────┐
│              MCP Protocol Layer                   │
│            (stdio or HTTP/JSON-RPC)               │
├────────────┬──────────┬───────────┬───────────────┤
│Memory Tools│RAG Tools │File Tools │Steward Tools  │
├────────────┼──────────┼───────────┼───────────────┤
│MemoryStore │RAGEngine │ PathGuard │ Steward       │
│  (SQLite)  │          │           │  Service      │
│            │┌────────┐│           │  Scheduler    │
│ Embedder◄──┤│DocSvc  ││           │  Inbox        │
│            ││VecSvc  ││           │  Drift/Verify │
│            │└────────┘│           │  Policy       │
│            │ (SQLite)  │           │  (SQLite)     │
└────────────┴──────────┴───────────┴───────────────┘
```

### Embedding providers

The server supports three embedding providers in `auto` mode:

1. **Jina AI** (primary) -- `jina-embeddings-v3`, native 1024 dimensions, multilingual
2. **OpenAI** (fallback) -- `text-embedding-3-small` or any OpenAI-compatible API. Native dimension is 1536; the server requests `dimensions=1024` via the OpenAI MRL parameter to match the rest of the stack
3. **Ollama** (local fallback) -- `bge-m3`, native 1024 dimensions, runs locally for free

All three are normalized to the same vector dimension (`MCP_EMBEDDING_DIMENSION`, default `1024`), but they are **not** interchangeable: each model has its own embedding space. Matching dimensions do not make cosine similarity safe across different models — that is why the server tags every memory with its `embedding_model` and refuses to mix them at recall time.

What `auto` mode means in practice:

- new memories or new RAG chunks use the first provider that is currently available
- existing memories keep the `embedding_model` they were created with
- semantic recall skips memories whose `embedding_model` does not match the current query model and falls back to text matching for those records
- RAG search refuses to query an index built with a different model and asks you to rebuild it

This avoids the dangerous case where provider fallback returns confident but incorrect semantic matches.

If you change provider or model intentionally, treat it as a migration:

```bash
# rebuild document index for the new embedding model
agent-memory-mcp index

# re-embed stored memories for the new embedding model
agent-memory-mcp reembed

# inspect how many memories still belong to older models
agent-memory-mcp stats -json
```

You can increase the dimension via `MCP_EMBEDDING_DIMENSION` for higher accuracy (for example `3072` with `text-embedding-3-large`), but any dimension or model change requires re-indexing and re-embedding.

If you set `MCP_EMBEDDING_MODE=local-only`, hosted providers are skipped entirely and only Ollama is used for embeddings.

### Why this is better for users

- you can keep using `auto` mode without silent corruption of semantic recall
- you can intentionally migrate to another embedding model without losing stored knowledge
- you can audit model usage with `agent-memory-mcp stats`
- you can prefer local-only operation without worrying about fallback to hosted providers

## macOS service installation

### Homebrew (recommended)

```bash
brew install ipiton/tap/agent-memory-mcp
brew services start agent-memory-mcp
```

See [Installation Options](#installation-options) for details and config location.

### Manual (legacy)

```bash
./scripts/install-macos.sh
```

This builds the binary, creates a `.env` file, and installs a launchd service that auto-starts on login.

Manual control:

```bash
# Status
launchctl list | grep com.agent-memory-mcp

# Start
launchctl load ~/Library/LaunchAgents/com.agent-memory-mcp.plist

# Stop
launchctl unload ~/Library/LaunchAgents/com.agent-memory-mcp.plist
```

## Troubleshooting / FAQ

### "no embedding provider available" or "embedder not configured"

The server tries Jina → OpenAI → Ollama in `auto` mode and stops at the first available one. If none is reachable, embeddings (and therefore semantic recall) are disabled.

- set at least one of `JINA_API_KEY`, `OPENAI_API_KEY`, or run Ollama with `bge-m3` pulled (`ollama pull bge-m3`)
- in `MCP_EMBEDDING_MODE=local-only` only Ollama is consulted; verify `OLLAMA_BASE_URL` (default `http://localhost:11434`) responds

CLI `agent-memory-mcp store …` without an embedder still saves the memory but skips the vector — the record will only be retrievable via text/keyword search until you run `agent-memory-mcp reembed`.

### "embedding model mismatch — index built with X, current is Y"

The RAG index and stored memories are tagged with the embedding model that produced them. If you switch provider or model, retrieval refuses to mix vector spaces:

```bash
agent-memory-mcp index            # rebuild RAG index for the new model
agent-memory-mcp reembed          # re-embed stored memories
agent-memory-mcp stats -json      # check how many memories still belong to older models
```

### "address already in use" — port `18080` taken

Another `agent-memory-mcp` (or unrelated service) holds the port:

```bash
lsof -nP -iTCP:18080 -sTCP:LISTEN
brew services list | grep agent-memory-mcp
```

If two instances were started, stop one (`brew services stop agent-memory-mcp` or `kill <pid>`) or change `MCP_HTTP_PORT`.

### `brew services start agent-memory-mcp` does not start the daemon

Most common causes:

- the formula was installed as a Cask before — uninstall first: `brew uninstall --cask agent-memory-mcp && brew install ipiton/tap/agent-memory-mcp`
- the config file is malformed: `cat $(brew --prefix)/etc/agent-memory-mcp/config.env`
- log: `tail -f $(brew --prefix)/var/log/agent-memory-mcp/mcp-diagnostics.log`

### Hooks not firing in Claude Code

After `agent-memory-mcp setup`, restart Claude Code (hooks load at process start). Verify the merge:

```bash
agent-memory-mcp setup --dry-run     # show what would be written
jq '.hooks' ~/.claude/settings.json  # inspect actual config
```

If you upgraded via `brew upgrade`, re-run `agent-memory-mcp setup --force` so the hook command points to the new binary path. See [docs/HOOKS.md](docs/HOOKS.md).

### "bind to 0.0.0.0 without auth is not allowed"

In HTTP mode the server refuses non-loopback binds without `MCP_HTTP_AUTH_TOKEN`. Either:

- set the bearer token (`MCP_HTTP_AUTH_TOKEN=$(openssl rand -hex 32)`) — recommended
- or, for tightly controlled environments only, set `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true`

### Memory database keeps growing

- run `agent-memory-mcp project-bank canonical_overview` and `… review_queue` to spot raw session summaries that should be promoted or marked outdated
- enable stewardship: `MCP_STEWARD_MODE=scheduled` triggers periodic dedup/stale scans
- prune session checkpoints with the dedup window (`MCP_CHECKPOINT_DEDUP_THRESHOLD`, `MCP_CHECKPOINT_DEDUP_WINDOW`)
- back up first: `agent-memory-mcp export > backup.json` (see [docs/BACKUP_RESTORE.md](docs/BACKUP_RESTORE.md))

### `recall_multihop` returns empty

The multi-hop graph walks the `(subj, rel, obj)` triple corpus. It's empty until you either:

- enable `MCP_TRIPLE_EXTRACTOR_ENABLED=true` and write new memories (extraction is async on every store)
- backfill existing memories with `agent-memory-mcp index-triples` (idempotent, supports `--resume`)

Both paths require `MCP_TRIPLE_EXTRACTOR_*` (see [Key variables](#key-variables)) — extraction calls an OpenAI-compatible `/chat/completions` endpoint.

### "config not reloading" in HTTP/service mode

Changes are picked up within ~30s. Force an immediate reload:

```bash
kill -HUP $(pgrep agent-memory-mcp)
```

Note: HTTP host/port require a full restart (`brew services restart agent-memory-mcp`). Only RAG-related settings hot-reload.

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
