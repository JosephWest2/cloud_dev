#!/usr/bin/env bash
# Internal entry point: run only inside the disposable check-arch.sh container.
set -euo pipefail
# Official container images omit documentation; exercise a normal full install.
sed -i '/^NoExtract[[:space:]]*=/d' /etc/pacman.conf
pacman -Syu --needed --noconfirm go git python openssh procps-ng namcap
git clone --no-checkout /input/source.bundle /work
tar -xf /input/source.tar -C /work
if ! getent group "$ARCH_HOST_GID" >/dev/null; then
  groupadd --gid "$ARCH_HOST_GID" builder
fi
useradd --create-home --uid "$ARCH_HOST_UID" --gid "$ARCH_HOST_GID" builder
chown -R "$ARCH_HOST_UID:$ARCH_HOST_GID" /work /output
export GOMAXPROCS=2
export AWS_EC2_METADATA_DISABLED=true
version=${ARCH_RELEASE_VERSION:?}

runuser -u builder -- bash -euo pipefail <<'BUILD'
cd /work
git config user.name 'Arch packaging check'
git config user.email 'arch-check@example.invalid'
git add .
if ! git diff --cached --quiet; then
  git -c core.hooksPath=/dev/null commit -qm 'Packaging test source snapshot'
fi
export SOURCE_DATE_EPOCH=$(git log -1 --format=%ct)
cd packaging/arch/cloud-dev-git
makepkg --printsrcinfo > /tmp/git.SRCINFO
diff -u .SRCINFO /tmp/git.SRCINFO
cd /work
python3 scripts/prepare-arch-release.py "$ARCH_RELEASE_VERSION" --output /work/stable
cd /work/stable
makepkg --printsrcinfo > .SRCINFO
makepkg --cleanbuild --force --noconfirm
cp cloud-dev-*.pkg.tar.zst /output/
cp cloud-dev-*.tar.gz /output/
tar -czf "/output/cloud-dev-$ARCH_RELEASE_VERSION-aur.tar.gz" PKGBUILD .SRCINFO
cp SOURCE_COMMIT /output/

# Foundation setup ships real, matching runtime artifacts; users do not build.
cd /work
make runner cleanup-check
python3 scripts/package-setup.py "$ARCH_RELEASE_VERSION" --output "/output/cloud-dev-$ARCH_RELEASE_VERSION-setup-linux-amd64.tar.gz"
cp scripts/install.sh /output/install.sh
python3 scripts/test-setup-installer.py

# Exercise the VCS recipe against the same snapshot, including uncommitted work.
mkdir /work/vcs
cp /work/packaging/arch/cloud-dev-git/PKGBUILD /work/vcs/
cd /work/vcs
sed -i 's|source=("cloud_dev::git+$url.git")|source=("cloud_dev::git+file:///work")|' PKGBUILD
makepkg --cleanbuild --force --noconfirm
cp cloud-dev-git-*.pkg.tar.zst /output/
BUILD

lint() {
  namcap "$1" | tee /tmp/namcap.log
  if grep -q ' E: ' /tmp/namcap.log; then
    echo "namcap reported an error for $1" >&2
    exit 1
  fi
}
for recipe in /work/stable/PKGBUILD /work/vcs/PKGBUILD; do lint "$recipe"; done
for package in /output/*.pkg.tar.zst; do
  lint "$package"
  name=$(pacman -Qp "$package" | cut -d ' ' -f 1)
  pacman -U --noconfirm "$package"
  runuser -u builder -- devbox --help >/dev/null
  actual=$(runuser -u builder -- devbox --json version)
  expected=$(pacman -Q "$name" | cut -d ' ' -f 2)
  expected=${expected%-*}
  python3 -c 'import json,sys; assert json.loads(sys.argv[1])["version"] == sys.argv[2]' "$actual" "$expected"
  test -f "/usr/share/licenses/$name/LICENSE"
  test -f "/usr/share/doc/$name/examples/config.toml"

  # A packaging revision must upgrade cleanly without touching user config.
  install -d -o builder -g "$ARCH_HOST_GID" /home/builder/.config/devbox
  echo 'preserve this user configuration' >/home/builder/.config/devbox/config.toml
  if [[ $name == cloud-dev ]]; then recipe_dir=/work/stable; else recipe_dir=/work/vcs; fi
  runuser -u builder -- bash -euc 'cd "$1"; sed -i "s/^pkgrel=1$/pkgrel=2/" PKGBUILD; makepkg --repackage --force --noconfirm' bash "$recipe_dir"
  pacman -U --noconfirm "$recipe_dir/$name-$expected-2-x86_64.pkg.tar.zst"
  test "$(pacman -Q "$name")" = "$name $expected-2"
  pacman -R --noconfirm "$name"
  test ! -e /usr/bin/devbox
  test ! -e "/usr/share/doc/$name"
  test "$(cat /home/builder/.config/devbox/config.toml)" = 'preserve this user configuration'
done
cd /output
sha256sum ./*.pkg.tar.zst ./*.tar.gz install.sh SOURCE_COMMIT > SHA256SUMS
