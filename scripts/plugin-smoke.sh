#!/bin/bash
#
# T167, T168: the Claude Code plugin on a machine that has never seen it, without
# Claude Code. Runs the launcher the way plugin/.mcp.json and plugin/hooks/hooks.json
# do, against a local server standing in for GitHub Releases.
#
# The environment is empty on purpose: HOME and the plugin data live in the temp
# directory, so nothing of the machine it runs on can make it pass or be touched.
#
# Usage: scripts/plugin-smoke.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/plugin-smoke.XXXXXX")"
SERVER_PID=""
cleanup() {
	if [ -n "$SERVER_PID" ]; then
		kill "$SERVER_PID" 2>/dev/null || true
		wait "$SERVER_PID" 2>/dev/null || true
	fi
	rm -rf "$WORK"
}
trap cleanup EXIT

case "$(uname -s)" in Darwin) OS=darwin ;; *) OS=linux ;; esac
case "$(uname -m)" in arm64 | aarch64) ARCH=arm64 ;; *) ARCH=amd64 ;; esac

PLUGIN="$WORK/plugin"
DATA="$WORK/plugin-data"
HOME_DIR="$WORK/home"
PROJECT="$WORK/project"
RELEASES="$WORK/releases"
mkdir -p "$HOME_DIR" "$PROJECT/docs" "$DATA"
cp -R "$ROOT/plugin" "$PLUGIN"
# The repository pins a real release; the archives here are built locally.
: >"$PLUGIN/checksums.txt"
git -C "$PROJECT" init -q
printf '# Runbook\nrollback the frobnicator safely\n' >"$PROJECT/docs/runbook.md"

(cd "$ROOT" && go build -o "$WORK/agent-memory-mcp" ./cmd/agent-memory-mcp)

publish() { # version
	mkdir -p "$RELEASES/v$1"
	tar -czf "$RELEASES/v$1/agent-memory-mcp-$1-$OS-$ARCH.tar.gz" -C "$WORK" agent-memory-mcp
}
pin() { # version
	sed -i.bak -E "s/(\"version\": \")[^\"]*/\1$1/" "$PLUGIN/.claude-plugin/plugin.json"
	(cd "$RELEASES/v$1" && shasum -a 256 "agent-memory-mcp-$1-$OS-$ARCH.tar.gz") >"$PLUGIN/checksums.txt"
}

PORT="$(python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
python3 -I -m http.server "$PORT" --bind 127.0.0.1 --directory "$RELEASES" >/dev/null 2>&1 &
SERVER_PID=$!

plugin_env() {
	env -i PATH="$PATH" HOME="$HOME_DIR" XDG_CONFIG_HOME="$WORK/xdg" HOMEBREW_PREFIX="$WORK/brew" \
		CLAUDE_PLUGIN_DATA="$DATA" CLAUDE_PROJECT_DIR="$PROJECT" \
		AMM_PLUGIN_RELEASE_BASE="${RELEASE_BASE:-http://127.0.0.1:$PORT}" "$@"
}

# Asks the server over stdio; prints each reply's text or error, one per line.
session() { # json array of [tool, args]
	plugin_env python3 -I -c '
import json, os, subprocess, sys
p = subprocess.Popen([sys.argv[1], "serve"], cwd=os.environ["CLAUDE_PROJECT_DIR"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
def rpc(i, method, params):
    p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method, "params": params}) + "\n"); p.stdin.flush()
    return json.loads(p.stdout.readline())
rpc(1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "0"}})
print("tools:", len(rpc(2, "tools/list", {})["result"]["tools"]))
for i, (name, args) in enumerate(json.loads(sys.argv[2]), 3):
    reply = rpc(i, "tools/call", {"name": name, "arguments": args})
    print(name + ":", json.dumps(reply.get("result", reply), ensure_ascii=False))
p.terminate(); p.wait(10)
' "$PLUGIN/scripts/launch.sh" "$1"
}

raw_summaries() {
	plugin_env python3 -I -c '
import glob, os, sqlite3
paths = glob.glob(os.environ["HOME"] + "/.local/share/agent-memory-mcp/projects/*/memory-store/memories.db")
assert len(paths) == 1, paths
print(sqlite3.connect(paths[0]).execute("SELECT COUNT(*) FROM memories WHERE content LIKE ?", ("%rollback script%",)).fetchone()[0])
'
}

check() { # description, haystack, needle
	if ! grep -qF -- "$3" <<<"$2"; then
		echo "FAIL: $1: no '$3' in:" >&2
		echo "$2" >&2
		exit 1
	fi
	echo "ok: $1"
}

VERSION="$(sed -n 's/.*"version"[^"]*"\([^"]*\)".*/\1/p' "$PLUGIN/.claude-plugin/plugin.json")"
BINARY="$DATA/bin/$VERSION/agent-memory-mcp"
TRANSCRIPT="$WORK/transcript.jsonl"
cat >"$TRANSCRIPT" <<'JSONL'
{"type":"user","message":{"role":"user","content":"please fix the rollback script"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Fixed the rollback script by checking the exit code of kubectl before the second step, and added a regression test that covers the failing path in the deployment flow."}]}}
JSONL
session_end() { # session id
	printf '{"session_id":"%s","hook_event_name":"SessionEnd","transcript_path":"%s","cwd":"%s"}\n' "$1" "$TRANSCRIPT" "$PROJECT" |
		plugin_env "$PLUGIN/scripts/launch.sh" hook auto-capture --hook-event
}

# A hook before the server has ever started: nothing to run, nothing to break.
session_end hook-before-install >/dev/null 2>&1 || { echo "FAIL: a hook failed with no binary" >&2; exit 1; }
[ ! -e "$BINARY" ] || { echo "FAIL: a hook downloaded the binary" >&2; exit 1; }
echo "ok: a hook without a binary is silent and downloads nothing"

# What plugin/.mcp.json would pass if a Claude Code version did not expand it.
set +e
OUT="$(cd "$PROJECT" && plugin_env AMM_PLUGIN_DATA='${CLAUDE_PLUGIN_DATA}' "$PLUGIN/scripts/launch.sh" serve 2>&1 </dev/null)"
STATUS=$?
set -e
[ "$STATUS" -ne 0 ] || { echo "FAIL: started with an unexpanded data directory" >&2; exit 1; }
check "an unexpanded data directory is refused" "$OUT" "not an absolute path"
[ -z "$(find "$PROJECT" -mindepth 1 -maxdepth 1 ! -name .git ! -name docs)" ] || { echo "FAIL: a placeholder directory in the project" >&2; ls -A "$PROJECT" >&2; exit 1; }

publish "$VERSION"

set +e
OUT="$(plugin_env "$PLUGIN/scripts/launch.sh" serve 2>&1 </dev/null)"
STATUS=$?
set -e
[ "$STATUS" -ne 0 ] || { echo "FAIL: started with no pinned checksum" >&2; exit 1; }
check "a version without a pinned checksum is refused" "$OUT" "no checksum for"

pin "$VERSION"
set +e
OUT="$(RELEASE_BASE=http://127.0.0.1:1 plugin_env "$PLUGIN/scripts/launch.sh" serve 2>&1 </dev/null)"
STATUS=$?
set -e
[ "$STATUS" -ne 0 ] || { echo "FAIL: started offline" >&2; exit 1; }
check "no network on the first run gives a clear error" "$OUT" "Check the network"

GOOD_SUMS="$(cat "$PLUGIN/checksums.txt")"
printf '%064d  agent-memory-mcp-%s-%s-%s.tar.gz\n' 0 "$VERSION" "$OS" "$ARCH" >"$PLUGIN/checksums.txt"
set +e
OUT="$(plugin_env "$PLUGIN/scripts/launch.sh" serve 2>&1 </dev/null)"
STATUS=$?
set -e
[ "$STATUS" -ne 0 ] || { echo "FAIL: started with a wrong checksum" >&2; exit 1; }
check "a wrong checksum is refused" "$OUT" "checksum mismatch"
[ ! -e "$BINARY" ] || { echo "FAIL: a binary was installed despite the mismatch" >&2; exit 1; }
echo "$GOOD_SUMS" >"$PLUGIN/checksums.txt"

OUT="$(session '[["store_memory", {"content": "the smokeword fact", "type": "semantic"}], ["index_documents", {}], ["semantic_search", {"query": "frobnicator", "limit": 3}], ["repo_read", {"path": "docs/runbook.md"}]]')"
check "core profile" "$OUT" "tools: 17"
check "memory is stored with no model" "$OUT" "Memory stored"
check "documents are indexed with no model" "$OUT" "Indexed 1 files"
check "keyword search finds the document" "$OUT" "docs/runbook.md"
check "a tool outside the profile is refused" "$OUT" "not in the core profile"
[ -x "$BINARY" ] || { echo "FAIL: binary not installed at $BINARY" >&2; exit 1; }
[ "$(ls -A "$DATA/bin")" = "$VERSION" ] || { echo "FAIL: leftovers in $DATA/bin" >&2; ls -A "$DATA/bin" >&2; exit 1; }
[ "$(git -C "$PROJECT" status --short | grep -cv docs/)" -eq 0 ] || { echo "FAIL: the server wrote into the project" >&2; git -C "$PROJECT" status --short >&2; exit 1; }
echo "ok: nothing but the user's own files in the project"

OUT="$(session '[["recall_memory", {"query": "smokeword"}]]')"
check "memory survives into the next session" "$OUT" "the smokeword fact"

OUT="$(plugin_env "$PLUGIN/scripts/launch.sh" hook context-inject 2>&1)"
check "the next session starts with the project's memory" "$OUT" "the smokeword fact"

mkdir -p "$WORK/fresh"
git -C "$WORK/fresh" init -q
OUT="$(plugin_env CLAUDE_PROJECT_DIR="$WORK/fresh" "$PLUGIN/scripts/launch.sh" hook context-inject 2>&1)"
[ -z "$OUT" ] || { echo "FAIL: the first session of a project is not silent: $OUT" >&2; exit 1; }
echo "ok: the first session of a project starts silently"

publish 99.0.0
pin 99.0.0
OUT="$(session '[["recall_memory", {"query": "smokeword"}]]')"
check "memory survives a plugin update" "$OUT" "the smokeword fact"
[ ! -e "$BINARY" ] || { echo "FAIL: the old binary was left behind" >&2; exit 1; }
echo "ok: the update replaced the binary"

session_end first >/dev/null 2>&1
[ "$(raw_summaries)" -ge 1 ] || { echo "FAIL: the hook captured nothing" >&2; exit 1; }
echo "ok: a session end is captured"

BEFORE="$(raw_summaries)"
mkdir -p "$HOME_DIR/.claude"
cat >"$HOME_DIR/.claude/settings.json" <<'JSON'
{"hooks": {"SessionEnd": [{"hooks": [{"type": "command", "command": "/opt/homebrew/bin/agent-memory-mcp auto-capture --hook-event"}]}]}}
JSON
session_end second >/dev/null 2>&1
[ "$(raw_summaries)" -eq "$BEFORE" ] || { echo "FAIL: plugin hooks ran next to the hooks of setup" >&2; exit 1; }
echo "ok: plugin hooks yield to the hooks of setup"

# The same files through the real installer, where Claude Code is around. CI has its
# own job for this against the published plugin.
if command -v claude >/dev/null 2>&1; then
	mkdir -p "$WORK/marketplace"
	cp -R "$ROOT/.claude-plugin" "$PLUGIN" "$WORK/marketplace/"
	AMM_PLUGIN_RELEASE_BASE="http://127.0.0.1:$PORT" "$ROOT/scripts/plugin-install-check.sh" "$WORK/marketplace"
else
	echo "skipped: no claude on PATH, the installer was not run"
fi

echo "plugin smoke passed"
