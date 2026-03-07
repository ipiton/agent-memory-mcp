#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="${AGENT_MEMORY_MCP_BIN:-$PROJECT_ROOT/bin/agent-memory-mcp}"
ENV_FILE="$PROJECT_ROOT/.env"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
MEMORY_TEXT="solo local smoke ${STAMP}"

cd "$PROJECT_ROOT"

if [ ! -f "$ENV_FILE" ]; then
    echo "Missing $ENV_FILE"
    echo "Create it first:"
    echo "  cp .env.example .env"
    exit 1
fi

if [ ! -x "$BINARY" ]; then
    echo "Building binary..."
    make build
fi

echo "== Memory smoke =="
"$BINARY" store -content "$MEMORY_TEXT" -type working -tags "smoke,local"
"$BINARY" recall "solo local smoke" -limit 1

echo ""
echo "== RAG smoke =="
"$BINARY" index
"$BINARY" search "agent memory" -limit 3

echo ""
echo "Solo local smoke completed."
