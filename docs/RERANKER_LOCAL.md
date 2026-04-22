# Local Neural Reranker (Design — not implemented)

T44 ships the Jina hosted `/v1/rerank` adapter. This document sketches the
local, on-box variant — Jina Reranker v3 running without external network — so
we can follow up without re-doing design work.

## Goals

- Zero outbound HTTP for the reranker path (air-gapped deployments).
- Same `reranker.Reranker` interface; no caller changes.
- Comparable latency to the hosted provider on a 40-candidate batch
  (<200 ms p50 on a modest Apple Silicon laptop).
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

## Recommended path

Start with **Option A (Python sidecar)** as a behind-flag experiment:
- Lowest engineering cost to prove the model wins on our eval suite.
- Lets us compare hosted-Jina vs local-Jina directly on real queries.
- If the quality gap is negligible, we can justify a one-time engineering
  spike to convert to Option C and ship a single binary.

Gate on the T43 eval harness — the local reranker earns its place only if
it improves MRR enough to justify the extra machinery, measured against the
no-rerank baseline AND against the hosted Jina variant.

## Operational notes

- Add `MCP_RERANK_PROVIDER=jina-local` in addition to the existing `jina`
  value; wire it to the sidecar endpoint by default (`http://127.0.0.1:8088`).
- Keep the 5-second caller timeout — a local sidecar that hangs is still a
  hang, and the graceful-fallback contract is non-negotiable.
- Log the model hash (sha256 of weights) on startup so we can correlate
  eval deltas with checkpoint versions.

## Out of scope for this doc

- GPU scheduling, model warm pool, request batching — all premature until
  single-request latency is measured on realistic hardware.
- Multi-tenant deployment — the MCP server is single-user by design.
