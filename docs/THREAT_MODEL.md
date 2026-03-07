# Threat Model

This document describes the primary security assumptions and threat boundaries for `agent-memory-mcp`.

## Security goals

- Keep repository file access constrained to explicit allowlisted paths
- Avoid sending indexed content to hosted embedding providers when local-only operation is required
- Prevent silent mixing of incompatible embedding spaces
- Make shared HTTP mode deployable with clear authentication and TLS expectations

## Trust boundaries

### Local solo mode

- Trusted: local machine, project working copy, local SQLite files under `.agent-memory/`
- Optional external boundary: hosted embedding provider if `MCP_EMBEDDING_MODE=auto` and Jina/OpenAI-compatible credentials are configured

### Shared HTTP mode

- Trusted: host running `agent-memory-mcp`, reverse proxy, backing storage, deployment secret store
- Untrusted: remote MCP clients, network between client and service unless TLS is terminated correctly

## Main threats and mitigations

### 1. Path traversal or over-broad file access

Threat:
- MCP client tries to read files outside the intended project scope

Mitigations:
- `PathGuard` resolves and validates all file paths
- `MCP_ALLOW_DIRS` constrains accessible roots
- Absolute paths and `..` traversal are rejected

### 2. Sensitive content accidentally indexed

Threat:
- Runbooks, docs, or notes include credentials or tokens and get stored in the RAG index or sent to a hosted embedder

Mitigations:
- RAG indexing only scans Markdown from `MCP_INDEX_DIRS`
- Built-in excluded directories skip common generated/internal folders
- `MCP_INDEX_EXCLUDE_DIRS` and `MCP_INDEX_EXCLUDE_GLOBS` provide project-specific excludes
- `MCP_REDACT_SECRETS=true` redacts common secret-like assignments and private key blocks before indexing

Residual risk:
- Redaction is heuristic, not DLP-grade inspection
- Projects with highly sensitive docs should prefer explicit excludes and `MCP_EMBEDDING_MODE=local-only`

### 3. Hosted embedding data exposure

Threat:
- Indexed text is sent to a third-party embedding API

Mitigations:
- `MCP_EMBEDDING_MODE=local-only` disables Jina/OpenAI-compatible providers completely
- In local-only mode, embedding requests fail instead of silently falling back to hosted providers

Residual risk:
- In `auto` mode, indexed text can be sent to the first available hosted provider

### 4. Unauthorized HTTP access

Threat:
- Shared HTTP deployment is exposed without authentication or TLS

Mitigations:
- `MCP_HTTP_AUTH_TOKEN` enables Bearer auth on `/mcp`
- README and security docs require TLS termination at a reverse proxy or load balancer
- Server logs a warning when HTTP mode starts without auth

Residual risk:
- `/health` is unauthenticated for health checks
- Plain HTTP should never be internet-facing without a trusted network boundary

### 5. Silent semantic corruption after model switch

Threat:
- Query embeddings and stored embeddings come from different models, producing misleading recall

Mitigations:
- Each memory stores `embedding_model`
- Semantic recall skips incompatible memories and falls back to text matching
- RAG index metadata stores the embedding model and requires re-index on mismatch
- `agent-memory-mcp reembed` provides explicit migration for memory embeddings

## Recommended deployment posture

### Solo local

- `MCP_HTTP_MODE=stdio`
- `MCP_EMBEDDING_MODE=local-only` if data must stay on the machine
- Narrow `MCP_INDEX_DIRS`
- Use `MCP_INDEX_EXCLUDE_DIRS` / `MCP_INDEX_EXCLUDE_GLOBS` for internal or secret-heavy docs

### Shared service

- `MCP_HTTP_MODE=http`
- Set `MCP_HTTP_AUTH_TOKEN`
- Put TLS and request logging policy at a reverse proxy
- Keep `MCP_ALLOW_DIRS` narrow
- Prefer local embeddings or a vetted hosted provider
- Back up `.agent-memory/` or equivalent data path regularly
