#!/usr/bin/env bash
# Internal disposable-container entry point; only synthetic release assets mount.
set -euo pipefail
pacman -Syu --needed --noconfirm sudo curl libarchive util-linux
# Build tools are deliberately absent from the end-user installation workflow.
if pacman -Q make >/dev/null 2>&1; then pacman -Rdd --noconfirm make; fi
useradd --create-home installer
printf 'installer ALL=(ALL) NOPASSWD: ALL\n' > /etc/sudoers.d/devbox-installer
chmod 440 /etc/sudoers.d/devbox-installer
cat > /home/installer/run.sh <<'RUN'
#!/usr/bin/env bash
set -euo pipefail
source /scripts/install.sh
# Only the project's synthetic release is substituted. Upstream pinned tool
# downloads, checksums, package operations and executable probes are real.
fetch() {
  case "$1" in
    https://api.github.com/repos/JosephWest2/cloud_dev/releases/tags/v0.0.0)
      printf '{"draft":false,"prerelease":false}\n' > "$2" ;;
    https://github.com/JosephWest2/cloud_dev/releases/download/v0.0.0/*)
      cp -- "/input/${1##*/}" "$2" ;;
    *) curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$1" -o "$2" ;;
  esac
}
main --version 0.0.0
RUN
chmod 755 /home/installer/run.sh
chown installer:installer /home/installer/run.sh
# script provides an actual terminal; no installer confirmation bypass exists.
for attempt in 1 2; do
  set +e
  { while sleep 1; do printf 'y\n'; done; } |
    script --quiet --return --command 'runuser -u installer -- /home/installer/run.sh' /dev/null
  result=${PIPESTATUS[1]}
  set -e
  test "$result" -eq 0
done
runuser -u installer -- bash -euo pipefail <<'VERIFY'
export PATH="$HOME/.local/bin:$PATH"
test "$(devbox version)" = 'devbox 0.0.0'
test "$(session-manager-plugin --version)" = '1.2.835.0'
test -f "$HOME/.local/share/devbox/bundles/0.0.0/bundle.json"
"$HOME/.local/share/devbox/tools/tofu-1.12.6/tofu" version | head -1 | grep -Fx 'OpenTofu v1.12.6'
for tool in go make jq git; do ! command -v "$tool"; done
set +e
output=$(devbox setup --json)
code=$?
set -e
test "$code" -eq 2
[[ $output == *'"code":"confirmation_required"'* ]]
test ! -e "$HOME/.aws/credentials"
test ! -e "$HOME/.config/devbox/config.toml"
VERIFY
printf 'Guided installer fresh-install and rerun checks passed without build tools or AWS credentials.\n'
