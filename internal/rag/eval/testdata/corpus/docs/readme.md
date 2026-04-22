# Test Corpus README

This directory is a fixture for the RAG retrieval evaluation suite. It
contains realistic but synthetic engineering documentation across four
source types used by the classifier: runbooks, ADRs, postmortems, and
general docs.

## Layout

- `runbooks/` — operational procedures.
- `adrs/` — architecture decision records.
- `postmortems/` — past incident analyses.
- `docs/` — cross-cutting references such as onboarding and troubleshooting.

## How it is used

The evaluation harness in `internal/rag/eval/runner.go` indexes this
directory into a temporary RAG index, runs the queries defined in
`qa.json`, and measures Hit Rate at 5 and Mean Reciprocal Rank.

## Updating the corpus

If you add or rename a file here, update any QA pairs that reference
the old path. After any content change run the harness with the
`-update-baseline` flag to refresh committed metrics.

## Content conventions

- Each file is between 30 and 100 lines.
- Content is plausible, non-sensitive, and contains no real credentials.
- Keywords are chosen to cover the retrieval ranking signals documented
  in the RAG ranking package.
