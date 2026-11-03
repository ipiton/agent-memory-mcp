#!/bin/bash
#
# T167: points the Claude Code plugin at a published release. Run after the release
# is out, then commit: the plugin refuses any archive not listed in
# plugin/checksums.txt, so a release asset replaced later does not reach users.
#
# Usage: scripts/plugin-pin.sh 0.15.0

set -euo pipefail

VERSION="${1:?usage: plugin-pin.sh <version>}"
VERSION="${VERSION#v}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
URL="https://github.com/ipiton/agent-memory-mcp/releases/download/v$VERSION/agent-memory-mcp_${VERSION}_checksums.txt"

SUMS="$(curl -fsSL "$URL")"
ARCHIVES="$(grep -E "agent-memory-mcp-$VERSION-(darwin|linux)-(amd64|arm64)\.tar\.gz$" <<<"$SUMS")"
if [ "$(wc -l <<<"$ARCHIVES")" -ne 4 ]; then
	echo "expected 4 archives in $URL, got:" >&2
	echo "$ARCHIVES" >&2
	exit 1
fi

{
	echo "# Pinned by scripts/plugin-pin.sh from the checksums of release v$VERSION."
	echo "$ARCHIVES"
} >"$ROOT/plugin/checksums.txt"

sed -i.bak -E "s/(\"version\": \")[^\"]*/\1$VERSION/" "$ROOT/plugin/.claude-plugin/plugin.json"
rm -f "$ROOT/plugin/.claude-plugin/plugin.json.bak"

echo "Plugin pinned to v$VERSION. Commit plugin/checksums.txt and plugin/.claude-plugin/plugin.json."
