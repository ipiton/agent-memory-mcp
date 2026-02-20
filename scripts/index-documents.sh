#!/bin/bash

# Index documents for RAG search
# Usage: ./scripts/index-documents.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$PROJECT_ROOT/bin/agent-memory-mcp"

echo "Starting document indexing..."

# Check API key
if [ -z "${JINA_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
    echo "No JINA_API_KEY or OPENAI_API_KEY set, will use Ollama fallback"
fi

# Build if needed
if [ ! -f "$BINARY" ]; then
    echo "Building..."
    make -C "$PROJECT_ROOT" build
fi

echo "Indexing (this may take 30-60 seconds)..."
echo ""

echo '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}}
{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "index_documents", "arguments": {}}}' | \
    timeout 90 "$BINARY" -root "$PROJECT_ROOT" 2>/dev/null | \
    tail -1 | python3 -m json.tool 2>/dev/null || echo "Done"

echo ""

# Show index stats
if [ -d "$PROJECT_ROOT/data/rag-index" ]; then
    SIZE=$(du -sh "$PROJECT_ROOT/data/rag-index" | awk '{print $1}')
    echo "Index created: $SIZE"
else
    echo "Warning: index directory not found"
fi
