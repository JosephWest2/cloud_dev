#!/usr/bin/env python3
"""Package the pinned foundation and already-built runtimes for guided setup."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile


def package(root, version, output):
    if not re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version):
        raise ValueError('use a numbered release version')
    commit = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    tracked = subprocess.check_output(['git', 'ls-files', '-z', 'infra'], cwd=root).decode().split('\0')
    names = [n for n in tracked if n and '/tests/' not in n and not n.endswith('.md')]
    names += ['bin/devbox-runner-linux-amd64', 'bin/devbox-cleanup-linux-amd64.zip']
    data = {}
    for name in sorted(names):
        p = root / name
        if p.is_symlink() or not p.is_file():
            raise ValueError('bundle source must be a regular file: ' + name)
        data[name] = p.read_bytes()
    manifest = {'schema_version': 1, 'version': version, 'source_commit': commit,
                'files': {n: hashlib.sha256(b).hexdigest() for n, b in data.items()}}
    data['bundle.json'] = (json.dumps(manifest, indent=2, sort_keys=True) + '\n').encode()
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open('xb') as dest:
        with gzip.GzipFile(filename='', fileobj=dest, mode='wb', mtime=0) as zipped:
            with tarfile.open(fileobj=zipped, mode='w') as archive:
                for name, body in sorted(data.items()):
                    info = tarfile.TarInfo(name)
                    info.size = len(body)
                    info.mode = 0o600
                    info.mtime = 0
                    archive.addfile(info, io.BytesIO(body))
    # Re-open the delivered archive and prove its manifest against every byte.
    with tarfile.open(output) as archive:
        assert set(archive.getnames()) == set(data)
        for name, body in data.items():
            assert archive.extractfile(name).read() == body
    return manifest


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('version')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    result = package(root, args.version, args.output)
    print(f'Packaged {len(result["files"])} verified files from {result["source_commit"]}')
