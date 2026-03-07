#!/bin/bash

# Full reindex: clears existing index and rebuilds from scratch
# Usage: ./scripts/reindex-all.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$PROJECT_ROOT/bin/agent-memory-mcp"
ENV_FILE="$PROJECT_ROOT/.env"

if [ -f "$ENV_FILE" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    set +a
fi

INDEX_DIR="${MCP_RAG_INDEX_PATH:-${MCP_DATA_PATH:-data}/rag-index}"
case "$INDEX_DIR" in
    /*) ;;
    *) INDEX_DIR="$PROJECT_ROOT/$INDEX_DIR" ;;
esac

echo "=== Full RAG Reindex ==="
echo "This will clear the existing index and rebuild it."
echo ""

if [ -z "${JINA_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
    echo "Warning: no JINA_API_KEY or OPENAI_API_KEY set, will use Ollama fallback"
fi

# Clear old index
if [ -d "$INDEX_DIR" ]; then
    echo "Clearing old index..."
    rm -rf "$INDEX_DIR"/*
fi

# Build if needed
if [ ! -f "$BINARY" ]; then
    echo "Building..."
    make -C "$PROJECT_ROOT" build
fi

# Run indexing
echo "Starting indexing..."
echo '{"jsonrpc": "2.0", "method": "tools/call", "params": {"name": "index_documents", "arguments": {}}, "id": 1}' | \
    MCP_RAG_ENABLED=true "$BINARY" 2>/dev/null

echo ""
echo "=== Reindex Complete ==="

if [ -d "$INDEX_DIR" ]; then
    SIZE=$(du -sh "$INDEX_DIR" | awk '{print $1}')
    echo "Index size: $SIZE"
fi
