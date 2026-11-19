# Bounded document context through MCP

Use `semantic_search` to find a file, then `document_context` to read several
indexed sections when the snippets omit relevant details. The tool is available
in the full profile when RAG is enabled. Grouped calls use `search` with
`action: "context"`.

Pass the index-root-relative `path` and any exact section keys shown by search.
Several keys can share one response budget:

```json
{
  "name": "document_context",
  "arguments": {
    "path": "docs/runbook.md",
    "section_keys": ["Operations > Rollback", "Operations > Open questions"],
    "max_chars": 8000
  }
}
```

Explicit selection expands sections with the same logic as `Engine.ExpandSection`.
Duplicate keys are read once. Unknown keys fail the call. Fragments without
indexed headings share a section keyed by the file path. A suffix distinguishes
that key if a source heading has the same name.

To choose sections by relevance within the file, pass the original `query`
instead of `section_keys`. Each section is scored by the highest similarity
among its chunks. Query selection requires vectors and a query encoder that
matches the index model. A query uses the configured embedding chain, and
results reporting a provider fallback are rejected. Explicit keys and
document-order reads work without an embedding provider.

Omitting both selectors reads the first indexed sections in document order.
`max_sections` defaults to four and accepts one through eight. `max_chars`
defaults to 12,000 and accepts 256 through 32,000. The tool returns the selected
sections in document order.

The character cap counts Unicode code points in the returned section keys and
text. JSON field names, paths and status metadata are outside this budget.
Sections share the budget, and unused space from a short section can be used
by another section. The response includes `chars_returned`, `sections_available`,
`sections_omitted`, `last_modified`, and `truncated`. Each section also reports
its full text length in `chars_total` and whether its text was cut. The top-level
flag covers both truncated text and omitted sections. Relevant facts late in a
long section may fall outside the returned prefix.

The usual path allowlist applies relative to the configured **index root**.
Under project scope the main checkout owns the shared index, so a path returned
by search can be read from a linked worktree even when the file is absent there.
The path must refer to an existing allowed regular file. A database built from
another root is refused. The tool also checks that the index is marked ready and rejects a change in
its publication stamps during the read.

The response contains indexed text. Ordinary edits and changes to symbolic
links may leave older text at an indexed path until the index is refreshed.
`last_modified` is the indexed timestamp. Read the authoritative source when
freshness matters or the returned sections omit a required fact. In a linked
worktree, `repo_read` refers to that worktree; check the main checkout when
you need the live file that backs the shared index.

A completed index that records the resolved root works without a schema change
or per-file backfill. New indexing also records a resolved-root marker so a
recorded physical root cannot later be reinterpreted as a legacy alias.
For an older index without that marker, indexing interprets the recorded alias
using its target at upgrade time. When that target matches the current root,
the stamps are upgraded and previously indexed files under unavailable sources
are retained. If no root is recorded, automatic adoption waits for unavailable
sources that still contain indexed files.
