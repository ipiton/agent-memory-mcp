# Troubleshooting

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

### "refusing to index … the index was built from …"

The RAG index belongs to another project: this run shares its `MCP_DATA_PATH` and would remove most of its files. Nothing was changed. While it is refused, search in this project still returns the other project's documents.

- to keep both indexes, give this project its own `MCP_DATA_PATH` (in its `.env`)
- to replace the index on purpose, run `agent-memory-mcp index --allow-root-change`

### 403 "Host is not a loopback name" or "Origin does not match Host"

The server rejected a request that looks like it came from a web page on a foreign name (DNS rebinding protection).

- `Host is not a loopback name`: a loopback instance without a token is reachable only as `localhost` or `127.0.0.1`. Use one of those, or set `MCP_HTTP_AUTH_TOKEN` if you reach it through a custom name or a reverse proxy
- `Origin does not match Host`: a browser client is on a different name than the server. If a reverse proxy rewrites `Host`, forward the original one (`proxy_set_header Host $host`, as in `deploy/nginx/agent-memory-mcp.conf`)

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

If you upgraded via `brew upgrade`, re-run `agent-memory-mcp setup --force` so the hook command points to the new binary path. See [docs/HOOKS.md](HOOKS.md).

### "bind to 0.0.0.0 without auth is not allowed"

In HTTP mode the server refuses non-loopback binds without `MCP_HTTP_AUTH_TOKEN`. Either:

- set the bearer token (`MCP_HTTP_AUTH_TOKEN=$(openssl rand -hex 32)`) — recommended
- or, for tightly controlled environments only, set `MCP_HTTP_INSECURE_ALLOW_UNAUTHENTICATED=true`

### Memory database keeps growing

- run `agent-memory-mcp project-bank canonical_overview` and `… review_queue` to spot raw session summaries that should be promoted or marked outdated
- enable stewardship: `MCP_STEWARD_MODE=scheduled` triggers periodic dedup/stale scans
- prune session checkpoints with the dedup window (`MCP_CHECKPOINT_DEDUP_THRESHOLD`, `MCP_CHECKPOINT_DEDUP_WINDOW`)
- back up first: `agent-memory-mcp export > backup.json` (see [docs/BACKUP_RESTORE.md](BACKUP_RESTORE.md))

### `recall_multihop` returns empty

The multi-hop graph walks the `(subj, rel, obj)` triple corpus. It's empty until you either:

- enable `MCP_TRIPLE_EXTRACTOR_ENABLED=true` and write new memories (extraction is async on every store)
- backfill existing memories with `agent-memory-mcp index-triples` (idempotent, supports `--resume`)

Both paths require `MCP_TRIPLE_EXTRACTOR_*` (see [Key variables](CONFIGURATION.md#key-variables)) — extraction calls an OpenAI-compatible `/chat/completions` endpoint.

### "config not reloading" in HTTP/service mode

Changes are picked up within ~30s. Force an immediate reload:

```bash
kill -HUP $(pgrep agent-memory-mcp)
```

Note: HTTP host/port require a full restart (`brew services restart agent-memory-mcp`). Only RAG-related settings hot-reload.
