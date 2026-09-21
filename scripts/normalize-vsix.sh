#!/usr/bin/env bash
set -euo pipefail

vsix="${1:?usage: normalize-vsix.sh path/to/extension.vsix}"
work="$(mktemp -d)"
normalized="$(mktemp)"
trap 'rm -rf "$work" "$normalized"' EXIT
rm "$normalized"

unzip -q "$vsix" -d "$work"
find "$work" -exec touch -h -t 198001010000 {} +
(
  cd "$work"
  find . -type f -print | LC_ALL=C sort | zip -X -q "$normalized" -@
)
mv "$normalized" "$vsix"
