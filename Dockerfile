FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY *.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /agent-memory-mcp .

# ---
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /agent-memory-mcp /usr/local/bin/agent-memory-mcp

# Default data directory
RUN mkdir -p /data/rag-index /data/memory-store /data/logs

ENV MCP_HTTP_MODE=http \
    MCP_HTTP_PORT=8080 \
    MCP_DATA_PATH=/data \
    MCP_RAG_INDEX_PATH=/data/rag-index \
    MCP_MEMORY_DB_PATH=/data/memory-store/memories.db \
    MCP_LOG_PATH=/data/logs/mcp-diagnostics.log \
    MCP_MEMORY_ENABLED=true \
    MCP_RAG_ENABLED=true

EXPOSE 8080

VOLUME ["/data"]

ENTRYPOINT ["agent-memory-mcp"]
