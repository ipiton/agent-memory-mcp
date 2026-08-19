#!/usr/bin/env bash
# T117: allow the one deprecation we decided to keep, fail on any other.
#
# `brews` is deprecated in goreleaser 2.x, and we keep it deliberately: the
# Homebrew *cask* `service` stanza only relocates a service file that ships
# inside the archive, whereas the formula's `service do` generates the launchd
# plist from `run [...]` — and the cask has no `install` block, which is where
# the T101 secrets wrapper is written. Dropping `brews` would drop both.
#
# So the gate is on the set of deprecations, not on their absence.
set -euo pipefail

known_deprecations=(
  "brews"
)

output="$(goreleaser check 2>&1 || true)"
echo "$output"

# goreleaser colours its output whenever it thinks a terminal is watching, and
# on GitHub Actions it does. The escape codes sit between "DEPRECATED:" and the
# key, so the pattern below matched an empty string and the gate reported
# "none" on every CI run while `brews` was right there in the log -- a gate
# that cannot see the deprecation it knows about would not have seen a new one
# either. NO_COLOR does not override it, so strip the SGR sequences instead.
# $(printf) rather than \x1b: BSD sed on macOS does not understand the escape.
esc="$(printf '\033')"
plain="$(printf '%s\n' "$output" | sed "s/${esc}\[[0-9;]*m//g")"

# No mapfile: macOS still ships bash 3.2 and this must run locally too.
found=""
while IFS= read -r item; do
  [ -n "$item" ] && found="$found $item"
done < <(
  printf '%s\n' "$plain" |
    sed -n 's/.*DEPRECATED: *\([a-z_.]*\) .*/\1/p' |
    sort -u
)

status=0
for item in $found; do
  if [[ ! " ${known_deprecations[*]} " == *" $item "* ]]; then
    echo "::error::new goreleaser deprecation: '$item' — decide what replaces it and record the decision in TASKS.md T117, then add it here"
    status=1
  fi
done

if [[ $status -eq 0 ]]; then
  echo "goreleaser deprecations:${found:- none} (all known)"
fi
exit $status
