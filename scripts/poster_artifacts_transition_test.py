"""Source and data-preservation controls for the schema009 ->010 acceptance."""
from copy import deepcopy
import importlib.util
import base64
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


poster = module('poster_transition_controls', 'poster-artifacts-transition.py')
current = module('poster_source_controls', 'current-installation-acceptance.py')
proof = module('poster_receipt_controls', 'source-retention-transition.py')
base = module('poster_host_controls', 'self-hosting-acceptance.py')


class PosterTransitionControls(unittest.TestCase):
    def baseline(self):
        return json.loads((ROOT / 'testdata/selfhosting-poster-baseline-source.txt').read_bytes())

    def inventory(self):
        old = current.application({'files': self.baseline()})
        candidate = deepcopy(old)
        for name in (poster.SQL_NAME, poster.SHAPE_NAME):
            data = (ROOT.parent / name).read_bytes()
            candidate[name] = dict(mode='100644', git_blob='0' * 40, bytes=len(data), sha256=proof.digest(data))
        return old, candidate

    def test_previous_source_is_exact_published_schema009(self):
        files = self.baseline()
        self.assertEqual(current.source_tree(files), poster.BASELINE_TREE)
        application = current.application({'files': files})
        self.assertEqual(len(application), poster.BASELINE_FILES)
        self.assertEqual(proof.digest(proof.canonical(application)), poster.BASELINE_APPLICATION_SHA256)

    def test_only_reviewed_additive010_inputs_are_admitted(self):
        old, candidate = self.inventory()
        poster.validate_schema_inventory(old, candidate)
        self.assertEqual(poster.migrations()[-1], dict(position=10, version=poster.MIGRATION, checksum=poster.SQL_SHA256))
        self.assertEqual(len(poster.migrations()), 10)

    def test_historical_sql_and_shape_edits_are_refused(self):
        old, candidate = self.inventory()
        for name in ('internal/app/migrations/001_schema.json', 'internal/app/migrations/009_20261018_external_audio_refs_v1.sql'):
            changed = deepcopy(candidate)
            changed[name]['sha256'] = 'f' * 64
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                poster.validate_schema_inventory(old, changed)

    def test_missing_extra_or_changed_new_migration_is_refused(self):
        old, candidate = self.inventory()
        for variant in ('missing', 'extra', 'changed-sql', 'changed-shape'):
            changed = deepcopy(candidate)
            if variant == 'missing':
                del changed[poster.SHAPE_NAME]
            elif variant == 'extra':
                changed['internal/app/migrations/011_unreviewed.sql'] = deepcopy(changed[poster.SQL_NAME])
            else:
                changed[poster.SQL_NAME if variant == 'changed-sql' else poster.SHAPE_NAME]['sha256'] = 'f' * 64
            with self.subTest(variant=variant), self.assertRaises(RuntimeError):
                poster.validate_schema_inventory(old, changed)

    def test_upgrade_preserves_old_values_and_adds_empty_poster_tables(self):
        before = dict(sources=[{'id': 'original', 'owner': 'owner'}], jobs=[{'id': 'job', 'request': {'format': 'mp4'}}],
                      artifacts=[{'id': 'ready', 'size_bytes': 713}], storage_counters=[{'stored_bytes': 713}])
        after = dict(deepcopy(before), image_artifacts=[], artifact_readers=[])
        poster.originals(before, after)
        for variant in ('old-row', 'missing-table', 'extra-table', 'new-row'):
            changed = deepcopy(after)
            if variant == 'old-row':
                changed['artifacts'][0]['size_bytes'] += 1
            elif variant == 'missing-table':
                del changed['artifact_readers']
            elif variant == 'extra-table':
                changed['unreviewed'] = []
            else:
                changed['image_artifacts'].append({'id': 'unexpected'})
            with self.subTest(variant=variant), self.assertRaises(RuntimeError):
                poster.originals(before, changed)

    def test_explicit_mode_pins_current_predecessor_and_keeps_old_mode(self):
        args = base.parse_args(['--mode', poster.MODE, '--revision', 'f' * 40, '--output', '/unused'])
        self.assertEqual(args.upgrade_from, poster.BASELINE)
        old = base.parse_args(['--mode', 'external-audio-008-to-009', '--revision', 'f' * 40, '--output', '/unused'])
        self.assertEqual(old.upgrade_from, '106d0d2d7887540768cc21e3bd4e2c71629af6c4')

    def test_group_inventory_keeps_every_existing_hosting_check(self):
        groups = poster.groups(base.GROUPS)
        self.assertEqual(set(groups), set(base.GROUPS) | {'poster_upgrade'})
        for name in set(base.GROUPS) - {'identity'}:
            self.assertEqual(groups[name], base.GROUPS[name])
        self.assertIn('poster_candidate_sources_bound', groups['identity'])
        self.assertNotIn('application_sources_unchanged', groups['identity'])
        self.assertEqual(set(poster.CATALOGS.values()), {9, 10})
        self.assertEqual(len(poster.CATALOGS), 6)

    def test_poster_image_receipt_requires_own_bounded_real_png(self):
        png = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a90sAAAAASUVORK5CYII=')
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'poster-ready.png'
            path.write_bytes(png)
            receipt = dict(path=path.name, bytes=len(png), sha256=proof.digest(png))
            self.assertEqual(poster.poster_image_artifact(directory, receipt), png)
            for patch in (dict(path='../poster-ready.png'), dict(path='borrowed.png'),
                          dict(bytes=True), dict(bytes=16 * 1024 * 1024 + 1),
                          dict(sha256='f' * 64), dict(sha256=None)):
                with self.subTest(patch=patch), self.assertRaises(RuntimeError):
                    poster.poster_image_artifact(directory, dict(receipt, **patch))
            corrupt = bytearray(png)
            corrupt[19] = 2
            path.write_bytes(corrupt)
            with self.assertRaises(RuntimeError):
                poster.poster_image_artifact(directory, dict(receipt, sha256=proof.digest(corrupt)))
            path.unlink()
            target = Path(directory) / 'other.png'
            target.write_bytes(png)
            path.symlink_to(target)
            with self.assertRaises(RuntimeError):
                poster.poster_image_artifact(directory, receipt)

    def test_restored_poster_binds_actual_image_dimensions_parent_and_range(self):
        image = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a90sAAAAASUVORK5CYII=')
        row = dict(id='image', parent_artifact_id='parent', filename='own.png', format='png',
                   size_bytes=len(image), width=1, height=1, timestamp_ms=500, owner='owner')
        metadata = {key: value for key, value in row.items() if key != 'owner'}
        metadata['sha256'] = proof.digest(image)
        receipt = dict(before=metadata, after=deepcopy(metadata), owner_download=200,
                       foreign_download=404, range_bytes=16, range_sha256=proof.digest(image[:16]),
                       decoded_width=1, decoded_height=1)
        retained = dict(image_artifacts=[row], artifacts=[dict(id='parent', owner='owner')])
        poster.validate_restored_poster(receipt, image, retained)
        for variant in ('dimensions', 'decoded', 'parent-owner', 'missing-image', 'range', 'bytes', 'metadata'):
            changed, rows = deepcopy(receipt), deepcopy(retained)
            if variant == 'dimensions':
                for phase in ('before', 'after'):
                    changed[phase]['width'] = 2
                rows['image_artifacts'][0]['width'] = 2
            elif variant == 'decoded':
                changed['decoded_height'] = 2
            elif variant == 'parent-owner':
                rows['artifacts'][0]['owner'] = 'foreign'
            elif variant == 'missing-image':
                rows['image_artifacts'] = []
            elif variant == 'range':
                changed['range_sha256'] = 'f' * 64
            elif variant == 'bytes':
                changed['after']['sha256'] = 'f' * 64
            else:
                rows['image_artifacts'][0]['timestamp_ms'] += 1
            with self.subTest(variant=variant), self.assertRaises(RuntimeError):
                poster.validate_restored_poster(changed, image, rows)

    def test_foreign_and_changed_raw_evidence_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'own.json'
            data = b'{"retained":true}\n'
            path.write_bytes(data)
            receipt = dict(path='own.json', bytes=len(data), sha256=proof.digest(data))
            self.assertEqual(proof.artifact(directory, receipt), data)
            path.write_bytes(b'{"retained":false}\n')
            with self.assertRaises(RuntimeError):
                proof.artifact(directory, receipt)
            with self.assertRaises(RuntimeError):
                proof.artifact(directory, dict(receipt, path='../foreign.json'))


if __name__ == '__main__':
    unittest.main()
