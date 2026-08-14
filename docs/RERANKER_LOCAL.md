# Local Neural Reranker (Design — not implemented)

T44 ships the Jina hosted `/v1/rerank` adapter. This document sketches the
local, on-box variant — Jina Reranker running without external network — so we
can follow up without re-doing design work.

> **Read the [work order](#work-order) before starting.** T98 found three ways
> this plan produced an uninterpretable result when executed literally: an
> acceptance criterion naming an arm that does not exist, a comparison varying
> two factors at once, and an eval that silently measures the baseline. The
> order of work below is part of the design, not paperwork around it.

## Goals

- Zero outbound HTTP for the reranker path (air-gapped deployments).
- Same `reranker.Reranker` interface; no caller changes.
- Comparable latency to the hosted provider on a 40-candidate batch
  (<200 ms p50 on a modest Apple Silicon laptop).
  ⚠️ Not yet checked against the model size below (~400 MB fp16 / ~120 MB int8
  for Option C). If the target turns out to be unreachable, that changes the
  runtime choice, not just the number.
- CPU-only default; optional Metal/CUDA acceleration when available.

## Non-goals

- Training or fine-tuning the model.
- Hosting multiple concurrent inference requests — the MCP server is
  single-user; one request in flight at a time is fine.

## Candidate runtimes

### Option A — transformers + ONNX Runtime (via a sidecar)

**Shape:** a small Python sidecar that loads
`jina-reranker-v3` from Hugging Face, wraps it behind a minimal HTTP server
(`POST /rerank` with the same Jina payload shape), and the Go code points
`Config.Endpoint` at `http://127.0.0.1:<port>/v1/rerank`.

Pros:
- Uses the upstream checkpoint as-is, no conversion.
- Python ecosystem has mature support for cross-encoder inference.
- Identical API surface lets us reuse `provider_jina.go` unchanged.

Cons:
- Pulls Python + PyTorch/ONNX into the runtime environment — heavy.
- Requires a process supervisor (launchd/systemd) or in-process
  subprocess manager in the Go binary.
- Cold start adds several seconds to first-call latency.

### Option B — llama.cpp / ggml embeddings pathway

**Shape:** cross-compile the `jina-reranker-v3` weights to GGUF, run via a
Go binding like `go-llama.cpp`. No Python dependency.

Pros:
- Single binary deployment, no sidecar.
- Quantization (Q4_K, Q5_K) fits comfortably on commodity hardware.
- Works offline out of the box.

Cons:
- Cross-encoder support in llama.cpp is less mature than decoder-only LLMs
  — may need patches for sequence-classification heads.
- Conversion tooling is fiddly and version-sensitive.
- No Metal kernel for the specific head type at time of writing.

### Option C — ONNX Runtime via `onnxruntime-go`

**Shape:** convert checkpoint to ONNX, load directly from Go.

Pros:
- Single-binary deployment like Option B.
- ONNX Runtime is a stable, first-class inference runtime.
- Cross-encoder inference fits naturally (one forward pass per doc).

Cons:
- `onnxruntime-go` bindings require CGo and a native ORT build per
  platform — deployment gets trickier on ARM macOS / Linux containers.
- Model size (~400 MB fp16, ~120 MB int8) must ship in the release or be
  fetched on first run.

## Work order

The acceptance criterion is a three-way comparison: no-rerank baseline, hosted
Jina, local Jina. Two of those arms do not exist today, and one of them cannot
be created by engineering work at all — so the steps are ordered by what blocks
what, not by what is interesting.

### Step 1 (operational, blocks everything) — get a Jina key

`/opt/homebrew/etc/agent-memory-mcp/config.env` has no `JINA_API_KEY`: it was
removed on 2026-08-10 after the previous key leaked (file mode 644, and the
value appeared in an agent session transcript). Defaults are
`MCP_RERANK_ENABLED=false` with provider `disabled`
(`internal/config/config.go:456-460`).

Until a new key is issued, the hosted arm cannot be measured, and a run that
skips it silently produces "local beats no-rerank" — which was never the
question. This is an operator action, not an engineering one; it belongs at the
top of the list precisely because engineering cannot route around it.

### Step 2 — measure hosted v2 against no-rerank

`make eval` with `MCP_RERANK_ENABLED=true` and provider `jina`. This establishes
the arm the local variant has to beat. Record the numbers before touching any
sidecar; a comparison assembled after the fact is not one.

### Step 3 — the local arm, on the same model version

Run the sidecar with **`jina-reranker-v2-base-multilingual`** — the same
checkpoint the hosted provider serves (`config.go:457`, default
`JINA_RERANKER_MODEL`).

This is a correction to the earlier draft, which proposed v3 locally and
promised to "compare hosted-Jina vs local-Jina directly". Those arms differ by
hosting *and* by model version, so any MRR delta has two candidate explanations
and no way to separate them. The antidote was already in this document — log
the sha256 of the weights so eval deltas can be tied to a checkpoint — and it
was not applied to its own acceptance criterion.

### Step 4 (separate measurement) — v2 → v3 upgrade

Only after step 3 lands. One factor, one measurement, one conclusion.

## Eval mode is not production mode

Production degrades gracefully by design: a reranker that errors or times out
leaves the hybrid ordering and the caller never learns. During a measurement
that same behaviour is a corruption, not a courtesy — a cold sidecar plausibly
misses the 5-second deadline on the first call, so the opening question of the
QA set scores the no-rerank baseline while the report attributes it to the
reranker. The aggregate never shows this.

Two rules follow, and they only work together:

- **Warm the sidecar before the run.** Issue one throwaway rerank call and wait
  for it to return.
- **Run with `MCP_RETRIEVAL_STRICT=1`.** Added in T99, this turns any fallback
  on the read path into a failed call rather than a quieter answer; `make eval`
  enables it by default (`internal/rag/eval/runner.go`). A run that would have
  silently substituted the baseline fails instead.

Outside the eval, every `semantic_search` response carries a `retrieval` block
naming the path that actually served it — including `rerank_skipped` with the
reason when the reranker did not.

⚠️ The cold-start problem is inferred from two claims of this document (Option A
"cold start adds several seconds" plus the 5-second timeout), not observed —
the sidecar does not exist. Check it on the first run rather than treating it as
established.

## Operational notes

- Add `MCP_RERANK_PROVIDER=jina-local` in addition to the existing `jina`
  value; wire it to the sidecar endpoint by default (`http://127.0.0.1:8088`).
- 🔴 **`reranker.Config.Endpoint` is not a production setting today.** Option A
  assumes it addresses the sidecar, but nothing feeds it outside tests:
  `config.RerankConfig` has no endpoint field and no env var maps to it
  (`internal/reranker/reranker.go`). Shipping Option A means adding
  `MCP_RERANK_ENDPOINT` and answering what it means for an operator to point the
  reranker at an arbitrary host — the API key travels there, which is the class
  T100 fixed for the triple extractor. Decide that with the wiring, not after.
- Keep the 5-second caller timeout in production — a local sidecar that hangs is
  still a hang, and the graceful-fallback contract is non-negotiable there. Eval
  runs are the exception, per the section above.
- Log the model hash (sha256 of weights) on startup so we can correlate eval
  deltas with checkpoint versions. This is also what keeps step 3 honest.

## What this document does not decide

Whether reranking is worth having at all. `make eval` currently reports
MRR 0.968 with no reranker on the committed QA set, and the T44 oracle mock
tops out at 1.000 — so the whole reranking axis is worth at most ~0.032 MRR on
that corpus. T102 asks a broader version of the same question (whether a
summary-tree walk displaces the vector leg entirely) and may make this document
moot. Neither question is settled here.

## Out of scope for this doc

- GPU scheduling, model warm pool, request batching — all premature until
  single-request latency is measured on realistic hardware.
- Multi-tenant deployment — the MCP server is single-user by design.
