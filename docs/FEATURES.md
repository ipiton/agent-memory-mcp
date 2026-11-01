# Features

What agent-memory-mcp is for and what it does beyond a plain memory store.

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
## Features

- **Automatic session capture** — Claude Code hooks auto-capture knowledge at session end, save checkpoints before context compression, and compile pending summaries at session start
- **Typed persistent memory** with 4 types: episodic, semantic, procedural, working
- **Hybrid retrieval** that combines embeddings with keyword/BM25-like ranking
- **RAG indexing** for project docs, changelogs, and knowledge archives (enabled by default in stdio/CLI mode; **disabled by default in the Homebrew service preset** — see [Installation Options](INSTALLATION.md#installation-options))
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
- **Better visibility**: `stats` and `memory_stats` show how many memories belong to each embedding model, and name the ones no semantic query can reach — records the encoder refused outright, and records embedded from their opening only
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
- **Age-aware recall (opt-in)**: recall scoring can apply an exponential age decay so stale memories sink while canonical knowledge and character/identity stay put — off by default since T121 measured it costing Hit@5 0.7217 → 0.1942 at the old 30-day setting. Enable with `MCP_RECALL_HALFLIFE_DAYS`, and note that `MCP_RECALL_DECAY_TYPES` (default `working`) decides which types age at all — the type axis matters more than the rate
- **Self-healing duplicate cleanup**: the steward can auto-merge high-confidence, near-identical duplicate groups instead of only queuing them for review — opt-in and guarded by a content-similarity threshold so nothing unique is archived (`auto_merge_duplicate_min_confidence` in `steward_policy`)
- **No more double session-close records**: closing a task folds the auto-captured session summary into the finalize record instead of writing a second near-identical memory per slug, cutting the duplicate pairs the steward used to flag as false contradictions
