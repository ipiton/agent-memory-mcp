# Secrets: SOPS instead of a plaintext `config.env`

`config.env` is world-readable by design (`0644`, brew's `etc`), and it is
where the service reads everything — including, historically, API keys. This
document describes the arrangement that keeps keys out of it, and the one
property that makes the arrangement cheap.

**Nothing here is mandatory.** With no `secrets.sops.env` present the service
starts exactly as it did before. The wrapper checks for the file and gets out
of the way when it is absent.

## Why it costs almost nothing

`internal/config/dotenv.go` gives the environment priority over the file:

```go
if current := strings.TrimSpace(os.Getenv(key)); current != "" {
    continue
}
```

A variable already present in the environment is **not** overwritten by
`config.env`. So `config.env` keeps every non-secret setting and stays where it
is; secrets arrive from the environment and win. Neither `--config` nor the
config format changes.

## How the service starts

The formula installs `libexec/service-wrapper` and the launchd job runs that
instead of the binary:

```bash
if [ -f "$SECRETS" ] && command -v sops >/dev/null 2>&1; then
  : "${SOPS_AGE_KEY_FILE:=$HOME/.config/sops/age/keys.txt}"
  export SOPS_AGE_KEY_FILE
  exec sops exec-env --same-process "$SECRETS" "$BIN serve --config $CONFIG"
fi
exec "$BIN" serve --config "$CONFIG"
```

Two details are not cosmetic, both established by measurement rather than
assumption:

- **`SOPS_AGE_KEY_FILE` is set explicitly.** Without it `sops` reports *"no
  identity matched any of the recipients"* and lists only environment
  variables as the places it looked. launchd's minimal environment makes this
  certain, but it reproduces in an ordinary shell too.
- **`--same-process`.** Plain `sops exec-env` forks the command, so launchd
  would supervise `sops` and `brew services stop` would be signalling the
  wrong process. With `--same-process` sops execs the service in place: the
  service keeps the pid launchd is watching (verified — the child's ppid is
  the caller, not sops).

If decryption fails the service does not start, `keep_alive` retries it, and
`service.err.log` carries the sops error naming the missing key. A loud
failure is the intent: silently starting without secrets is the degraded path
this repo spends effort eliminating elsewhere.

## Setup (one machine, one time)

The recipient below is an example — use your own age public key.

```sh
# 1. An age key, if there isn't one yet. Keep the private half out of git.
age-keygen -o ~/.config/sops/age/keys.txt
chmod 600 ~/.config/sops/age/keys.txt
age-keygen -y ~/.config/sops/age/keys.txt      # prints the public recipient

# 2. Rules. The file name MUST match a creation rule or `sops --encrypt`
#    fails with "no matching creation rules". Placing this next to the
#    secrets file lets sops find it without --config.
cat > /opt/homebrew/etc/agent-memory-mcp/.sops.yaml <<'YAML'
creation_rules:
  - path_regex: .*\.sops\.env$
    age: age1yourrecipienthere
YAML

# 3. The secrets themselves, in dotenv form.
cat > /tmp/secrets.env <<'ENV'
JINA_API_KEY=
OPENAI_API_KEY=
MCP_TRIPLE_EXTRACTOR_API_KEY=
MCP_HTTP_AUTH_TOKEN=
ENV

# 4. Encrypt into place, then destroy the plaintext.
sops --encrypt --input-type dotenv --output-type dotenv \
  /tmp/secrets.env > /opt/homebrew/etc/agent-memory-mcp/secrets.sops.env
chmod 600 /opt/homebrew/etc/agent-memory-mcp/secrets.sops.env
rm -f /tmp/secrets.env

# 5. Remove the same keys from config.env — the point is that they stop
#    living there. The environment wins either way, so a leftover value is
#    inert, but it is still a plaintext key on disk.

# 6. Restart and check.
brew services restart agent-memory-mcp
tail -n 40 /opt/homebrew/var/log/agent-memory-mcp/service.err.log
```

Editing later: `sops /opt/homebrew/etc/agent-memory-mcp/secrets.sops.env` —
it decrypts into `$EDITOR` and re-encrypts on save.

## What to verify after an upgrade

`brew upgrade` regenerates the launchd plist from the formula, so a plist
edited by hand loses the wrapper silently. That is why the wrapper lives in
the formula (generated from `.goreleaser.yaml`) and not in
`~/Library/LaunchAgents`. After an upgrade, confirm the service still comes up
with secrets:

```sh
brew services info agent-memory-mcp
ps -o command= -p "$(pgrep -f 'agent-memory-mcp serve')"   # the service, not sops
```

## What this does not give you

The secrets end up in the service process's environment, so `ps eww` shows
them to the same user. This is strictly better than a `0644` file, and it is
not isolation. If isolation is the requirement, the conversation is about
Keychain, not SOPS.
