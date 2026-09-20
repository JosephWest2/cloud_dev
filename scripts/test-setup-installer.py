#!/usr/bin/env python3
"""Offline tests of installer extraction, checksums, conflicts and bundle layout."""
import hashlib
import importlib.util
import io
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
INSTALLER = ROOT / 'scripts/install.sh'


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='devbox-installer-test-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def shell(self, command, *args):
        return subprocess.run(['bash', '-c', 'source "$1"; shift; ' + command,
                               'installer-test', str(INSTALLER), *map(str, args)],
                              capture_output=True, text=True)

    def archive(self, name='safe/file', kind=tarfile.REGTYPE, link=''):
        path = self.root / 'input.tar.gz'
        with tarfile.open(path, 'w:gz') as archive:
            item = tarfile.TarInfo(name)
            item.type = kind
            item.linkname = link
            body = b'verified bytes'
            if kind == tarfile.REGTYPE:
                item.size = len(body)
                archive.addfile(item, io.BytesIO(body))
            else:
                archive.addfile(item)
        return path

    @unittest.skipUnless(shutil.which('bsdtar'), 'libarchive tools required')
    def test_extract_regular_only(self):
        archive = self.archive()
        result = self.shell('safe_extract "$1" "$2"', archive, self.root / 'out')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / 'out/safe/file').read_bytes(), b'verified bytes')
        for name, kind, link in [('../outside', tarfile.REGTYPE, ''),
                                 ('/absolute', tarfile.REGTYPE, ''),
                                 ('safe/link', tarfile.SYMTYPE, '/tmp'),
                                 ('safe/hard', tarfile.LNKTYPE, '/tmp'),
                                 ('fifo', tarfile.FIFOTYPE, '')]:
            with self.subTest(name=name):
                archive = self.archive(name, kind, link)
                self.assertNotEqual(self.shell('safe_extract "$1" "$2"', archive,
                                              self.root / 'unsafe').returncode, 0)

    def test_checksum_failure(self):
        f = self.root / 'asset'
        f.write_bytes(b'asset')
        good = hashlib.sha256(f.read_bytes()).hexdigest()
        self.assertEqual(self.shell('verify_file "$1" "$2"', good, f).returncode, 0)
        self.assertNotEqual(self.shell('verify_file "$1" "$2"', '0' * 64, f).returncode, 0)
        alias = self.root / 'alias'
        alias.symlink_to(f)
        self.assertNotEqual(self.shell('verify_file "$1" "$2"', good, alias).returncode, 0)

    def test_preserve_existing_installation(self):
        link = self.root / 'plugin'
        link.write_text('existing user executable')
        self.assertNotEqual(self.shell('install_link "$1" "$2"', '/new/plugin', link).returncode, 0)
        self.assertEqual(link.read_text(), 'existing user executable')
        symlink = self.root / 'owned'
        self.assertEqual(self.shell('install_link "$1" "$2"', '/new/plugin', symlink).returncode, 0)
        self.assertEqual(self.shell('install_link "$1" "$2"', '/new/plugin', symlink).returncode, 0)

    def test_no_noninteractive_or_implicit_release_install(self):
        for args in [[], ['--version', 'latest'], ['--version', '0.0.0'], ['--yes']]:
            result = subprocess.run(['bash', str(INSTALLER), *args], input='', text=True,
                                    capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn('unbound variable', result.stderr)

    def test_refuse_symlink_parent(self):
        (self.root / 'actual').mkdir()
        (self.root / 'link').symlink_to(self.root / 'actual')
        result = self.shell('safe_directory "$1"', self.root / 'link/child')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'actual/child').exists())


if __name__ == '__main__':
    unittest.main()
