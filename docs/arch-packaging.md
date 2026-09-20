# Arch Linux packages

The package names are `cloud-dev` (numbered releases) and `cloud-dev-git`
(current Git source); both install `/usr/bin/devbox` on x86-64 Arch Linux.
They conflict with each other and with Jetify's unrelated `devbox` and
`devbox-bin`, which use the same executable path. They do not provide Jetify's
Devbox functionality. AUR name availability must be rechecked before submission.

Packages contain the CLI, MIT license, example configuration, embedded-profile
source and documentation. The default profile is also embedded in the CLI.
The package does not create user configuration, SSH keys, AWS credentials or
resources. Runner and Lambda artifacts remain part of foundation provisioning.
Installed guides are under `/usr/share/doc/cloud-dev/` or
`/usr/share/doc/cloud-dev-git/`; links to source files outside those guides require
a matching source checkout.

## Install without an AUR account

Until a release is published, use the source-build instructions below. After
publication, download these assets from the selected
[GitHub release](https://github.com/JosephWest2/cloud_dev/releases):

- `cloud-dev-VERSION-1-x86_64.pkg.tar.zst`
- `cloud-dev-VERSION.tar.gz`
- `cloud-dev-VERSION-aur.tar.gz`
- `SOURCE_COMMIT` and `SHA256SUMS`
- For releases including guided setup: `install.sh` and
  `cloud-dev-VERSION-setup-linux-amd64.tar.gz`

In the directory containing those files, verify and install (replace `VERSION`):

```sh
sha256sum --check SHA256SUMS
sudo pacman -U ./cloud-dev-VERSION-1-x86_64.pkg.tar.zst
devbox version
devbox --help
```

Checksums detect corrupted downloads; they are not detached publisher signatures.
These local packages are not installed from a pacman repository. Install a later
release with `pacman -U` to upgrade; `pacman -Syu` alone will not fetch project
updates. Remove with `sudo pacman -R cloud-dev`. User configuration is preserved.

To build current source, no AUR login is needed:

```sh
sudo pacman -S --needed base-devel git go openssh procps-ng
git clone https://github.com/JosephWest2/cloud_dev.git
cd cloud_dev/packaging/arch/cloud-dev-git
less PKGBUILD
makepkg -si
```

`makepkg` must run as your regular user. This recipe fetches current upstream
Git source; it does not build uncommitted changes in the surrounding checkout.
To build a numbered release, extract its `-aur.tar.gz` into a fresh directory,
review `PKGBUILD`, then run `makepkg -si`. The source archive URL points at that
release and its SHA-256 is pinned. Base development tools are assumed by Arch;
Go is needed only for building. Build dependencies and test dependencies are
recorded separately from runtime dependencies.

## Optional access tools and AWS setup

Install OpenSSH for interactive access and the AWS CLI for authentication/setup:

```sh
sudo pacman -S --needed openssh aws-cli-v2
```

SSH also needs AWS Session Manager plugin **1.2.764.0 or newer**. Reading and
cloning an existing AUR package does not require an AUR account:

```sh
git clone https://aur.archlinux.org/aws-session-manager-plugin.git
cd aws-session-manager-plugin
less PKGBUILD
makepkg -si
session-manager-plugin --version
```

Check the recipe's version before installing. If the available package is too
old, follow [AWS's plugin installation instructions](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)
for a supported version. Keep plugin logging disabled as described in
[local access tools](configuration.md#local-access-tools).

For new foundations, use [guided setup](guided-setup.md) and the matching release
setup bundle; `devbox setup` invokes pinned OpenTofu after plan approval. The
[manual foundation path](setup.md) still uses a matching source checkout, Go,
Python and `jq`. Installation alone creates no deployment manifest or AWS resources.

## Packaging checks

The maintained recipes are
[`cloud-dev-git/PKGBUILD`](../packaging/arch/cloud-dev-git/PKGBUILD) and the
[`cloud-dev/PKGBUILD.in`](../packaging/arch/cloud-dev/PKGBUILD.in) release template.
The stable recipe is generated only when there is a concrete source archive,
so the repository does not carry an invented release URL/checksum.

From a checkout with Docker available:

```sh
bash scripts/check-arch.sh
```

This checks the current working tree in a fresh Arch container using a pinned
base image and current Arch packages. It verifies the committed Git `.SRCINFO`,
builds both recipes, runs their offline Go tests and `namcap`, and exercises
installation, a packaging-revision upgrade, removal and preservation of user
configuration. Only the isolated container's pacman database is changed.
Outputs are retained in an ignored `bin-arch-check.*` directory. Version `0.0.0`
is a validation fixture, not a release. A local Git snapshot records uncommitted
changes; `SOURCE_COMMIT` identifies that snapshot. No AWS credentials are mounted
and no live AWS acceptance is performed.

For an additional devtools clean-chroot build on Arch, install `devtools`, then
run `extra-x86_64-build` from the generated stable recipe directory (with its
source archive) or from `packaging/arch/cloud-dev-git/`. This needs local privilege
to create the chroot. A fresh Docker build is not a claim that this separate
devtools check ran.

The [Arch workflow](../.github/workflows/arch.yml) performs the container checks
on pull requests, pushes to `main`, manual dispatch and version tags. Its
`Arch packages` job should be added to required PR checks if branch protection
is used. CI artifacts contain a tested stable fixture package, source archive,
generated `PKGBUILD`/`.SRCINFO` bundle and checksums. GitHub-hosted runs still
need to pass after this workflow is pushed.

## Cut a release

Choose a version of the form `MAJOR.MINOR.PATCH`, merge the changes and ensure
the ordinary CI and Arch checks pass. Then tag that commit, for example:

```sh
git tag -a v0.1.0 -m 'devbox 0.1.0'
git push origin v0.1.0
```

The workflow builds from the tagged commit, embeds `0.1.0` in both CLI version
formats, uploads workflow artifacts, and attaches them to a **draft** GitHub
release. Review its assets, checksums and release notes, then publish the draft.
The tagged source archive is generated with `git archive` and a deterministic
gzip header. Its hash is embedded in the stable recipe. `.SRCINFO` is generated
by `makepkg`, not handwritten. `SOURCE_COMMIT` must match the tagged commit.
The upload deliberately refuses to overwrite existing release assets on a rerun;
investigate failures before removing/replacing an asset. Do not move public tags.

To prepare inputs locally from an already committed ref:

```sh
python3 scripts/prepare-arch-release.py 0.1.0 --ref v0.1.0 --output bin/arch-0.1.0
cd bin/arch-0.1.0
makepkg --printsrcinfo > .SRCINFO
makepkg -s
namcap PKGBUILD ./*.pkg.tar.zst
```

The preparation command does not tag, push or publish anything. It rejects
nonrelease version strings and refuses to overwrite prepared release files.

## Publish to AUR when registration is available

An account is needed to maintain AUR recipes, not to install them. Once you can
register, add an SSH public key to your account and check both package names and
the current `devbox` executable conflicts again. Set the public Git author name
and email you want associated with your AUR commits.

Create separate repositories:

```sh
git -c init.defaultBranch=master clone ssh://aur@aur.archlinux.org/cloud-dev.git
git -c init.defaultBranch=master clone ssh://aur@aur.archlinux.org/cloud-dev-git.git
```

For `cloud-dev`, copy `PKGBUILD` and `.SRCINFO` from the published release's
`-aur.tar.gz`. For `cloud-dev-git`, copy the maintained recipe and generate fresh
metadata after a successful build. Verify the maintainer contact comment in each
recipe. In each AUR repository:

```sh
makepkg --printsrcinfo > .SRCINFO
git add PKGBUILD .SRCINFO
git commit -m 'Initial package'
git push origin master
```

Commit recipes and required support files only. For stable updates, use the new
release's recipe/checksum and reset `pkgrel` to 1; increment `pkgrel` for packaging
fixes to the same release. The Git recipe computes `pkgver()` when built and does
not need an AUR commit for every upstream commit. Regenerate `.SRCINFO` for every
metadata change. No AUR publishing credential is needed by the GitHub workflow.

Reference: [Arch Go packaging](https://wiki.archlinux.org/title/Go_package_guidelines),
[creating packages](https://wiki.archlinux.org/title/Creating_packages), and
[AUR submission](https://wiki.archlinux.org/title/AUR_submission_guidelines).
