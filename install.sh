#!/bin/sh
set -eu

die() {
  printf 'ig-mcp: %s\n' "$*" >&2
  exit 1
}

case "${1:-}" in
  '') [ "$#" -eq 0 ] || die 'Usage: sh install.sh [--uninstall]' ;;
  --uninstall) [ "$#" -eq 1 ] || die 'Usage: sh install.sh [--uninstall]' ;;
  *) die 'Usage: sh install.sh [--uninstall]' ;;
esac

if [ -n "${IG_MCP_BIN_DIR:-}" ]; then
  bin_dir=$IG_MCP_BIN_DIR
else
  [ -n "${HOME:-}" ] || die 'HOME is not set'
  bin_dir=$HOME/.local/bin
fi
case $bin_dir in
  /*) ;;
  *) bin_dir=$PWD/$bin_dir ;;
esac
target=$bin_dir/ig-mcp

[ ! -L "$target" ] || die "Refusing to modify symlink: $target"

if [ "${1:-}" = --uninstall ]; then
  if [ -f "$target" ]; then
    rm -f "$target"
    printf 'Removed %s (session data preserved)\n' "$target"
  else
    printf 'ig-mcp is not installed at %s\n' "$target"
  fi
  exit 0
fi

[ ! -e "$target" ] || [ -f "$target" ] || die "Not a regular file: $target"
command -v curl >/dev/null 2>&1 || die 'curl is required'
command -v tar >/dev/null 2>&1 || die 'tar is required'

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die 'Only Linux and macOS are supported' ;;
esac
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die 'Unsupported CPU architecture' ;;
esac

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/ig-mcp.XXXXXX")
stage=
cleanup() {
  [ -z "$stage" ] || rm -f "$stage"
  rm -rf "$tmp_dir"
}
trap 'cleanup' 0
trap 'exit 1' 1 2 3 15

tag=${IG_MCP_VERSION:-}
if [ -z "$tag" ]; then
  curl -fsSL 'https://api.github.com/repos/mewisme/ig-mcp/releases/latest' -o "$tmp_dir/release.json" || die 'Failed to fetch latest release'
  tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp_dir/release.json" | head -n 1)
fi
case $tag in
  v*) ;;
  *) tag=v$tag ;;
esac
version=${tag#v}
case $version in
  ''|*[!A-Za-z0-9._+-]*) die 'Invalid release version' ;;
esac
case $tag in
  *[!A-Za-z0-9._+-]*) die 'Invalid release tag' ;;
esac

asset=ig-mcp_${version}_${os}_${arch}.tar.gz
base=https://github.com/mewisme/ig-mcp/releases/download/$tag
archive=$tmp_dir/$asset
printf 'Installing ig-mcp %s (%s/%s)...\n' "$tag" "$os" "$arch"
curl -fsSL "$base/$asset" -o "$archive" || die 'Failed to download release archive'
curl -fsSL "$base/checksums.txt" -o "$tmp_dir/checksums.txt" || die 'Failed to download checksums'

expected=$(awk -v file="$asset" '$2 == file || $2 == "*" file { print $1; exit }' "$tmp_dir/checksums.txt")
[ -n "$expected" ] || die "Checksum not found for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
else
  die 'sha256sum or shasum is required'
fi
[ "$actual" = "$expected" ] || die 'SHA256 checksum mismatch'

mkdir -p "$bin_dir"
stage=$(mktemp "$bin_dir/.ig-mcp.XXXXXX")
tar -xOzf "$archive" ig-mcp > "$stage" || die 'Failed to extract ig-mcp from archive'
chmod +x "$stage"
"$stage" --help >/dev/null 2>&1 || die 'Downloaded ig-mcp failed the --help check'
mv -f "$stage" "$target"
stage=
printf 'Installed %s\n' "$target"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) printf '%s\n' "Add to PATH if needed: export PATH=\"$bin_dir:\$PATH\"" ;;
esac