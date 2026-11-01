#!/bin/bash
#
# T160: first run of a release artifact on a machine that has never seen it.
# Unpacks the archive, runs setup, then talks to the server over stdio through
# the launch command setup wrote: store_memory in one process, recall_memory in
# the next, so the record has to survive on the default database path.
#
# The environment is empty on purpose. HOME, XDG_CONFIG_HOME and HOMEBREW_PREFIX
# point into the temp directory, so no config, data or API key of the machine it
# runs on can make it pass, and nothing of that machine can be touched.
#
# Usage: scripts/release-smoke.sh dist/agent-memory-mcp-<version>-<os>-<arch>.tar.gz

set -euo pipefail

ARCHIVE="${1:?usage: release-smoke.sh <archive.tar.gz>}"
ARCHIVE="$(cd "$(dirname "$ARCHIVE")" && pwd)/$(basename "$ARCHIVE")"

# agent-memory-mcp-0.14.0-darwin-arm64.tar.gz -> 0.14.0
VERSION="$(basename "$ARCHIVE" .tar.gz)"
VERSION="${VERSION#agent-memory-mcp-}"
VERSION="${VERSION%-*-*}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/release-smoke.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin" "$WORK/home" "$WORK/project"
tar -xzf "$ARCHIVE" -C "$WORK/bin"

MARKER="release-smoke-marker-$$"

fail() {
    echo "FAIL: $1" >&2
    shift
    for log in "$@"; do
        echo "--- $(basename "$log")" >&2
        cat "$log" >&2
    done
    exit 1
}

clean_env() {
    env -i \
        HOME="$WORK/home" \
        XDG_CONFIG_HOME="$WORK/home/.config" \
        HOMEBREW_PREFIX="$WORK/brew" \
        TMPDIR="$WORK" \
        PATH=/usr/bin:/bin \
        "$@"
}

# Local-only with nothing listening: no embedder, no hosted provider, no network.
cat >"$WORK/project/.env" <<'EOF'
MCP_EMBEDDING_MODE=local-only
OLLAMA_BASE_URL=http://127.0.0.1:9
EOF

echo "== setup =="
BINARY="$WORK/bin/agent-memory-mcp"
(cd "$WORK/project" && clean_env "$BINARY" setup) || fail "setup exited non-zero"
(cd "$WORK/project" && clean_env "$BINARY" setup claude-code --yes) || fail "setup claude-code exited non-zero"
jq -e '.hooks | length > 0' "$WORK/home/.claude/settings.json" >/dev/null ||
    fail "setup wrote no hooks"
LAUNCH_COMMAND="$(jq -r '.mcpServers.memory.command' "$WORK/home/.claude.json")"
jq -r '.mcpServers.memory.args[]' "$WORK/home/.claude.json" >"$WORK/launch-args"
LAUNCH_ARGS=()
while IFS= read -r arg; do LAUNCH_ARGS+=("$arg"); done <"$WORK/launch-args"

# session <name> <tool> <json arguments>: one server process, one tool call. The
# server answers in order and exits on EOF, so stdin can be written up front. The
# alarm turns a hang into a failure on systems without timeout(1).
session() {
    local name="$1" tool="$2" arguments="$3"
    jq -cn '{jsonrpc: "2.0", id: 1, method: "initialize", params: {protocolVersion: "2025-06-18", capabilities: {}, clientInfo: {name: "release-smoke", version: "1"}}}' >"$WORK/$name.in"
    jq -cn '{jsonrpc: "2.0", method: "notifications/initialized"}' >>"$WORK/$name.in"
    jq -cn --arg tool "$tool" --argjson arguments "$arguments" \
        '{jsonrpc: "2.0", id: 2, method: "tools/call", params: {name: $tool, arguments: $arguments}}' >>"$WORK/$name.in"

    clean_env perl -e 'alarm 120; exec @ARGV' "$LAUNCH_COMMAND" "${LAUNCH_ARGS[@]}" \
        <"$WORK/$name.in" >"$WORK/$name.out" 2>"$WORK/$name.err" ||
        fail "$name: server exited non-zero or hung" "$WORK/$name.out" "$WORK/$name.err"

    jq -e 'select(.id == 2) | (.error == null) and (.result.isError != true)' "$WORK/$name.out" >/dev/null ||
        fail "$name: $tool returned an error" "$WORK/$name.out" "$WORK/$name.err"
}

echo "== store_memory =="
RECORD="$MARKER is the record to find"
session store store_memory "$(jq -cn --arg c "$RECORD" '{content: $c, type: "working"}')"
SERVER_VERSION="$(jq -r 'select(.id == 1) | .result.serverInfo.version' "$WORK/store.out")"
[ "$SERVER_VERSION" = "$VERSION" ] ||
    fail "server reports version '$SERVER_VERSION', the archive is '$VERSION'" "$WORK/store.out"

# Look for the whole record, not the query: an empty result echoes the query back.
echo "== recall_memory (new process) =="
session recall recall_memory "$(jq -cn --arg q "$MARKER" '{query: $q}')"
jq -r 'select(.id == 2) | .result.content[].text' "$WORK/recall.out" | grep -qF "$RECORD" ||
    fail "the stored record did not come back" "$WORK/recall.out" "$WORK/recall.err"

echo "Release smoke passed: $VERSION on $(uname -s)/$(uname -m)"
