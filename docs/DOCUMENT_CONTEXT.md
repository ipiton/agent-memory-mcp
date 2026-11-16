# Bounded document context through MCP

Use `semantic_search` to find a file, then `document_context` to read several
indexed sections without loading the whole file. The tool is available in both
profiles when RAG is enabled. Grouped discovery exposes `search` with
`action: "context"`; the flat name remains callable.

```json
{
  "name": "document_context",
  "arguments": {
    "path": "docs/runbook.md",
    "query": "How do we roll back and what remains unverified?",
    "max_chars": 8000,
    "max_sections": 4
  }
}
```

The query selects sections inside this file using the highest chunk cosine
similarity per section. It does not use the global search's document cap, recency
or keyword boosts. Results retain document order, including numeric chunk order.
This is a relevance selector, not an answer-confidence estimate. It requires
vector/model metadata matching the query encoder and refuses embedding fallback.
Text, vectors and model/root metadata are read from one SQLite transaction, so a
concurrent model change cannot validate old vectors against a new model. An
incomplete index is refused; finish indexing before reading context.
It does not automatically read other files or follow links.

To read known sections without an embedding provider, use `section_keys` instead
of `query`:

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

Keys are exact source-derived breadcrumbs from context results. Duplicate keys
are read once. Unknown keys fail the call. Omitting both selectors reads the first
indexed sections in document order. `max_sections` defaults to four and accepts
one through eight; it also limits an explicit key list. `max_chars` defaults to
12,000 and accepts 256 through 32,000.

The character cap counts Unicode code points across **all returned section keys
and text in this call**. JSON field names, path and status metadata are outside
that source-text cap. Record transport size separately when budgeting the entire
tool response. A large section shares the budget with the other selected sections;
unused shares from short sections are reassigned. Heading keys remain intact and
text is cut at a valid Unicode boundary. If even headings cannot fit, sections
are omitted. This is a prefix of each chosen section, so a relevant fact late in
a long section can remain outside the returned context.

The MCP result contains JSON text with `source: "index"`, `selection`,
`max_chars`, `chars_returned`, `last_modified`, `sections_available`,
`sections_omitted`, `truncated`, and `sections`. Each section contains
`section_key`, `text`, `chars_total` (full body length) and its own `truncated` flag.
The top-level flag is true if any section body was cut or indexed sections were
omitted, including intentional selection by the query or explicit key list.

The returned material is **indexed text**, not a live file read or a complete
answer. It retains indexed chunk overlaps; repeated headings with the same
breadcrumb are grouped. Sections excluded during indexing remain unavailable.
`last_modified` is the indexed chunk timestamp, not proof that the current file
matches the index. Read the authoritative file when freshness or a later amendment
matters, and never infer a final approval or measured value from an open question
or plan. `repo_read` remains available in the full profile for source inspection.

Access uses the server's existing path allowlist and symlink resolution. The
source must be an existing regular file, and the indexed-root path must resolve
to that same allowed file. A separate index root cannot be used to read another
document with the same relative name. The persisted index root and the file's
physical source recorded at indexing time must also match. An unchanged configured
symlink is supported; retargeting it refuses old context until indexing records
the new source. The canonical path originally authorized by the allowlist is
passed to the reader unchanged, so a second retarget cannot change that decision.
Ordinary edits at the same physical path can still leave stale
indexed text, as described above. Reusing another repository's database does not
authorize its stored text.

Run `index_documents` once after upgrading an existing index to record file
origins. Unchanged files do not need new embeddings; their verified origins are
recorded on the indexing pass. There is no schema migration. Indexes without
origin/root/completion metadata are refused by this tool.

Regression tests use synthetic documents and local embedding fixtures through the
engine and actual MCP dispatch, including the grouped call form. They need no
downloaded model or private corpus. Search ranking and the default retrieval
evaluation baseline are unchanged.
