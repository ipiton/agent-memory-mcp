#!/bin/bash
#
# T167: installs the plugin the way a user does, with the real Claude Code, in a
# throwaway HOME: a machine that has never seen it, on any machine. No login needed:
# nothing here talks to a model. `claude mcp list` starts the server, so a pass means
# the marketplace, the download, the checksum and the MCP handshake all worked.
#
# Usage: scripts/plugin-install-check.sh [marketplace]
#        marketplace: owner/repo or a local directory, default the published one.
# AMM_PLUGIN_RELEASE_BASE, when set, replaces GitHub Releases.

set -euo pipefail

MARKETPLACE="${1:-ipiton/agent-memory-mcp}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/plugin-install.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/home" "$WORK/project"
git -C "$WORK/project" init -q
cd "$WORK/project"

fresh() {
	env -i PATH="$PATH" HOME="$WORK/home" \
		${AMM_PLUGIN_RELEASE_BASE:+AMM_PLUGIN_RELEASE_BASE="$AMM_PLUGIN_RELEASE_BASE"} "$@"
}

fresh claude plugin marketplace add "$MARKETPLACE" </dev/null
fresh claude plugin install agent-memory@agent-memory-mcp </dev/null

OUT="$(fresh claude mcp list </dev/null 2>&1)"
echo "$OUT"
if ! grep -q 'plugin:agent-memory:agent-memory.*Connected' <<<"$OUT"; then
	echo "FAIL: the plugin's server did not connect" >&2
	exit 1
fi
if [ -n "$(find "$WORK/project" -mindepth 1 -maxdepth 1 ! -name .git)" ]; then
	echo "FAIL: the plugin wrote into the project" >&2
	exit 1
fi
echo "plugin install check passed: $MARKETPLACE"
