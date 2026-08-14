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

# No mapfile: macOS still ships bash 3.2 and this must run locally too.
found=""
while IFS= read -r item; do
  [ -n "$item" ] && found="$found $item"
done < <(
  printf '%s\n' "$output" |
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
