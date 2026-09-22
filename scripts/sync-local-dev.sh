#!/bin/sh
set -eu

repo_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
install_dir="${SYSONESCRIPT_INSTALL_DIR:-$HOME/.local/bin}"
code_command="${SYSONESCRIPT_CODE_COMMAND:-code}"
version="$(node -p "require('$repo_root/vscode/package.json').version")"
stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/sysonescript-local-sync.XXXXXX")"
vsix="$stage_dir/sysonescript-vscode-$version.vsix"
ldflags="-X github.com/DonaldMurillo/system-one-playground/sos.Version=$version -X github.com/DonaldMurillo/system-one-playground/sos.ReleaseMarker=SysOneScriptVersion=$version;SysOneScriptVersionEnd"

cleanup() {
  rm -rf "$stage_dir"
}
trap cleanup EXIT HUP INT TERM

command -v go >/dev/null 2>&1 || { echo "error: go is required" >&2; exit 1; }
command -v node >/dev/null 2>&1 || { echo "error: node is required" >&2; exit 1; }
command -v corepack >/dev/null 2>&1 || { echo "error: corepack is required" >&2; exit 1; }
command -v "$code_command" >/dev/null 2>&1 || { echo "error: $code_command is required" >&2; exit 1; }

echo "Building SysOneScript $version from $repo_root..."
(cd "$repo_root" && go generate ./sos ./internal/sosbuild)
(cd "$repo_root" && go build -trimpath -ldflags "$ldflags" -o "$stage_dir/sos" ./cmd/sos)
(cd "$repo_root" && go build -trimpath -ldflags "$ldflags" -o "$stage_dir/sysone" ./cmd/sysone)

mkdir -p "$repo_root/vscode/bin"
install -m 0755 "$stage_dir/sos" "$repo_root/vscode/bin/sos"
(cd "$repo_root/vscode" && corepack pnpm@9.15.9 exec vsce package --no-dependencies --out "$vsix")
(cd "$repo_root" && corepack pnpm@9.15.9 --dir vscode run package:smoke -- "$vsix")

mkdir -p "$install_dir"
install -m 0755 "$stage_dir/sos" "$install_dir/.sos.new.$$"
mv "$install_dir/.sos.new.$$" "$install_dir/sos"
install -m 0755 "$stage_dir/sysone" "$install_dir/.sysone.new.$$"
mv "$install_dir/.sysone.new.$$" "$install_dir/sysone"
"$code_command" --install-extension "$vsix" --force

echo "Installed local SysOneScript $version:"
"$install_dir/sos" version
"$install_dir/sysone" version
"$code_command" --list-extensions --show-versions | grep '^donaldmurillo\.sysonescript-vscode@'
echo "Reload VS Code windows that were already open to activate the replacement extension host."
