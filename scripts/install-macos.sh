#!/bin/bash

# agent-memory-mcp Installation Script for macOS
# Sets up agent-memory-mcp as a launchd service on macOS

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SERVICE_NAME="com.agent-memory-mcp"
SERVICE_TEMPLATE="$PROJECT_ROOT/system/macos/$SERVICE_NAME.plist"
ENV_TEMPLATE="$PROJECT_ROOT/.env.example"
ENV_FILE="$PROJECT_ROOT/.env"
BINARY_DST="$PROJECT_ROOT/bin/agent-memory-mcp"

# Logging functions
log_info()    { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
log_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
log_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

check_macos() {
    if [[ "$OSTYPE" != "darwin"* ]]; then
        log_error "This script is designed for macOS only"
        exit 1
    fi
}

check_dependencies() {
    if ! command -v go &> /dev/null; then
        log_error "Go is required but not installed"
        log_info "Install Go: https://go.dev/doc/install"
        exit 1
    fi
}

create_directories() {
    log_info "Creating necessary directories..."
    mkdir -p "$PROJECT_ROOT/bin"
    mkdir -p "$PROJECT_ROOT/logs"
    mkdir -p "$PROJECT_ROOT/data/rag-index"
    mkdir -p "$PROJECT_ROOT/data/memory-store"
    log_success "Directories created"
}

build_binary() {
    log_info "Building agent-memory-mcp..."

    if [[ ! -f "$PROJECT_ROOT/go.mod" ]]; then
        log_error "go.mod not found in $PROJECT_ROOT"
        exit 1
    fi

    cd "$PROJECT_ROOT"
    if ! go build -o "$BINARY_DST" .; then
        log_error "Failed to build binary"
        exit 1
    fi

    log_success "Binary built: $BINARY_DST"
}

setup_environment() {
    if [[ -f "$ENV_FILE" ]]; then
        log_warning "Environment file already exists: $ENV_FILE"
        read -p "Overwrite? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            log_info "Skipping environment setup"
            return
        fi
    fi

    log_info "Setting up environment configuration..."

    if [[ ! -f "$ENV_TEMPLATE" ]]; then
        log_error "Environment template not found: $ENV_TEMPLATE"
        exit 1
    fi

    cp "$ENV_TEMPLATE" "$ENV_FILE"
    sed -i '' "s|__PROJECT_ROOT__|$PROJECT_ROOT|g" "$ENV_FILE"

    log_success "Environment file created: $ENV_FILE"
    log_warning "Please edit $ENV_FILE and set your API keys"
}

install_service() {
    log_info "Installing launchd service..."

    if [[ ! -f "$SERVICE_TEMPLATE" ]]; then
        log_error "Service template not found: $SERVICE_TEMPLATE"
        exit 1
    fi

    if [[ ! -f "$BINARY_DST" ]]; then
        log_error "Binary not found: $BINARY_DST"
        exit 1
    fi

    # Create customized plist with actual paths
    mkdir -p "$HOME/Library/LaunchAgents"
    sed "s|__INSTALL_DIR__|$PROJECT_ROOT|g" "$SERVICE_TEMPLATE" > "$HOME/Library/LaunchAgents/$SERVICE_NAME.plist"

    log_success "Service installed to: $HOME/Library/LaunchAgents/$SERVICE_NAME.plist"
}

start_service() {
    log_info "Starting service..."

    if launchctl list 2>/dev/null | grep -q "$SERVICE_NAME"; then
        log_info "Service already loaded, restarting..."
        launchctl unload "$HOME/Library/LaunchAgents/$SERVICE_NAME.plist" 2>/dev/null || true
        sleep 2
    fi

    launchctl load "$HOME/Library/LaunchAgents/$SERVICE_NAME.plist"
    sleep 2

    if launchctl list 2>/dev/null | grep -q "$SERVICE_NAME"; then
        log_success "Service started successfully"
    else
        log_error "Service failed to start"
        log_info "Check logs: tail -f $PROJECT_ROOT/logs/agent-memory-mcp.err.log"
        exit 1
    fi
}

test_service() {
    log_info "Testing service..."
    sleep 3
    if curl -s "http://localhost:18080/health" > /dev/null 2>&1; then
        log_success "Service is responding on port 18080"
    else
        log_warning "Health check failed - it may still be starting up"
        log_info "Check logs: tail -f $PROJECT_ROOT/logs/agent-memory-mcp.err.log"
    fi
}

print_usage() {
    cat << EOF
agent-memory-mcp Installation Script for macOS

Usage: $0 [OPTIONS]

Options:
  -h, --help          Show this help message
  --no-service        Skip service installation (manual startup only)
  --test-only         Only run tests on existing installation

After installation:
  1. Edit $ENV_FILE and set your API keys
  2. The service will auto-start on login

Manual control:
  Start:  launchctl load ~/Library/LaunchAgents/$SERVICE_NAME.plist
  Stop:   launchctl unload ~/Library/LaunchAgents/$SERVICE_NAME.plist
  Status: launchctl list | grep $SERVICE_NAME

EOF
}

main() {
    local skip_service=false
    local test_only=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            -h|--help)      print_usage; exit 0 ;;
            --no-service)   skip_service=true; shift ;;
            --test-only)    test_only=true; shift ;;
            *)              log_error "Unknown option: $1"; print_usage; exit 1 ;;
        esac
    done

    echo -e "${BLUE}=== agent-memory-mcp Installation for macOS ===${NC}"
    echo

    check_macos
    check_dependencies

    if [[ "$test_only" == true ]]; then
        test_service
        exit 0
    fi

    create_directories
    build_binary
    setup_environment

    if [[ "$skip_service" == false ]]; then
        install_service
        start_service
        test_service
    fi

    echo
    log_success "Installation completed!"
    echo
    log_info "Next steps:"
    echo "  1. Edit your environment file: $ENV_FILE"
    echo "  2. Set JINA_API_KEY or configure Ollama for embeddings"
    echo "  3. Check service: launchctl list | grep $SERVICE_NAME"
    echo
    log_info "Service will auto-start on login"
}

main "$@"
