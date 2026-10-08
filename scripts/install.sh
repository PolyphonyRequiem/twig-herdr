#!/bin/sh
set -eu

# Plugin builds use the checked-out manifest. Portable installs select a product explicitly.
product=twig-herdr
version=
install_dir=
fail() { printf '%s: %s\n' "$product" "$*" >&2; exit 1; }
usage() {
  printf '%s\n' 'Usage: install.sh [--product twig-herdr|twig-bench-tui] [--version latest|v0.3.0] [--install-directory PATH]' \
    'Without options, installs the checked-out Herdr plugin into its bin directory.' \
    'For the standalone release: install.sh --product twig-bench-tui --version latest' \
    'Portable installs default to ~/.local/bin; no compilers, PATH changes, shims, or migration.'
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --product|--version|--install-directory)
      [ "$#" -ge 2 ] && [ -n "$2" ] || fail "missing value for $1"
      case "$1" in
        --product) product=$2 ;;
        --version) version=$2 ;;
        --install-directory) install_dir=$2 ;;
      esac
      shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done
case "$product" in twig-herdr|twig-bench-tui) ;; *) fail 'unsupported product' ;; esac
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ "$product" = twig-herdr ] && [ -z "$install_dir" ]; then
  plugin_root=${HERDR_PLUGIN_ROOT:-$script_dir/..}
  plugin_root=$(CDPATH= cd -- "$plugin_root" && pwd) || fail 'plugin root does not exist'
  manifest="$plugin_root/herdr-plugin.toml"
  [ -f "$manifest" ] || fail "manifest not found at $manifest; select --product twig-bench-tui for a standalone install"
  manifest_version=$(sed -n 's/^version = "\([^"]*\)".*/\1/p' "$manifest")
  [ -n "$manifest_version" ] || fail "could not read version from $manifest"
  [ -z "$version" ] || [ "${version#v}" = "$manifest_version" ] || fail 'plugin install version must match the checked-out manifest'
  version=$manifest_version
  install_dir="$plugin_root/bin"
else
  install_dir=${install_dir:-$HOME/.local/bin}
  version=${version:-latest}
fi
if [ "$version" != latest ]; then
  version=${version#v}
  printf '%s\n' "$version" | LC_ALL=C awk '
    /^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9][A-Za-z0-9.-]*)?$/ { valid++; next }
    { invalid=1 }
    END { exit !(valid == 1 && !invalid) }
  ' || fail 'version must be latest or a release version such as v0.3.0'
fi
case "$install_dir" in *'
'*) fail 'invalid install directory' ;; esac
os=$(uname -s)
arch=$(uname -m)
case "$os" in Linux) os=linux; sqlite=libe_sqlite3.so ;; Darwin) os=macos; sqlite=libe_sqlite3.dylib ;; *) fail "unsupported platform $os/$arch" ;; esac
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) fail "unsupported architecture $arch on $os" ;; esac
asset="$product-$os-$arch.tar.gz"
if [ "$version" = latest ]; then
  base_url='https://github.com/PolyphonyRequiem/twig-herdr/releases/latest/download'
else
  base_url="https://github.com/PolyphonyRequiem/twig-herdr/releases/download/v$version"
fi

download() {
  if command -v curl >/dev/null 2>&1; then
    curl --proto '=https' --proto-redir '=https' -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget --https-only -qO "$2" "$1"
  else
    fail 'curl or wget is required'
  fi
}
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    fail 'a SHA-256 tool (sha256sum/shasum) is required'
  fi
}
assert_help() {
  help_flags=$1
  shift
  help_output=$("$native" "$@" --help 2>&1) || fail "bundled companion cannot run: $* --help"
  for help_flag in $help_flags; do
    case "$help_output" in *"$help_flag"*) ;; *) fail "bundled companion lacks $help_flag on $*; installation unchanged" ;; esac
  done
}

mkdir -p -- "$install_dir"
install_dir=$(CDPATH= cd -- "$install_dir" && pwd)
tmpdir=$(mktemp -d "$install_dir/.twig-bench-install.XXXXXX") || fail 'could not create staging directory'
lock_dir="$install_dir/.twig-bench-install.lock"
locked=no
committed=no
old_browser=no
old_native=no
new_browser=no
new_native=no
keep_tmp=no
cleanup() {
  result=$?
  trap - 0 1 2 15
  if [ "$committed" != yes ]; then
    if [ "$new_browser" = yes ]; then rm -f -- "$install_dir/$product" || keep_tmp=yes; fi
    if [ "$new_native" = yes ]; then rm -rf -- "$install_dir/twig-bench-native" || keep_tmp=yes; fi
    if [ "$old_native" = yes ]; then
      { [ ! -e "$install_dir/twig-bench-native" ] && [ ! -L "$install_dir/twig-bench-native" ] && mv -- "$tmpdir/previous/twig-bench-native" "$install_dir/twig-bench-native"; } || keep_tmp=yes
    fi
    if [ "$old_browser" = yes ]; then
      { [ ! -e "$install_dir/$product" ] && [ ! -L "$install_dir/$product" ] && mv -- "$tmpdir/previous/$product" "$install_dir/$product"; } || keep_tmp=yes
    fi
  fi
  if [ "$locked" = yes ]; then rmdir -- "$lock_dir" || :; fi
  if [ "$keep_tmp" = yes ]; then
    printf '%s: rollback needs attention; previous files preserved in %s/previous\n' "$product" "$tmpdir" >&2
  else
    rm -rf -- "$tmpdir"
  fi
  exit "$result"
}
trap cleanup 0
trap 'exit 130' 1 2 15
archive="$tmpdir/$asset"
sums="$tmpdir/SHA256SUMS"
download "$base_url/$asset" "$archive" || fail "bundle unavailable: $asset ($version)"
download "$base_url/SHA256SUMS" "$sums" || fail "SHA256SUMS unavailable for $version"
expected=$(LC_ALL=C awk -v name="$asset" '
  $2 == name || $2 == "*" name {
    if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-fA-F]/) invalid=1;
    hash=tolower($1); count++
  }
  END { if (count != 1 || invalid) exit 1; print hash }
' "$sums") || fail "expected exactly one SHA256SUMS entry for $asset"
actual=$(sha256_of "$archive")
[ "$actual" = "$expected" ] || fail "checksum mismatch for $asset; installation unchanged"

# Validate every name and type before extraction. Links, special files, duplicate
# names and paths outside the two private executable locations are never admitted.
tar -tzf "$archive" > "$tmpdir/names"
LC_ALL=C awk -v browser="$product" '
  {
    name=$0; sub(/^\.\//, "", name);
    if (name == "." || name == "./" || name == "") { next }
    if (name ~ /^\// || name ~ /(^|\/)\.\.?($|\/)/ || name ~ /\/\// || name ~ /[^A-Za-z0-9._+\/-]/) exit 1;
    if (name != browser && name != "INSTALL.txt" && name != "twig-bench-native/" && index(name, "twig-bench-native/") != 1) exit 1;
    sub(/\/$/, "", name);
    if (seen[name]++) exit 1;
  }
' "$tmpdir/names" || fail 'unsafe or unexpected archive paths'
tar -tvzf "$archive" > "$tmpdir/types"
LC_ALL=C awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" { exit 1 }' "$tmpdir/types" || fail 'archive contains links or special files'
stage="$tmpdir/bundle"
mkdir -- "$stage"
tar -xzf "$archive" -C "$stage"
for required in "$product" INSTALL.txt twig-bench-native/twig-bench-native "twig-bench-native/$sqlite"; do
  [ -f "$stage/$required" ] && [ ! -L "$stage/$required" ] || fail "bundle missing regular file $required"
done
chmod 755 "$stage/$product" "$stage/twig-bench-native/twig-bench-native"
browser_help=$(cd -- "$stage" && "./$product" --help 2>&1) || fail 'bundled browser cannot run'
case "$browser_help" in *"$product"*) ;; *) fail 'unexpected browser help in bundle' ;; esac
native="$stage/twig-bench-native/twig-bench-native"
# Help-only probes run in the staging directory, never an admitted user workspace.
(cd -- "$stage" &&
  assert_help '--include-browser --expect-binding --expect-identity' workspace &&
  assert_help '--expect-bench --expect-settings --expect-binding --expect-identity' workspace track &&
  assert_help '--expect-bench --expect-settings --expect-binding --expect-identity' workspace track-tree &&
  assert_help '--mode --expect-bench --expect-settings --expect-binding --expect-identity' workspace untrack &&
  assert_help '--expect-bench --expect-binding --expect-identity' workspace sync &&
  assert_help '--expect-bench --expect-binding --expect-identity' bench configuration &&
  assert_help '--width --expect-bench --expect-binding --expect-identity' bench detail &&
  assert_help '--expect-bench --expect-binding --expect-identity' bench configuration area candidates &&
  assert_help '--expect-bench --expect-settings --expect-binding --expect-identity' bench configuration area add &&
  assert_help '--expect-bench --expect-settings --expect-binding --expect-identity' bench configuration sprint remove &&
  assert_help '--include-management --expect-binding --expect-identity' bench list &&
  assert_help '--expect-binding --expect-identity' bench create &&
  assert_help '--expect-bench --expect-binding --expect-identity' bench switch &&
  assert_help '--expect-bench --expect-contents --confirm --expect-binding --expect-identity' bench delete
) || fail 'native browser/lifecycle capability verification failed'

mkdir -- "$lock_dir" || fail 'another companion installer is active (or a stale .twig-bench-install.lock needs removal)'
locked=yes
for target in "$install_dir/$product" "$install_dir/twig-bench-native"; do
  [ ! -L "$target" ] || fail "refusing to replace a link: $target"
done
[ ! -e "$install_dir/$product" ] || [ -f "$install_dir/$product" ] || fail 'browser destination is not a regular file'
[ ! -e "$install_dir/twig-bench-native" ] || [ -d "$install_dir/twig-bench-native" ] || fail 'companion destination is not a directory'
mkdir -- "$tmpdir/previous"
if [ -e "$install_dir/$product" ]; then
  mv -- "$install_dir/$product" "$tmpdir/previous/$product"
  old_browser=yes
fi
if [ -e "$install_dir/twig-bench-native" ]; then
  mv -- "$install_dir/twig-bench-native" "$tmpdir/previous/twig-bench-native"
  old_native=yes
fi
mv -- "$stage/twig-bench-native" "$install_dir/twig-bench-native"
new_native=yes
mv -- "$stage/$product" "$install_dir/$product"
new_browser=yes
committed=yes
printf '%s: installed %s for %s/%s; whole bundle SHA-256 and native browser/lifecycle capabilities verified.\n' "$product" "$version" "$os" "$arch"
printf 'Run %s/%s from an existing Twig workspace. No PATH, normal Twig, shims, or workspace data were changed.\n' "$install_dir" "$product"
printf '%s\n' 'Close old browser panels before upgrading; migrate only explicitly with normal Twig, then acknowledge/reconnect (Ctrl+R).'
