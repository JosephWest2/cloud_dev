#!/usr/bin/env python3
"""Prepare a source archive and stable Arch recipe from a committed Git ref."""

import argparse
import gzip
import hashlib
from pathlib import Path
import re
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", help="release version without v, e.g. 0.1.0")
    parser.add_argument("--ref", default="HEAD", help="committed source ref (default: HEAD)")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.version):
        parser.error("version must be a three-component numeric release, e.g. 0.1.0")
    commit = subprocess.check_output(
        ["git", "rev-parse", "--verify", args.ref + "^{commit}"], text=True
    ).strip()
    license_text = subprocess.check_output(["git", "show", f"{commit}:LICENSE"], text=True)
    if not license_text.startswith("MIT License\n"):
        parser.error("the selected commit must contain the approved MIT LICENSE")
    template = subprocess.check_output(
        ["git", "show", f"{commit}:packaging/arch/cloud-dev/PKGBUILD.in"], text=True
    )
    args.output.mkdir(parents=True, exist_ok=True)
    archive = args.output / f"cloud-dev-{args.version}.tar.gz"
    if archive.exists() or (args.output / "PKGBUILD").exists():
        parser.error("output already contains release files; choose a fresh directory")
    # A fixed gzip header and git archive's commit timestamps give stable bytes.
    with archive.open("wb") as raw:
        with gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as compressed:
            process = subprocess.Popen(
                ["git", "archive", "--format=tar", f"--prefix=cloud-dev-{args.version}/", commit],
                stdout=subprocess.PIPE,
            )
            while chunk := process.stdout.read(1024 * 1024):
                compressed.write(chunk)
            if process.wait() != 0:
                raise SystemExit("git archive failed")
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    recipe = template.replace("@VERSION@", args.version).replace("@SHA256@", digest)
    (args.output / "PKGBUILD").write_text(recipe)
    (args.output / "SOURCE_COMMIT").write_text(commit + "\n")
    print(f"Prepared {archive} from {commit}; generate .SRCINFO with makepkg.")


if __name__ == "__main__":
    main()
