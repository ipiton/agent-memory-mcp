# Backup And Restore

This document covers practical backup and restore workflows for `agent-memory-mcp`.

## What to back up

For the default solo-local layout, all runtime state lives under:

```text
.agent-memory/
  memory-store/
  rag-index/
  logs/
```

The important data is:

- `memory-store/memories.db` for stored memories
- `rag-index/vectors.db` for indexed document chunks and RAG metadata
- optional logs or stats files if you want operational history

## Backup options

### Option 1. Whole data directory

Best when you want a full restore including RAG index state.

```bash
tar czf agent-memory-backup.tgz .agent-memory
```

### Option 2. Memory-only export

Best when you want a portable JSON snapshot of memories.

```bash
agent-memory-mcp export > memory-backup.json
```

This does not include the RAG vector index. Rebuild that later with:

```bash
agent-memory-mcp index
```

## Restore options

### Restore full data directory

```bash
tar xzf agent-memory-backup.tgz
```

Then restart the MCP client or server from the same project root.

### Restore memory-only export

```bash
agent-memory-mcp import memory-backup.json
agent-memory-mcp index
```

Use `agent-memory-mcp reembed` if you intentionally switched embedding model and want to migrate stored memories to the current model.

## Recommended practice

### Solo local

- Back up `.agent-memory/` periodically
- Use `export` before major experiments or model migrations

### Shared HTTP mode

- Back up the configured `MCP_DATA_PATH`
- Keep the auth token in your normal secret management system, not in the backup archive
- Rebuild the RAG index after restore if the embedding model or dimension changed

## Validation after restore

Run a quick smoke check:

```bash
agent-memory-mcp stats
agent-memory-mcp recall "smoke test"
agent-memory-mcp search "smoke test"
```

If search reports an embedding model mismatch, rebuild the RAG index:

```bash
agent-memory-mcp index
```
