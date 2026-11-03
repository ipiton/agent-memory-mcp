# Claude Code plugin

The plugin gives Claude Code persistent memory and document search with nothing else to install: no Homebrew, no embedding model, no background service.

```text
/plugin marketplace add ipiton/agent-memory-mcp
/plugin install agent-memory@agent-memory-mcp
```

On Claude Code 2.1.275 or later, one step does both:

```text
/plugin install agent-memory --marketplace ipiton/agent-memory-mcp
```

Restart Claude Code afterwards. macOS and Linux, amd64 and arm64. Windows is not supported yet.

## What happens on first start

A plugin cannot ship a binary per platform, so the first server start downloads the release archive for this plugin version from GitHub Releases, once. It needs `curl` or `wget`, and the archive is checked against `plugin/checksums.txt`, which comes with the plugin: a release asset replaced later does not install. Without a network the server reports why and starts on the next attempt. Hooks never download anything and never fail a session.

## What it sets up

- **Server `agent-memory`** over stdio, with the `core` profile of 17 tools ([MCP_TOOL_PROFILE](CONFIGURATION.md)).
- **Hooks**: `SessionStart` injects the project's memory, `SessionEnd` captures the session, `PreCompact` saves a checkpoint ([Hooks](HOOKS.md)).
- **One database per project**, in `~/.local/share/agent-memory-mcp/projects/<name>-<hash>` ([MCP_DATA_SCOPE](CONFIGURATION.md)). Worktrees of a repository share it, and nothing is written into the repository.
- **Search by keywords**, with no model ([MCP_RAG_KEYWORD_FALLBACK](CONFIGURATION.md)). Results are marked as a degraded retrieval path because they are not semantic.

## Settings

The plugin's settings live in `config.env` in its data directory, `~/.claude/plugins/data/agent-memory-<marketplace>/`. A key set there wins over the plugin's defaults; [Configuration](CONFIGURATION.md) lists them all.

To add vectors, point at a local embedding server. On the next server start the index is rebuilt with them:

```bash
LLAMACPP_BASE_URL=http://127.0.0.1:8090/v1
```

`MCP_TOOL_PROFILE=full` offers every tool.

## With a Homebrew install

The two can sit on one machine and do not mix. The plugin always passes its own `config.env`, so it never reads the Homebrew config or its database.

- **Hooks.** If `agent-memory-mcp setup` wrote its hooks into `~/.claude/settings.json` or the project's `.claude/settings.json`, the plugin's hooks do nothing, and those hooks keep writing to the Homebrew bank. Otherwise each session would be captured twice.
- **Servers.** A server registered with `setup claude-code` and the plugin's server both stay available, each with its own bank.
- **Moving to the plugin.** The bank is not migrated. To switch, remove the hooks ([Uninstall](HOOKS.md#uninstall--rollback)) and the server entry, and restart Claude Code.

## Moving, updating, removing

- **Moving a repository** changes the database key, so the project starts empty. Move its directory under `projects/` by hand, or set `MCP_DATA_PATH`.
- **Updating the plugin** replaces the binary and keeps the data.
- **Removing the plugin** deletes its data directory: the binary and `config.env`. Project databases stay in `~/.local/share/agent-memory-mcp/`; delete them by hand if you do not want them.

## Publishing a version

The plugin names one release, and that release has to exist first:

1. Publish the release as usual, from a tag. Until step 2 the plugin in the repository still names the previous version. Before the first release it names one with no checksums, and installing it stops with that message.
2. `make plugin-pin VERSION=x.y.z` writes the release's checksums to `plugin/checksums.txt` and the version to `plugin.json`.
3. Commit and push. That commit is what users receive.

`make plugin-smoke` runs the launcher as a new machine would, against a local stand-in for GitHub Releases. CI runs it on macOS and Linux. To try a local build in real Claude Code:

```bash
AMM_PLUGIN_BINARY="$PWD/agent-memory-mcp" claude --plugin-dir ./plugin
```
