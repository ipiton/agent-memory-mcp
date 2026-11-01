# Architecture

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
