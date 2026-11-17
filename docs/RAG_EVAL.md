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
go test -tags=eval ./internal/rag ./internal/rag/eval/

# Or via the Makefile:
make eval
```

`go test ./...` skips the eval test (it stays behind `//go:build eval`), so
the default suite is unaffected. Only the pure-unit metrics tests
(`HitRateAtK`, `MRR`, `AggregateMetrics`) run in the default suite.

## Comparing hybrid and semantic-only ranking

```sh
make eval-compare
```

For each document cap (0 and 1), this indexes the public fixture once and runs both
arms against that same index, encoder, QA set and `K=5`. It prints aggregate Hit@5,
MRR and per-type metrics, with the fusion mode, cutoff and document cap. It does
not write the committed baseline or apply its quality threshold to the comparison
arm. A lower semantic score is a valid measurement; `make eval` still checks the
normal hybrid baseline and also runs the evaluation-only regression tests.

Semantic-only selection uses the existing bounded vector candidates and cosine
scores. It skips keyword retrieval and recency/source/confidence boosts, uses
chunk IDs to break score ties, and retains source filters, trust metadata and the
document cap. If a reranker is configured on the harness, it still runs for both
arms; the default comparison has none. Missing vectors, provider fallback,
background vector loading and reranker failure cannot silently substitute a
different retrieval method.

For a different corpus or encoder, construct the existing `HarnessConfig` with
its paths and optional `Embeddings`, then call `h.CompareRankings(ctx)`. The result
contains aggregate metrics, without source text, document paths or questions.
`RunAll` and `RunQuery` retain their configured hybrid behavior. The engine's
`SearchSemanticForEval` entry point exists only with `-tags=eval`; release builds
and MCP expose no new search mode or `MCP_RAG_FUSION` value.

The stock encoder hashes ASCII tokens into 64 dimensions. It tests pipeline and
ranking mechanics, not multilingual embedding quality. Use a real encoder for
model-quality conclusions, and keep private corpora and question sets out of git.

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
- `type`: one of `simple`, `set`, `multi-hop`, `conditional`, `table`.
- `question`: natural-language query sent to the engine.
- `expected_doc_ids`: list of corpus-relative paths. At least one must
  appear in the top-5 results for the query to count as a hit.
- `source_type` (optional): for conditional queries, restricts the
  engine to that source type filter.

Pick expected doc IDs honestly — if a query could reasonably match
several docs, list them all. Do not tune a query just to hit a single
cherry-picked document.

### `table` questions

`table-*` ask for a value in a row of a long table under `corpus/registers/`,
below the first chunk of that document, phrased with the column names
(`notice period`, `payment status`, `custodian`). The three registers list the
same suppliers, so the supplier name alone does not tell them apart; only the
header does. The column names live in the header row, so they measure whether
a chunk cut from the middle of a table still carries it.

| Splitter | `table` Hit@5 | `table` MRR | all 33 Hit@5 | all 33 MRR |
|---|---|---|---|---|
| before T143 (whitespace cut, header in the first chunk only) | 0.375 | 0.208 | 0.848 | 0.784 |
| T143 (row-boundary cut, header repeated) | 0.750 | 0.521 | 0.939 | 0.860 |

The other 25 questions score the same in both runs. The two remaining misses
(`table-1`, `table-2`) do not reach the top five at all with the fixture
encoder.

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
