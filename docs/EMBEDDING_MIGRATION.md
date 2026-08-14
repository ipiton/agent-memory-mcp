# Switching the embedding model

Changing the encoder invalidates every vector you have stored. Both stores —
the memory bank and the RAG index — have to be rebuilt, and until they are, a
mixed store answers queries with whatever fraction of it happens to share the
query's model.

This document is the order of operations plus the two things that are easy to
get wrong.

## What the code already does for you

`Recall` compares `EmbeddingModel` per record against the query's model id and
falls back to text matching on a mismatch, so a half-migrated bank degrades
rather than returning nonsense. That is a safety net, not a plan: text matching
is a much worse answer, and nothing on the surface says the semantic leg was
skipped for that record.

The model id is derived, not configured — `llamacpp:<base-url>:<model>:<dim>`.
Changing the port or the model label alone is enough to invalidate a bank, so
keep both stable once migrated.

## Before anything: check what the service is actually running

The scoring code and the model are one decision, not two. Measure on one binary
and deploy to another and the numbers do not transfer — Granite R2 scored 0.9101
Hit@5 with mean-centering and 0.2870 without it, so shipping the model to a
build that predates centering is a large regression sold as an upgrade. That is
not hypothetical; it is what happened on the first attempt at this migration.

```bash
brew list --versions agent-memory-mcp   # what is installed
git log --oneline -1                    # what you measured against
```

If those differ in anything touching retrieval scoring, release first.

## Order

```sh
# 1. Serve the new model. Match the flags of the server you are replacing —
#    see "Batch size" below before choosing -b/-ub.
llama-server -m <new-model>.gguf --embeddings --pooling <per model card> \
  --host 127.0.0.1 --port 8090 -c 2048 -b 2048 -ub 2048 -np 1 -cb

# 2. Point the service at it and rebuild the bank (~5.5 min for 4.5k records
#    at ~14 records/s on an M-series laptop).
LLAMACPP_BASE_URL=http://127.0.0.1:8090/v1 \
LLAMACPP_EMBEDDING_MODEL=<label> MCP_EMBEDDING_DIMENSION=<dim> \
  agent-memory-mcp reembed

# 3. Rebuild the RAG index. Not optional: two models' vectors in one index
#    are not comparable, and nothing detects the mixture.
agent-memory-mcp index

# 4. Restart the service so the in-memory cache — and the centering mean —
#    are rebuilt from the new vectors.
brew services restart agent-memory-mcp
```

Rehearse on a copy first. `reembed` operates on whatever `MCP_MEMORY_DB_PATH`
points at, so a copy plus a second server port gives you the whole procedure
without touching the live bank — which is exactly how the T74 comparison was
run.

## Batch size, and why it fails quietly

llama-server rejects an embedding input that does not fit in one **physical
batch** (`-ub`), not merely one that exceeds the context window. The error names
the real cause — `input (792 tokens) is too large to process. increase the
physical batch size (current batch size: 512)` — but only in the server's log.
The caller sees an ordinary provider error, `reembed` counts it as a failure,
and the record keeps whatever vector it had.

With `-ub 512`, 4363 of 4565 records failed. With `-ub 2048` — matching the
server being replaced — 8 failed. Set `-b` and `-ub` to the same value and size
them for your longest record, not for your average one.

## Records too large for any batch

The 8 that still failed are 3.7 KB to 102 KB of content. They have no vector
under the current model either, so they are invisible to semantic recall
entirely — a state that predates any migration and is not reported anywhere.
See T120.

If you care about those records, the fix is upstream of the encoder: chunk them
at write time or refuse to store them, rather than raising `-ub` until the
largest one fits.

## Verifying the migration

```sh
sqlite3 <bank>.db \
  "SELECT embedding_model, count(*) FROM memories GROUP BY 1 ORDER BY 2 DESC;"
```

One row should hold almost everything. A second populated row means the
migration stopped halfway, and every record in it is answering queries by text
match.

## Scoring

Keep `MCP_RECALL_CENTERED=true` (the default). Anisotropy varies by model, and
the difference is not marginal: measured on the same corpus, Granite R2 scored
0.2870 Hit@5 on raw cosine and 0.9101 centered, while the model it replaced
scored 0.6232 and 0.7217. A model comparison run on raw cosine can invert the
ranking of the models themselves.
