#!/usr/bin/env python3
"""Check the real deployment archive and package determinism/update behavior."""
import hashlib
from pathlib import Path
import subprocess
import tempfile
import zipfile

source = Path("bin/cleanup/bootstrap")
archive = Path("bin/devbox-cleanup-linux-amd64.zip")
with zipfile.ZipFile(archive) as packaged:
    assert packaged.namelist() == ["bootstrap"]
    entry = packaged.getinfo("bootstrap")
    assert entry.external_attr >> 16 == 0o100755
    assert entry.date_time == (1980, 1, 1, 0, 0, 0)
    assert packaged.read("bootstrap") == source.read_bytes()
with tempfile.TemporaryDirectory(prefix="devbox-cleanup-package-") as temp:
    target = Path(temp) / "repeated.zip"
    command = ["python3", "scripts/package-cleanup.py"]
    subprocess.run(command + [str(source), str(target)], check=True)
    assert archive.read_bytes() == target.read_bytes()
    changed = Path(temp) / "changed-bootstrap"
    changed.write_bytes(source.read_bytes() + b"package digest change fixture")
    subprocess.run(command + [str(changed), str(target)], check=True)
    assert hashlib.sha256(archive.read_bytes()).digest() != hashlib.sha256(target.read_bytes()).digest()
    changed.write_bytes(b"not a Linux x86_64 executable")
    assert subprocess.run(command + [str(changed), str(target)], capture_output=True).returncode != 0
print("cleanup archive architecture, executable mode, determinism and digest updates passed")
