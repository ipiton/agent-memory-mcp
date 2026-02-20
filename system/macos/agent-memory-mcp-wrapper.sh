#!/bin/bash

# agent-memory-mcp wrapper for launchd
# This script loads environment variables and starts the service

# Change to service directory (set during installation)
cd "__INSTALL_DIR__"

# Set environment variables
export MCP_ROOT="__PROJECT_ROOT__"
export MCP_HTTP_MODE="http"
export MCP_HTTP_PORT="18080"
export MCP_RAG_ENABLED="false"
export MCP_MEMORY_ENABLED="true"

# Start the service
exec "./bin/agent-memory-mcp"
