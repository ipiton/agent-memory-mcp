BINARY_NAME=agent-memory-mcp

.PHONY: build run test vet local-smoke eval eval-update eval-rerank

build:
	go build -o bin/$(BINARY_NAME) ./cmd/agent-memory-mcp

run:
	go run ./cmd/agent-memory-mcp

test:
	go test ./...

vet:
	go vet ./...

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
