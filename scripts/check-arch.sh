#!/usr/bin/env bash
# Build and test the current working tree in a fresh Arch Linux container.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
if (( EUID == 0 )); then
  echo 'Run this check as a regular user with Docker access.' >&2
  exit 2
fi
version=${1:-0.0.0}
if [[ ! $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo 'Expected a numeric release version, e.g. 0.1.0' >&2
  exit 2
fi
output=$(mktemp -d "$PWD/bin-arch-check.XXXXXX")
trap 'rm -f "$output/source.tar" "$output/source.bundle"' EXIT
# Include uncommitted implementation changes without copying ignored AWS inputs.
git ls-files --cached --others --exclude-standard -z |
  while IFS= read -r -d '' path; do
    if [[ -e $path || -L $path ]]; then printf '%s\0' "$path"; fi
  done |
  tar --null --files-from=- -cf "$output/source.tar"
git bundle create "$output/source.bundle" HEAD
docker run --rm \
  --mount "type=bind,src=$output/source.tar,dst=/input/source.tar,readonly" \
  --mount "type=bind,src=$output/source.bundle,dst=/input/source.bundle,readonly" \
  --mount "type=bind,src=$output,dst=/output" \
  -e "ARCH_RELEASE_VERSION=$version" \
  -e "ARCH_HOST_UID=$(id -u)" -e "ARCH_HOST_GID=$(id -g)" \
  archlinux:base-devel@sha256:4894f5a268c696fad671966f383175a13faf433c9d9c88cdd4e32eaa2d18838b \
  bash -o pipefail -c 'tar -xOf /input/source.tar scripts/check-arch-container.sh | bash'
printf 'Validated Arch packages and release inputs: %s\n' "$output"
