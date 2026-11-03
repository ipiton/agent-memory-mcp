#!/bin/sh
#
# Runs agent-memory-mcp for the Claude Code plugin. Claude Code has no mechanism
# for platform binaries, so the first server start downloads the release archive
# into CLAUDE_PLUGIN_DATA and checks it against plugin/checksums.txt, which ships
# with the plugin: a replaced release asset does not pass.
#
# Usage: launch.sh serve            start the MCP server, downloading if needed
#        launch.sh hook <args...>   run a hook subcommand; never downloads and
#                                   never fails, a hook must not disturb a session

set -eu

umask 077

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
plugin_root=$(dirname -- "$here")
version=$(sed -n 's/.*"version"[^"]*"\([^"]*\)".*/\1/p' "$plugin_root/.claude-plugin/plugin.json" | head -n 1)
# plugin/.mcp.json passes these under its own names: an env entry that refers to
# itself, CLAUDE_PLUGIN_DATA=${CLAUDE_PLUGIN_DATA}, reached the server unexpanded.
data_dir=${AMM_PLUGIN_DATA:-${CLAUDE_PLUGIN_DATA:-}}
project_dir=${AMM_PROJECT_DIR:-${CLAUDE_PROJECT_DIR:-}}
release_base=${AMM_PLUGIN_RELEASE_BASE:-https://github.com/ipiton/agent-memory-mcp/releases/download}
config=$data_dir/config.env
binary=$data_dir/bin/$version/agent-memory-mcp

fail() {
	echo "agent-memory-mcp plugin: $1" >&2
	exit 1
}

# Anything but an absolute path is a placeholder nobody expanded, and writing to it
# would put a directory of that name into the user's project.
case $data_dir in
/*) ;;
*)
	[ "${1:-}" != hook ] || exit 0
	fail "the plugin data directory is not an absolute path: '$data_dir'"
	;;
esac

platform() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) fail "$(uname -s) is not supported, releases exist for macOS and Linux" ;;
	esac
	case $(uname -m) in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*) fail "$(uname -m) is not supported, releases exist for amd64 and arm64" ;;
	esac
	echo "$os-$arch"
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		fail "neither sha256sum nor shasum found, cannot verify the download"
	fi
}

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --connect-timeout 10 --retry 2 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -T 10 -O "$2" "$1"
	else
		fail "neither curl nor wget found, cannot download the server"
	fi
}

install_binary() {
	archive=agent-memory-mcp-$version-$(platform).tar.gz
	expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$plugin_root/checksums.txt")
	[ -n "$expected" ] || fail "no checksum for $archive in this plugin version, it cannot be installed"

	mkdir -p "$data_dir/bin"
	staging=$(mktemp -d "$data_dir/bin/.staging.XXXXXX")
	trap 'rm -f "$staging/$archive" "$staging/agent-memory-mcp"; rmdir "$staging" 2>/dev/null || true' EXIT

	url=$release_base/v$version/$archive
	echo "agent-memory-mcp plugin: downloading $url" >&2
	fetch "$url" "$staging/$archive" ||
		fail "could not download $url. Check the network and restart Claude Code; memory is unavailable until then."

	actual=$(sha256_of "$staging/$archive")
	[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive: expected $expected, got $actual. Not installing it."

	tar -xzf "$staging/$archive" -C "$staging" agent-memory-mcp || fail "could not unpack $archive"
	chmod 755 "$staging/agent-memory-mcp"
	mkdir -p "$data_dir/bin/$version"
	# Rename, so a concurrent start sees either nothing or a whole binary.
	mv -f "$staging/agent-memory-mcp" "$binary"
	rm -f "$staging/$archive"
	rmdir "$staging"
	trap - EXIT

	# Binaries of other versions are all this directory holds. The data is elsewhere.
	for old in "$data_dir"/bin/*/agent-memory-mcp; do
		[ "$old" = "$binary" ] && continue
		rm -f "$old"
		rmdir "$(dirname -- "$old")" 2>/dev/null || true
	done
}

# Defaults for the plugin, unless the user's config.env sets the key. An explicit
# --config below is what keeps the server off a global config of a brew install:
# that file names its own database, and the two banks must not mix.
default_env() {
	grep -q "^$1=" "$config" 2>/dev/null || export "$1=$2"
}

mkdir -p "$data_dir"
[ -f "$config" ] || cat >"$config" <<'CONFIG'
# Settings for the agent-memory-mcp Claude Code plugin; docs/CONFIGURATION.md lists them.
# Without a model, search works by keywords. To add vectors, point at a local server:
# LLAMACPP_BASE_URL=http://127.0.0.1:8090/v1
CONFIG

default_env MCP_HTTP_MODE stdio
default_env MCP_DATA_SCOPE project
default_env MCP_TOOL_PROFILE core
default_env MCP_EMBEDDING_MODE local-only
default_env MCP_RAG_KEYWORD_FALLBACK true
case $project_dir in
/*) default_env MCP_ROOT "$project_dir" ;;
esac

if [ -n "${AMM_PLUGIN_BINARY:-}" ]; then
	binary=$AMM_PLUGIN_BINARY
fi

mode=${1:-}
[ $# -gt 0 ] && shift

case $mode in
serve)
	[ -x "$binary" ] || install_binary
	exec "$binary" serve --config "$config" "$@"
	;;
hook)
	# Hooks written by `agent-memory-mcp setup` already do this work with the
	# bank of that install. Running both would capture each session twice (T168).
	for settings in "${HOME:-/nonexistent}/.claude/settings.json" "${project_dir:-/nonexistent}/.claude/settings.json"; do
		if grep -q 'agent-memory-mcp[^"]* auto-capture' "$settings" 2>/dev/null; then
			exit 0
		fi
	done
	[ -x "$binary" ] || exit 0
	"$binary" "$@" --config "$config" || echo "agent-memory-mcp plugin: hook $1 failed" >&2
	exit 0
	;;
*)
	fail "unknown mode '$mode', expected serve or hook"
	;;
esac
