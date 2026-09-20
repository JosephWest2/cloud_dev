#!/usr/bin/env bash
# Run after check-arch.sh; accepts its fixture asset directory.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
assets=$(realpath -- "${1:?pass the Arch fixture asset directory}")
test -f "$assets/cloud-dev-0.0.0-1-x86_64.pkg.tar.zst"
test -f "$assets/cloud-dev-0.0.0-setup-linux-amd64.tar.gz"
docker run --rm \
  --mount "type=bind,src=$assets,dst=/input,readonly" \
  --mount "type=bind,src=$root/scripts,dst=/scripts,readonly" \
  archlinux:base-devel@sha256:4894f5a268c696fad671966f383175a13faf433c9d9c88cdd4e32eaa2d18838b \
  bash /scripts/check-setup-install-container.sh
