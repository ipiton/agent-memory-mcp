BINARY_NAME=agent-memory-mcp

.PHONY: build run test

build:
	go build -o bin/$(BINARY_NAME) ./...

run:
	go run .

test:
	go test ./...
