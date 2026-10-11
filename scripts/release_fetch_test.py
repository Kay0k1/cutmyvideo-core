"""Real ZIP/layout and artifact-identity negative controls; no build proof."""

import importlib.util
from pathlib import Path
import stat
import struct
import tempfile
import unittest
import warnings
import zipfile

SPEC = importlib.util.spec_from_file_location('release_fetch', Path(__file__).with_name('release-fetch.py'))
fetch = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fetch)
release = fetch.release


class FetchControls(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.source = {'version': 'v0.0.0-candidate.g' + 'a' * 40}
        self.files = {release.archive_name(self.source, os_, arch): b'synthetic archive'
                      for os_, arch in release.TARGETS}
        self.files.update({'SOURCE.json': release.canonical(self.source), 'MANIFEST.json': b'{}\n', 'SHA256SUMS': b'synthetic\n'})

    def tearDown(self):
        self.temporary.cleanup()

    def zipped(self, entries=None, symlink=None):
        output = self.root / 'artifact.zip'
        with warnings.catch_warnings():
            warnings.simplefilter('ignore', UserWarning)
            with zipfile.ZipFile(output, 'w', zipfile.ZIP_DEFLATED) as archive:
                for name, data in (entries or list(self.files.items())):
                    item = zipfile.ZipInfo(name)
                    item.compress_type = zipfile.ZIP_DEFLATED
                    item.create_system = 3
                    item.external_attr = ((stat.S_IFLNK | 0o777) if name == symlink else (stat.S_IFREG | 0o644)) << 16
                    archive.writestr(item, data)
        return output

    def reject(self, archive):
        target = self.root / 'bundle'
        with self.assertRaises(ValueError):
            fetch.extract_bundle(archive, target, self.source)
        self.assertFalse(target.exists())
        self.assertEqual(list(self.root.glob('.cutmy-download-*')), [])

    def test_exact_flat_bundle_and_existing_destination(self):
        archive = self.zipped()
        target = self.root / 'bundle'
        fetch.extract_bundle(archive, target, self.source)
        self.assertEqual({p.name: p.read_bytes() for p in target.iterdir()}, self.files)
        with self.assertRaises(ValueError):
            fetch.extract_bundle(archive, target, self.source)

    def test_missing_extra_duplicate_and_traversal_entries(self):
        entries = list(self.files.items())
        bad_sets = [entries[:-1], entries + [('stale.tar.gz', b'x')],
                    entries[:-1] + [entries[0]], entries[:-1] + [('../escape', b'x')]]
        for bad in bad_sets:
            with self.subTest(names=[name for name, _ in bad]):
                self.reject(self.zipped(bad))
        self.assertFalse((self.root / 'escape').exists())

    def test_symlink_and_mixed_source_are_rejected(self):
        self.reject(self.zipped(symlink=next(iter(self.files))))
        mixed = dict(self.files, **{'SOURCE.json': b'{}\n'})
        self.reject(self.zipped(list(mixed.items())))

    def test_declared_oversized_member_is_rejected_before_read(self):
        archive = self.zipped()
        raw = bytearray(archive.read_bytes())
        offset = raw.index(b'PK\x01\x02')
        struct.pack_into('<I', raw, offset + 24, fetch.MAX_MEMBER + 1)
        archive.write_bytes(raw)
        self.reject(archive)

    def test_unbounded_directory_is_rejected_before_zip_parser(self):
        archive = self.zipped()
        raw = bytearray(archive.read_bytes())
        struct.pack_into('<H', raw, len(raw) - 12, 65535)
        archive.write_bytes(raw)
        self.reject(archive)

    def test_artifact_id_run_digest_name_expiry_and_size(self):
        expected = 'b' * 64
        metadata = {'id': 42, 'workflow_run': {'id': 123}, 'name': 'bundle',
                    'expired': False, 'digest': 'sha256:' + expected, 'size_in_bytes': 100}
        fetch.validate_metadata(metadata, 42, 123, 'bundle', expected)
        patches = [{'id': 43}, {'workflow_run': {'id': 124}}, {'name': 'old'},
                   {'digest': 'sha256:' + 'c' * 64}, {'expired': True},
                   {'size_in_bytes': 0}, {'size_in_bytes': fetch.MAX_DOWNLOAD + 1}]
        for patch in patches:
            with self.subTest(patch=patch), self.assertRaises(ValueError):
                fetch.validate_metadata(dict(metadata, **patch), 42, 123, 'bundle', expected)


if __name__ == '__main__':
    unittest.main()
