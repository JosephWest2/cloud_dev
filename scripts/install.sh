#!/usr/bin/env bash
# Download this file from the selected release, inspect it, then run with --version.
set -euo pipefail

TOFU_VERSION=1.12.6
TOFU_ARCHIVE_SHA256=50a6106fa4de523d09c87af85f3db1dd47535fc005727fdca6852146476b88ec
TOFU_BINARY_SHA256=8f95cbe1523ef7b7913773634a6d6ac94c38f3c8eadba2e18b1e5b01567561ad
PLUGIN_VERSION=1.2.835.0
PLUGIN_ARCHIVE_SHA256=7c6dcad12518571cc7959a713e6a8ae1bdf6ed66fd9bee37dc189e39ca58ae03
PLUGIN_BINARY_SHA256=f6002be08e5c57dc97eb9a0ef819d54f4c9a4a724c5818cec7d4baeaadfe4cbd

fail() { printf 'devbox installer: %s\n' "$*" >&2; return 1; }
confirm() {
  local reply
  printf '%s [y/N] ' "$1" >&2
  IFS= read -r reply || return 1
  [[ $reply == y || $reply == Y || $reply == yes || $reply == YES ]]
}
verify_file() {
  local actual
  [[ -f $2 && ! -L $2 ]] || return 1
  [[ $1 =~ ^[0-9a-f]{64}$ ]] || return 1
  actual=$(sha256sum -- "$2"); actual=${actual%% *}
  [[ $actual == "$1" ]] || fail 'download checksum mismatch; nothing installed'
}
safe_directory() {
  local check=$1
  [[ $check == /* && $check != *$'\n'* && $check != *$'\r'* ]] || return 1
  while [[ $check != / ]]; do
    [[ ! -L $check ]] || { fail 'refusing a symlink in installation path'; return 1; }
    check=$(dirname -- "$check")
  done
  mkdir -p -- "$1"
}
# Archives are verified first. Refuse links, special files and traversal even
# from a release archive; never extract untrusted paths as root.
safe_extract() {
  local archive=$1 destination=$2 name line
  while IFS= read -r name; do
    [[ -n $name && $name != /* && $name != .. && $name != ../* && $name != */../* && $name != *'/..' && $name != *\\* ]] || return 1
  done < <(bsdtar -tf "$archive")
  while IFS= read -r line; do
    [[ ${line:0:1} == - || ${line:0:1} == d ]] || return 1
  done < <(bsdtar -tvf "$archive")
  safe_directory "$destination"
  bsdtar --no-same-owner --no-same-permissions -xf "$archive" -C "$destination"
}
fetch() { curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$1" -o "$2"; }
install_link() {
  local target=$1 link=$2
  if [[ -e $link || -L $link ]]; then
    if [[ -L $link && $(readlink -- "$link") == "$target" ]]; then return; fi
    fail "preserving existing $link; resolve the conflict before rerunning"; return 1
  fi
  ln -s -- "$target" "$link"
}
main() {
  local version='' prerelease=false
  while (($#)); do
    case $1 in
      --version) (($#>=2)) || return 2; version=$2; shift 2 ;;
      --prerelease) prerelease=true; shift ;;
      --help|-h) printf 'Usage: bash install.sh --version MAJOR.MINOR.PATCH [--prerelease]\nGuided Arch Linux x86-64 installation. No AWS changes.\n'; return ;;
      *) fail 'unknown option'; return 2 ;;
    esac
  done
  [[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || { fail 'select an explicit numbered release with --version'; return 2; }
  [[ -t 0 && -t 2 ]] || { fail 'run interactively in a terminal'; return 2; }
  ((EUID!=0)) || { fail 'run as your regular user; only package operations use sudo'; return 2; }
  [[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { fail 'automated installation targets Linux x86-64'; return 2; }
  [[ -r /etc/arch-release ]] || { fail 'automated dependency installation currently targets Arch Linux'; return 2; }
  for tool in curl bsdtar sha256sum pacman sudo; do command -v "$tool" >/dev/null || { fail "install prerequisite $tool"; return 1; }; done
  for package in devbox devbox-bin cloud-dev-git; do
    if pacman -Q "$package" >/dev/null 2>&1; then fail "package conflict: $package; choose which installation to keep first"; return 1; fi
  done
  if command -v devbox >/dev/null && ! pacman -Q cloud-dev >/dev/null 2>&1; then fail 'an unmanaged devbox executable exists; resolve its ownership first'; return 1; fi
  umask 077
  local staging data localbin base pkg bundle expected metadata cleanup
  staging=$(mktemp -d)
  printf -v cleanup 'rm -rf -- %q' "$staging"
  trap "$cleanup" EXIT
  data=${XDG_DATA_HOME:-$HOME/.local/share}/devbox
  localbin=$HOME/.local/bin
  safe_directory "$data"; safe_directory "$localbin"
  base=https://github.com/JosephWest2/cloud_dev/releases/download/v$version
  pkg=cloud-dev-$version-1-x86_64.pkg.tar.zst
  bundle=cloud-dev-$version-setup-linux-amd64.tar.gz
  fetch "https://api.github.com/repos/JosephWest2/cloud_dev/releases/tags/v$version" "$staging/release.json"
  metadata=$(tr -d '\n\r\t ' < "$staging/release.json")
  [[ $metadata == *'"draft":false'* || $metadata == *'"draft":true'* ]] || { fail 'unrecognized release metadata'; return 1; }
  [[ $metadata == *'"prerelease":false'* || $metadata == *'"prerelease":true'* ]] || { fail 'unrecognized release metadata'; return 1; }
  if [[ $metadata == *'"draft":true'* ]]; then fail 'draft releases cannot be installed'; return 1; fi
  if [[ $metadata == *'"prerelease":true'* && $prerelease == false ]]; then fail 'selected release is a prerelease; inspect it and opt in with --prerelease'; return 1; fi
  fetch "$base/SHA256SUMS" "$staging/SHA256SUMS"
  for name in "$pkg" "$bundle"; do
    expected=$(awk -v wanted="$name" '$2==wanted || $2=="./"wanted {print $1}' "$staging/SHA256SUMS")
    [[ $expected =~ ^[0-9a-f]{64}$ ]] || { fail 'release has no unique checksum for a required setup asset'; return 1; }
    fetch "$base/$name" "$staging/$name"; verify_file "$expected" "$staging/$name"
  done
  safe_extract "$staging/$bundle" "$staging/bundle" || { fail 'unsafe setup archive'; return 1; }
  if [[ -e $data/bundles/$version ]]; then
    diff -qr "$staging/bundle" "$data/bundles/$version" >/dev/null || { fail 'existing bundle differs; preserving it for recovery'; return 1; }
  else
    safe_directory "$data/bundles"
    mv -- "$staging/bundle" "$data/bundles/$version"
  fi
  printf 'Install reviewed release v%s and Arch dependencies:\n  sudo pacman -S --needed aws-cli-v2 openssh\n  sudo pacman -U %s\n' "$version" "$pkg" >&2
  confirm 'Run these package operations?' || return 2
  sudo pacman -S --needed aws-cli-v2 openssh
  if [[ $(pacman -Q cloud-dev 2>/dev/null || true) != "cloud-dev $version-1" ]]; then sudo pacman -U "$staging/$pkg"; fi
  [[ $(/usr/bin/devbox version) == "devbox $version" ]] || { fail 'installed CLI version mismatch'; return 1; }

  local tofu_dir plugin_dir
  tofu_dir=$data/tools/tofu-$TOFU_VERSION
  plugin_dir=$data/tools/session-manager-plugin-$PLUGIN_VERSION
  if [[ ! -f $tofu_dir/tofu ]]; then
    fetch "https://github.com/opentofu/opentofu/releases/download/v$TOFU_VERSION/tofu_${TOFU_VERSION}_linux_amd64.tar.gz" "$staging/tofu.tar.gz"
    verify_file "$TOFU_ARCHIVE_SHA256" "$staging/tofu.tar.gz"
    safe_extract "$staging/tofu.tar.gz" "$staging/tofu"
    verify_file "$TOFU_BINARY_SHA256" "$staging/tofu/tofu"
    safe_directory "$tofu_dir"; install -m755 "$staging/tofu/tofu" "$tofu_dir/tofu"
  fi
  verify_file "$TOFU_BINARY_SHA256" "$tofu_dir/tofu"
  if [[ ! -f $plugin_dir/bin/session-manager-plugin ]]; then
    fetch "https://s3.amazonaws.com/session-manager-downloads/plugin/$PLUGIN_VERSION/ubuntu_64bit/session-manager-plugin.deb" "$staging/plugin.deb"
    verify_file "$PLUGIN_ARCHIVE_SHA256" "$staging/plugin.deb"
    bsdtar -xOf "$staging/plugin.deb" data.tar.gz > "$staging/plugin.tar.gz"
    safe_extract "$staging/plugin.tar.gz" "$staging/plugin"
    verify_file "$PLUGIN_BINARY_SHA256" "$staging/plugin/usr/local/sessionmanagerplugin/bin/session-manager-plugin"
    safe_directory "$plugin_dir/bin"
    install -m755 "$staging/plugin/usr/local/sessionmanagerplugin/bin/session-manager-plugin" "$plugin_dir/bin/session-manager-plugin"
    for name in LICENSE NOTICE THIRD-PARTY; do install -m600 "$staging/plugin/usr/local/sessionmanagerplugin/$name" "$plugin_dir/$name"; done
  fi
  verify_file "$PLUGIN_BINARY_SHA256" "$plugin_dir/bin/session-manager-plugin"
  # No seelog.xml is installed: logging remains disabled. An unexpected existing
  # config requires review, even in a previously installed tool directory.
  [[ ! -e /usr/local/sessionmanagerplugin/seelog.xml && ! -L /usr/local/sessionmanagerplugin/seelog.xml && ! -e $plugin_dir/seelog.xml && ! -e $plugin_dir/bin/seelog.xml ]] || { fail 'disable logging at /usr/local/sessionmanagerplugin/seelog.xml before continuing'; return 1; }
  install_link "$plugin_dir/bin/session-manager-plugin" "$localbin/session-manager-plugin"
  [[ $("$plugin_dir/bin/session-manager-plugin" --version) == "$PLUGIN_VERSION" ]] || return 1
  if [[ :$PATH: != *":$localbin:"* ]]; then
    local rc line
    case ${SHELL##*/} in bash) rc=$HOME/.bashrc ;; zsh) rc=$HOME/.zshrc ;; *) rc='' ;; esac
    line='export PATH="$HOME/.local/bin:$PATH"'
    if [[ -n $rc && ! -L $rc ]]; then
      printf 'Append to %s:\n%s\n' "$rc" "$line" >&2
      if confirm 'Add the local tools directory to future shells?'; then
        if [[ -f $rc ]]; then install -m600 -- "$rc" "$rc.devbox-backup-$(date +%s)"; fi
        grep -qxF "$line" "$rc" 2>/dev/null || printf '\n%s\n' "$line" >> "$rc"
      fi
    fi
    printf 'For this shell, run: %s\n' "$line" >&2
  fi
  printf 'Installed devbox %s and its setup bundle. Authenticate with AWS, then run:\n  devbox setup\nNo AWS resources were created.\n' "$version"
  rm -rf -- "$staging"
  trap - EXIT
}
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
