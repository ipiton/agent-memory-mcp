BINARY_NAME=agent-memory-mcp

.PHONY: build run test vet fmt-check local-smoke eval eval-update eval-rerank

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

# Run the T44 rerank eval variant that compares no-rerank, oracle, and
# reversing rerankers on the same corpus. Logs MRR deltas for inspection.
eval-rerank:
	go test -tags=eval ./internal/rag/eval/ -count=1 -v -run TestRetrievalEval_WithRerankMock
