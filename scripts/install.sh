#!/bin/sh
set -eu

repo="PolyphonyRequiem/twig-herdr"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
plugin_root=${HERDR_PLUGIN_ROOT:-$script_dir/..}
plugin_root=$(CDPATH= cd -- "$plugin_root" && pwd)
manifest="$plugin_root/herdr-plugin.toml"

version=$(awk -F'"' '/^version = "/ { print $2; exit }' "$manifest")
[ -n "$version" ] || { echo "twig-herdr: could not read version from $manifest" >&2; exit 1; }

os=$(uname -s 2>/dev/null || printf '%s' unknown)
arch=$(uname -m 2>/dev/null || printf '%s' unknown)

case "$os" in
  Linux) os=linux ;;
  Darwin) os=macos ;;
  *) echo "twig-herdr: unsupported platform $os/$arch" >&2; exit 1 ;;
esac

case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "twig-herdr: unsupported architecture $arch on $os" >&2; exit 1 ;;
esac

asset="twig-herdr-${os}-${arch}"
base_url="https://github.com/$repo/releases/download/v$version"
bin_dir="$plugin_root/bin"
dest="$bin_dir/twig-herdr"

mkdir -p "$bin_dir"
tmpdir=$(mktemp -d "$bin_dir/.twig-herdr.XXXXXX") || { echo "twig-herdr: could not create a temp dir" >&2; exit 1; }
trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM

download() {
  url=$1
  out=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$out" "$url"
  else
    return 1
  fi
}

sha256_of() {
  file=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1; exit}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1; exit}'
  else
    return 1
  fi
}

tmp_bin="$tmpdir/$asset"
tmp_sums="$tmpdir/SHA256SUMS"

download "$base_url/$asset" "$tmp_bin" || { echo "twig-herdr: prebuilt binary not available for v$version ($asset)" >&2; exit 1; }
download "$base_url/SHA256SUMS" "$tmp_sums" || { echo "twig-herdr: checksums not available for v$version" >&2; exit 1; }

expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1; exit }' "$tmp_sums")
[ -n "$expected" ] || { echo "twig-herdr: no checksum listed for $asset" >&2; exit 1; }

actual=$(sha256_of "$tmp_bin") || { echo "twig-herdr: no SHA-256 tool (sha256sum/shasum) available" >&2; exit 1; }
[ "$actual" = "$expected" ] || { echo "twig-herdr: checksum mismatch for $asset (expected $expected, got $actual)" >&2; exit 1; }

chmod 755 "$tmp_bin"
mv -f "$tmp_bin" "$dest"
echo "twig-herdr: installed v$version for $os/$arch and verified SHA-256."
