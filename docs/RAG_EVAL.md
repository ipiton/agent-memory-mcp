# RAG Retrieval Evaluation

The eval suite measures retrieval quality on a curated fixture corpus. It is
the ground-truth gate for any change to ranking weights, retrieval logic, or
the chunking pipeline.

## Layout

- `internal/rag/eval/` — Go package with metrics, harness, and tests.
- `internal/rag/eval/testdata/corpus/` — fixture markdown (runbooks, ADRs,
  postmortems, docs) indexed during the evaluation.
- `internal/rag/eval/testdata/qa.json` — QA pairs the harness evaluates.
- `internal/rag/eval/testdata/baseline.json` — committed baseline metrics
  the regression gate compares against.

## Running

```bash
# Full eval (requires the eval build tag):
go test -tags=eval ./internal/rag/eval/

# Or via the Makefile:
make eval
```

`go test ./...` skips the eval test (it stays behind `//go:build eval`), so
the default suite is unaffected. Only the pure-unit metrics tests
(`HitRateAtK`, `MRR`, `AggregateMetrics`) run in the default suite.

## Updating the baseline

After an intentional change to ranking weights, retrieval logic, or the
corpus/QA pairs:

```bash
go test -tags=eval ./internal/rag/eval/ -args -update-baseline
# or
make eval-update
```

Commit the refreshed `testdata/baseline.json` alongside the change that
caused the new metrics. The PR description should explain why the delta
is expected.

## Adding QA pairs

Edit `internal/rag/eval/testdata/qa.json`. Each entry has:

- `id`: stable, unique identifier (`simple-7`, `multi-3`, ...).
- `type`: one of `simple`, `set`, `multi-hop`, `conditional`.
- `question`: natural-language query sent to the engine.
- `expected_doc_ids`: list of corpus-relative paths. At least one must
  appear in the top-5 results for the query to count as a hit.
- `source_type` (optional): for conditional queries, restricts the
  engine to that source type filter.

Pick expected doc IDs honestly — if a query could reasonably match
several docs, list them all. Do not tune a query just to hit a single
cherry-picked document.

## Interpreting failure

The regression gate allows a tolerance of `0.05` on both HitRateAtK@5 and
MRR. If either metric drops by more than that, the test fails. When that
happens:

1. Inspect the test log. Misses are logged as
   `MISS id=... q=... top=... expected=...` — it will show which queries
   stopped retrieving the expected docs.
2. Decide whether the regression is a bug or intentional:
   - Bug: revert or fix the ranking / retrieval change so metrics recover.
   - Intentional (e.g. a deliberate new ranking strategy with better
     properties but a temporarily lower metric): refresh the baseline with
     `-update-baseline` and document the rationale in the PR.

## How the harness works

The harness builds a temporary RAG engine against the fixture corpus and
uses a deterministic in-process embedding server (hash-based, content
correlated, L2-normalized). The deterministic embedder is intentionally
not a good semantic model — the harness therefore leans on the engine's
hybrid scoring (keyword BM25-style + recency + source-type boosts) to
produce reproducible rankings across CI machines.

When evaluating a real embedding change, run the eval with that real
embedder in a bespoke branch, record the delta, and then decide whether
the committed fixture baseline should be adjusted.
