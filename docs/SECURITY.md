# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| latest  | Yes       |

## Reporting a vulnerability

If you discover a security vulnerability, please report it responsibly.

**Do not open a public issue.**

Instead, please email security concerns to the maintainers or use [GitHub's private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability).

### What to include

- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### Response timeline

- **Acknowledgment**: within 48 hours
- **Assessment**: within 1 week
- **Fix**: depends on severity, typically within 2 weeks for critical issues

## Security considerations

Reference docs:

- [Threat Model](THREAT_MODEL.md)
- [Backup And Restore](BACKUP_RESTORE.md)
- [Shared Service Guide](SHARED_SERVICE.md)

### API keys

- Never commit API keys (Jina AI, Ollama) to the repository
- Use environment variables or `.env` files (gitignored by default)
- The `.env.example` file contains only placeholder values

### File access

- The server uses a **PathGuard** that restricts file access to explicitly allowlisted paths
- Path traversal attacks are prevented by resolving and validating all paths
- Only paths under `MCP_ROOT` and `MCP_ALLOW_DIRS` are accessible

### HTTP mode

- `MCP_HTTP_MODE=http` enables the HTTP MCP endpoint
- `MCP_HTTP_HOST` defaults to `127.0.0.1`, so a quick local HTTP run stays loopback-only by default
- for shared deployments set `MCP_HTTP_HOST=0.0.0.0` and `MCP_HTTP_AUTH_TOKEN`; requests to `/mcp` must then send `Authorization: Bearer <token>`
- the server refuses to start on a non-loopback host without `MCP_HTTP_AUTH_TOKEN`, unless you explicitly set `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true`
- The server intentionally listens on plain HTTP; terminate TLS at a reverse proxy or load balancer
- `/health` is intended for health checks and does not carry MCP traffic
- `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true` is an explicit unsafe override and should only be used for tightly controlled environments
- `deploy/nginx/agent-memory-mcp.conf` is the reference reverse proxy recipe for TLS termination and forwarding `Authorization`
- `/console` serves a static retrieval inspection page; live data requests from the page go through `/console/api/query` and use the same Bearer token model as `/mcp`

### Data storage

- Memory and vector index data are stored in local SQLite databases
- No data is sent to external services except embedding API calls (Jina AI, OpenAI-compatible APIs, or Ollama)
- Embedding API calls contain only document text chunks, no metadata or credentials

### Indexing safety

- RAG indexing only scans Markdown files from `MCP_INDEX_DIRS`
- Built-in directory excludes skip common non-source or generated folders such as `.git`, `.agent-memory`, `node_modules`, `logs`, and `.terraform`
- `MCP_INDEX_EXCLUDE_DIRS` adds repo-relative path excludes
- `MCP_INDEX_EXCLUDE_GLOBS` adds glob-style path excludes such as `docs/internal/*.md`
- `MCP_REDACT_SECRETS=true` redacts common secret-like assignments and private key blocks before content is written to the RAG index or sent to embedding providers

### Local-only mode

- Set `MCP_EMBEDDING_MODE=local-only` to disable hosted embedding providers
- In local-only mode, the server will not call Jina AI or OpenAI-compatible embedding APIs even if their keys are present
- Embedding traffic is limited to the configured local Ollama endpoint (default: `http://localhost:11434`)
- If Ollama is unavailable, embedding requests fail with a local-only specific error instead of silently falling back to hosted providers

### Embedding model safety

- The server tracks which embedding model was used for each stored memory
- Semantic recall does not mix memories from different embedding spaces, even if vector dimensions match
- Memories from older models remain available through text matching and filters until you run `agent-memory-mcp reembed`
- RAG indexes are tied to the embedding model they were built with; switching models requires `agent-memory-mcp index`
