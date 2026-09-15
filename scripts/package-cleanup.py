#!/usr/bin/env python3
"""Reproducible Lambda archive: fixed metadata and one executable bootstrap."""
from pathlib import Path
import sys
import zipfile

source, destination = map(Path, sys.argv[1:])
data = source.read_bytes()
if data[:5] != b"\x7fELF\x02" or data[18:20] != b"\x3e\x00":
    raise SystemExit("cleanup requires a Linux x86_64 ELF executable")
entry = zipfile.ZipInfo("bootstrap", (1980, 1, 1, 0, 0, 0))
entry.create_system = 3
entry.external_attr = 0o100755 << 16
entry.compress_type = zipfile.ZIP_DEFLATED
with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
    archive.writestr(entry, data)
