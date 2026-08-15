BINARY_NAME=agent-memory-mcp

.PHONY: build run test vet fmt-check local-smoke eval eval-update eval-rerank eval-corpus eval-real eval-recall eval-permute eval-flatten

build:
	go build -o bin/$(BINARY_NAME) ./cmd/agent-memory-mcp

run:
	go run ./cmd/agent-memory-mcp

test:
	go test ./...

# `go vet ./...` does not see files behind a build tag, which is how the eval
# harness sat broken for four months (T112): the config struct was reshaped, the
# tagged file kept naming the old fields, and build, vet and test all skipped it.
# Every tag in use has to be vetted explicitly or it rots the same way again.
vet: fmt-check
	go vet ./...
	go vet -tags=eval ./...
	go vet -tags=corpus ./...

# Nothing checked formatting, so six files had drifted out of gofmt shape on
# main — invisible until an unrelated `gofmt -w` pulled them into a diff.
fmt-check:
	@unformatted="$$(gofmt -l ./cmd ./internal)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

local-smoke:
	./scripts/local-smoke.sh

# Run the RAG retrieval evaluation harness. Fails if Hit Rate@5 or MRR
# regress by more than 0.05 from the committed baseline.
eval:
	go test -tags=eval ./internal/rag/eval/

# Refresh the committed retrieval baseline. Commit the updated
# internal/rag/eval/testdata/baseline.json alongside the change that
# caused the metrics to move.
eval-update:
	go test -tags=eval ./internal/rag/eval/ -args -update-baseline

# T119: the committed QA set is saturated (Hit@5 = 1.0), so nothing that has to
# be decided "by measured win" can be decided on it. These two targets build a
# harder set out of a local task archive and run against it with a real
# encoder. The generated corpus is private and stays out of git — the toy
# corpus above remains the CI gate.
EVAL_TASK_ARCHIVE ?= $(HOME)/Sema/tasks/archive
EVAL_OUT_DIR      ?= $(CURDIR)/.eval-corpus
EVAL_MAX_TASKS    ?= 150

eval-corpus:
	MCP_EVAL_TASK_ARCHIVE=$(EVAL_TASK_ARCHIVE) MCP_EVAL_OUT_DIR=$(EVAL_OUT_DIR) \
	MCP_EVAL_MAX_TASKS=$(EVAL_MAX_TASKS) \
	go test -tags=eval ./internal/rag/eval/ -count=1 -v -run TestGenerateEvalCorpus

eval-real:
	set -a; [ -f $(EVAL_SERVICE_CONFIG) ] && . $(EVAL_SERVICE_CONFIG); set +a; \
	MCP_EVAL_OUT_DIR=$(EVAL_OUT_DIR) MCP_EVAL_CORPUS_NAME=$${MCP_EVAL_CORPUS_NAME:-corpus} \
	LLAMACPP_BASE_URL=$${LLAMACPP_BASE_URL:-http://127.0.0.1:8090/v1} \
	go test -tags=eval ./internal/rag/eval/ -count=1 -v -timeout 30m -run TestRetrievalEvalGenerated

# T102: the permutation arm. Builds a copy of the corpus with heading texts
# shuffled corpus-wide (levels untouched) and runs the same QA set against it,
# so the two runs differ in exactly one thing: whether a section's label says
# anything about the content under it.
eval-permute:
	MCP_EVAL_OUT_DIR=$(EVAL_OUT_DIR) \
	go test -tags=eval ./internal/rag/eval/ -count=1 -v -run TestPermuteEvalCorpusHeadings
	$(MAKE) eval-real MCP_EVAL_CORPUS_NAME=corpus-permuted

# T102, discriminating arm: headings demoted to bold text. The words stay put,
# the section tree does not, so baseline minus this run is what the structure
# itself was worth — which the permutation arm cannot tell apart from the words.
eval-flatten:
	MCP_EVAL_OUT_DIR=$(EVAL_OUT_DIR) \
	go test -tags=eval ./internal/rag/eval/ -count=1 -v -run TestFlattenEvalCorpusHeadings
	$(MAKE) eval-real MCP_EVAL_CORPUS_NAME=corpus-flat

# The instrument with actual headroom (Hit@5 = 0.62): memory recall, not
# document search. Runs against a COPY of the bank — never the live file, since
# opening a store migrates and writes to it.
EVAL_BANK_SRC ?= /opt/homebrew/var/agent-memory-mcp/memory-store/memories.db
EVAL_BANK_COPY ?= $(EVAL_OUT_DIR)/bank.db

# The harness assembles its scoring and encoder settings from config.LoadFromEnv,
# so it has to see the environment the service sees. Without this the run picks
# up code defaults instead of the deployed ones — which is how a post-migration
# run reported Hit@5 0.0232 while comparing granite queries against a bank the
# harness thought was embeddinggemma.
EVAL_SERVICE_CONFIG ?= /opt/homebrew/etc/agent-memory-mcp/config.env

eval-recall:
	@mkdir -p $(EVAL_OUT_DIR)
	cp $(EVAL_BANK_SRC) $(EVAL_BANK_COPY)
	-cp $(EVAL_BANK_SRC)-wal $(EVAL_BANK_COPY)-wal
	set -a; [ -f $(EVAL_SERVICE_CONFIG) ] && . $(EVAL_SERVICE_CONFIG); set +a; \
	MCP_EVAL_MEMORY_DB=$(EVAL_BANK_COPY) MCP_EVAL_TASK_ARCHIVE=$(EVAL_TASK_ARCHIVE) \
	LLAMACPP_BASE_URL=$${LLAMACPP_BASE_URL:-http://127.0.0.1:8090/v1} \
	go test -tags=eval ./internal/memory/ -count=1 -v -timeout 30m -run TestRecallEval

# Run the T44 rerank eval variant that compares no-rerank, oracle, and
# reversing rerankers on the same corpus. Logs MRR deltas for inspection.
eval-rerank:
	go test -tags=eval ./internal/rag/eval/ -count=1 -v -run TestRetrievalEval_WithRerankMock
