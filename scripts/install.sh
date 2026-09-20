#!/bin/sh
set -eu

repo="DonaldMurillo/system-one-playground"
install_dir="${SYSONESCRIPT_INSTALL_DIR:-$HOME/.local/bin}"
version="${SYSONESCRIPT_VERSION:-}"

command -v curl >/dev/null 2>&1 || { echo "error: curl is required" >&2; exit 1; }
command -v tar >/dev/null 2>&1 || { echo "error: tar is required" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) platform="darwin" ;;
  Linux) platform="linux" ;;
  *) echo "error: unsupported operating system $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64) arch="x64" ;;
  *) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if [ -z "$version" ]; then
  tag="$(curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=30" | sed -n 's/.*"tag_name":[[:space:]]*"\(vscode-v[^"]*\)".*/\1/p' | head -n 1)"
  [ -n "$tag" ] || { echo "error: no SysOneScript CLI release found" >&2; exit 1; }
  version="${tag#vscode-v}"
else
  tag="vscode-v${version#v}"
  version="${version#v}"
fi

archive="sysonescript-cli-v${version}-${platform}-${arch}.tar.gz"
base_url="https://github.com/$repo/releases/download/$tag"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

echo "Downloading SysOneScript $version for $platform-$arch..."
curl -fsSL "$base_url/$archive" -o "$tmp_dir/$archive"
curl -fsSL "$base_url/checksums.txt" -o "$tmp_dir/checksums.txt"

expected="$(awk -v file="$archive" '$2 == file { print $1 }' "$tmp_dir/checksums.txt")"
[ -n "$expected" ] || { echo "error: release checksum is missing for $archive" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp_dir/$archive" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$tmp_dir/$archive" | awk '{print $1}')"
fi
[ "$actual" = "$expected" ] || { echo "error: checksum verification failed" >&2; exit 1; }

tar -xzf "$tmp_dir/$archive" -C "$tmp_dir"
mkdir -p "$install_dir"
install -m 755 "$tmp_dir/sos" "$install_dir/.sos.new.$$"
install -m 755 "$tmp_dir/sysone" "$install_dir/.sysone.new.$$"
mv -f "$install_dir/.sos.new.$$" "$install_dir/sos"
mv -f "$install_dir/.sysone.new.$$" "$install_dir/sysone"

echo "Installed sos and sysone to $install_dir"
case ":${PATH:-}:" in
  *:"$install_dir":*) ;;
  *) echo "Add $install_dir to PATH, then open a new terminal." ;;
esac
echo "Run: sysone version"
