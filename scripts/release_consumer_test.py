"""Tiny real archive controls and mocked crypto; no real signature claim."""
from contextlib import contextmanager
from copy import deepcopy
import ctypes
import gzip
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import stat
import tarfile
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch, Mock
import zipfile
import warnings

import release_test_fixtures as fixture
import release_consumer_process as processes

SPEC = importlib.util.spec_from_file_location('release_consumer', Path(__file__).with_name('release-consumer.py'))
c = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(c)
m, t = c.m, c.t
ROOT = Path(__file__).resolve().parents[1]


def make_fixture(directory):
    """No Git, Go or executables: build five tiny synthetic package fixtures."""
    inputs = {name: (ROOT / name).read_bytes() for name in ('go.sum', 'LICENSE', 'THIRD_PARTY_NOTICES.md')}
    inputs.update({path: b'fixture document\n' for path in m.DOCUMENTS.values() if path not in inputs})
    for path in ('go.mod', 'scripts/release.sh', 'scripts/package-release.py', 'scripts/release_manifest.py', '.github/workflows/release.yml'):
        inputs[path] = b'fixture source\n'
    epoch = 1760000000
    source = {'schema_version': 1, 'mode': 'release', 'publishable': True, 'version': 'v1.2.3', 'commit': '1' * 40,
        'tree': '2' * 40, 'source_epoch': epoch, 'build_time': m.timestamp(epoch), 'toolchain': 'go1.27.2',
        'module': 'github.com/' + t.REPOSITORY,
        'files': {p: {'git_blob': m.digest(raw)[:40], 'mode': '100644', 'sha256': m.digest(raw), 'bytes': len(raw)} for p, raw in inputs.items()}}
    deps = [{'Path': p, 'Version': v, 'Sum': s} for p, v, s in (line.split() for line in inputs['go.sum'].decode().splitlines())
            if (p, v) in t.LICENSES]
    def reader(binary, source, os_, arch, go='go'):
        return dict(fixture.info_for(source, os_, arch), Deps=deepcopy(deps))
    directory.mkdir()
    with tempfile.TemporaryDirectory() as temporary:
        for os_, arch in m.TARGETS:
            stage = Path(temporary) / m.prefix(source, os_, arch)
            stage.mkdir()
            binary = 'cutmy.exe' if os_ == 'windows' else 'cutmy'
            (stage / binary).write_bytes(b'not-an-executable:' + os_.encode() + arch.encode())
            for name, path in m.DOCUMENTS.items():
                (stage / name).write_bytes(inputs[path])
            with patch.object(m, 'binary_info', side_effect=reader):
                fixture.package.package(stage, directory, source, os_, arch, m.digest((stage / binary).read_bytes()))
    with patch.object(m, 'go_identity', return_value=source['toolchain']), patch.object(m, 'binary_info', side_effect=reader):
        base = m.set_manifest(directory, source)
    manifest = dict(base, reproducibility={'builds': 2, 'scope': m.REPRO_SCOPE,
        'first_manifest_sha256': m.digest(m.canonical(base)), 'second_manifest_sha256': m.digest(m.canonical(base))})
    run = t.context(t.REPOSITORY, 123, 1, source['commit'], 456, 'a' * 64)
    verdict = dict(run, schema_version=1, ok=True, source_commit=source['commit'], source_tree=source['tree'],
        source_sha256=m.digest(m.canonical(source)), manifest_sha256=m.digest(m.canonical(manifest)), mode='release', publishable=True,
        native=[{'os': a['os'], 'arch': a['arch'], 'archive': {k: a[k] for k in ('name', 'sha256', 'bytes')},
            'binary_sha256': a['contents']['cutmy.exe' if a['os'] == 'windows' else 'cutmy']['sha256'],
            'required_groups': 7, 'required_checks': 51 if a['os'] == 'windows' else 53, 'report_sha256': 'b' * 64} for a in manifest['archives']],
        linux_archive_recovery={'required_groups': 9, 'decoded_media_observations': 11,
            'schema_versions': ['20261005-storage-queue-v3', '20261010-cache-retention-v1',
                                '20261010-ordered-migrations-v1', '20261010-storage-maintenance-v1',
                                '20261011-source-retention-v1', '20261012-source-media-cleanup-v1', '20261014-metadata-resolution-leases-v1', '20261016-agent-projects-v1'],
            'binary_sha256': manifest['archives'][0]['contents']['cutmy']['sha256'], 'report_sha256': 'c' * 64})
    sbom = t.spdx(source, manifest, go_sum=inputs['go.sum'], notices=inputs['THIRD_PARTY_NOTICES.md'], project_license=inputs['LICENSE'])
    trust = t.make_trust(source, manifest, sbom, verdict, run)
    for name, value in (('SOURCE.json', source), ('MANIFEST.json', manifest), ('SBOM.spdx.json', sbom), ('TRUST.json', trust)):
        (directory / name).write_bytes(m.canonical(value))
    (directory / 'SHA256SUMS').write_bytes(m.checksums(manifest))
    for name in ('provenance.sigstore.json', 'sbom.sigstore.json'):
        (directory / name).write_bytes(b'{"synthetic_bundle":"NOT A SIGNATURE"}\n')
    record = {'schema_version': 1, 'repository': t.REPOSITORY, 'signer_workflow': t.WORKFLOW,
        'source_commit': source['commit'], 'source_ref': 'refs/tags/' + source['version'], 'source_sha256': trust['source_sha256'],
        'trust_sha256': c.file_hash(directory / 'TRUST.json'), 'sbom_sha256': trust['sbom_sha256'],
        'provenance_subjects': t.subjects(trust, c.file_hash(directory / 'TRUST.json')), 'sbom_subjects': t.subjects(trust, '', sbom_only=True),
        'bundle_sha256': {n: c.file_hash(directory / n) for n in ('provenance.sigstore.json', 'sbom.sigstore.json')},
        'actual_signature_verification': True, 'scope': c.RECEIPT_SCOPE}
    (directory / 'ATTESTATIONS.json').write_bytes(m.canonical(record))
    return source, manifest, sbom, trust, record


class ConsumerControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix='cutmy-consumer-controls-')
        cls.root = Path(cls.temporary.name)
        cls.original = cls.root / 'fixture'
        cls.objects = make_fixture(cls.original)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        self.directory = self.root / self.id().rsplit('.', 1)[-1]
        shutil.copytree(self.original, self.directory)
        self.source, self.manifest, self.sbom, self.trust, self.receipt = deepcopy(self.objects)
        self.calls, self.crypto_calls = [], 0

    def response(self, predicate):
        subjects = self.receipt['sbom_subjects' if predicate == t.SPDX_PREDICATE else 'provenance_subjects']
        repository, ref = 'https://github.com/' + t.REPOSITORY, 'refs/tags/v1.2.3'
        provenance = {'buildDefinition': {'buildType': 'https://actions.github.io/buildtypes/workflow/v1',
            'externalParameters': {'workflow': {'ref': ref, 'repository': repository, 'path': '.github/workflows/release.yml'}},
            'internalParameters': {'github': {'event_name': 'push', 'runner_environment': 'github-hosted'}},
            'resolvedDependencies': [{'uri': 'git+' + repository + '@' + ref, 'digest': {'gitCommit': self.source['commit']}}]},
            'runDetails': {'builder': {'id': 'https://github.com/' + t.WORKFLOW + '@' + ref},
                'metadata': {'invocationId': repository + '/actions/runs/123/attempts/1'}}}
        return [{'attestation': {'fixture': 'MOCKED CRYPTO'}, 'verificationResult': {
            'mediaType': 'application/vnd.dev.sigstore.verificationresult+json;version=0.1',
            'signature': {'certificate': {'certificateIssuer': 'MOCK', 'subjectAlternativeName': 'MOCK',
                'runInvocationURI': 'https://github.com/' + t.REPOSITORY + '/actions/runs/123/attempts/1'}},
            'verifiedTimestamps': [{'type': 'MOCK', 'uri': 'https://example.invalid', 'timestamp': '2026-10-11T00:00:00Z'}],
            'statement': {'_type': 'https://in-toto.io/Statement/v1',
                'subject': [{'name': n, 'digest': {'sha256': v}} for n, v in subjects.items()],
                'predicateType': predicate, 'predicate': self.sbom if predicate == t.SPDX_PREDICATE else provenance}}}]

    def output(self, argv, limit, deadline=None):
        self.calls.append(argv)
        if argv[0] == 'git':
            return (self.source['commit'] + '\trefs/tags/v1.2.3\n').encode()
        self.crypto_calls += 1
        return json.dumps(self.response(argv[argv.index('--predicate-type') + 1])).encode()

    def verify(self, output=None):
        with patch.object(c, 'command_output', side_effect=output or self.output):
            return c.verify(self.directory, '1' * 40, 'v1.2.3')

    def rejects(self, output=None):
        with self.assertRaises((ValueError, OSError, KeyError, TypeError, AttributeError, subprocess.SubprocessError)):
            self.verify(output)

    def write(self, name, value):
        (self.directory / name).write_bytes(m.canonical(value))

    def rebind_fixture(self):
        """Mock a signer accepting malformed bytes; archive policy must still refuse."""
        for row in self.manifest['archives']:
            row['sha256'] = c.file_hash(self.directory / row['name'])
            row['bytes'] = (self.directory / row['name']).stat().st_size
        self.write('MANIFEST.json', self.manifest)
        self.sbom = t.spdx(self.source, self.manifest, go_sum=(ROOT / 'go.sum').read_bytes(),
            notices=(ROOT / 'THIRD_PARTY_NOTICES.md').read_bytes(), project_license=(ROOT / 'LICENSE').read_bytes())
        self.write('SBOM.spdx.json', self.sbom)
        self.trust.update(manifest_sha256=c.file_hash(self.directory / 'MANIFEST.json'), sbom_sha256=c.file_hash(self.directory / 'SBOM.spdx.json'),
            archives=sorted([{k: r[k] for k in ('name', 'os', 'arch', 'sha256', 'bytes')} for r in self.manifest['archives']], key=lambda r: r['name']))
        self.write('TRUST.json', self.trust)
        self.receipt.update(trust_sha256=c.file_hash(self.directory / 'TRUST.json'), sbom_sha256=self.trust['sbom_sha256'],
            provenance_subjects=t.subjects(self.trust, c.file_hash(self.directory / 'TRUST.json')), sbom_subjects=t.subjects(self.trust, '', sbom_only=True))
        self.write('ATTESTATIONS.json', self.receipt)
        (self.directory / 'SHA256SUMS').write_bytes(m.checksums(self.manifest))

    def test_exact13_12_fixed_crypto_policies_and_two_live_tag_boundaries(self):
        with patch.object(m, 'binary_info', side_effect=AssertionError('consumer must not require Go')), \
                patch.object(m, 'git', side_effect=AssertionError('consumer must not require checkout')):
            result = self.verify()
        self.assertTrue(result['ok'])
        self.assertEqual(len(result['assets']), 13)
        self.assertEqual(self.crypto_calls, 12)
        self.assertEqual([a[0] for a in self.calls], ['git'] + ['gh'] * 12 + ['git'])
        for argv in self.calls[1:-1]:
            for flag, value in (('--hostname', 'github.com'), ('--repo', t.REPOSITORY), ('--signer-workflow', t.WORKFLOW),
                                ('--source-digest', '1' * 40), ('--signer-digest', '1' * 40), ('--source-ref', 'refs/tags/v1.2.3'), ('--format', 'json')):
                self.assertEqual(argv[argv.index(flag) + 1], value)
            self.assertIn('--bundle', argv)
            self.assertIn('--deny-self-hosted-runners', argv)
            self.assertNotEqual(Path(argv[3]).parent, self.directory)
        self.assertIn('publisher claims', result['metadata_scope'])

    def test_each_of_twelve_crypto_failures_refuses_despite_true_receipt(self):
        for failure in range(1, 13):
            self.crypto_calls = 0
            def failing(argv, limit, deadline=None):
                raw = self.output(argv, limit, deadline)
                if argv[0] == 'gh' and self.crypto_calls == failure:
                    raise subprocess.CalledProcessError(1, argv)
                return raw
            self.rejects(failing)

    def test_wrong_reviewed_commit_version_and_invalid_cli_before_network(self):
        for commit, version in (('HEAD', 'v1.2.3'), ('1' * 12, 'v1.2.3'), ('1' * 40, 'v01.2.3'),
                                ('1' * 40, 'v1.2.3-01'), ('1' * 40, 'v0.0.0-candidate.g' + '1' * 40)):
            with patch.object(c, 'command_output', side_effect=AssertionError('unexpected network')):
                with self.assertRaises(ValueError):
                    c.verify(self.directory, commit, version)
        with patch.object(c, 'command_output', side_effect=AssertionError('unexpected network')):
            with patch('sys.stderr', io.StringIO()):
                self.assertEqual(c.main(['--directory', str(self.directory), '--expected-commit', 'HEAD', '--version', 'v1.2.3']), 1)
        with patch('sys.stderr', io.StringIO()), self.assertRaises(SystemExit):
            c.main(['--dir', str(self.directory), '--expected-commit', '1' * 40, '--version', 'v1.2.3'])

    def test_malformed_missing_ambiguous_and_wrong_tag(self):
        ref = '\trefs/tags/v1.2.3\n'
        for raw in (b'', b'bad tag\n', ('1' * 40 + ref + '1' * 40 + ref).encode(),
                    ('2' * 40 + ref).encode(), ('1' * 40 + '\trefs/tags/other\n').encode(), b'\xff'):
            self.rejects(lambda argv, limit, deadline=None: raw)

    def test_annotated_tag_and_reannotation_same_commit(self):
        def annotated(argv, limit, deadline=None):
            if argv[0] == 'git':
                return ('3' * 40 + '\trefs/tags/v1.2.3\n' + '1' * 40 + '\trefs/tags/v1.2.3^{}\n').encode()
            return self.output(argv, limit, deadline)
        self.assertTrue(self.verify(annotated)['ok'])
        count = 0
        def moved(argv, limit, deadline=None):
            nonlocal count
            if argv[0] == 'git':
                count += 1
                return (('3' if count == 1 else '4') * 40 + '\trefs/tags/v1.2.3\n' + '1' * 40 + '\trefs/tags/v1.2.3^{}\n').encode()
            return self.output(argv, limit, deadline)
        self.rejects(moved)

    def test_moved_deleted_and_unavailable_tag_after_crypto(self):
        for mode in ('moved', 'deleted', 'unavailable'):
            count = 0
            def moved(argv, limit, deadline=None):
                nonlocal count
                if argv[0] == 'git':
                    count += 1
                    if count == 2:
                        if mode == 'unavailable':
                            raise subprocess.CalledProcessError(1, argv)
                        return b'' if mode == 'deleted' else ('2' * 40 + '\trefs/tags/v1.2.3\n').encode()
                return self.output(argv, limit, deadline)
            self.rejects(moved)

    def test_missing_extra_and_nonregular_assets(self):
        path = self.directory / 'ATTESTATIONS.json'
        raw = path.read_bytes()
        path.unlink()
        self.rejects()
        path.mkdir()
        self.rejects()
        path.rmdir()
        path.write_bytes(raw)
        (self.directory / 'unexpected').write_bytes(b'x')
        self.rejects()

    def test_symlink_asset_or_equivalent_nonregular_refusal(self):
        path = self.directory / 'TRUST.json'
        path.unlink()
        try:
            path.symlink_to(self.original / 'TRUST.json')
        except (OSError, NotImplementedError):
            # Windows without symlink privilege still exercises a nonregular
            # asset. Actual symlink creation is reported as unavailable.
            path.mkdir()
            print('Consumer symlink fixture unavailable; directory refusal exercised.')
        self.rejects()

    def test_noncanonical_duplicate_keys_numbers_and_malformed_controls(self):
        path = self.directory / 'TRUST.json'
        original = path.read_bytes()
        for raw in (b'{"a":1,"a":2}', b'{"a":NaN}', b'{"a":Infinity}', b'{"a":1.0}', b'{"a":1e9}',
                    b'null', b'[]', b'\xff', b'{', json.dumps(self.trust).encode()):
            path.write_bytes(raw)
            self.rejects()
        path.write_bytes(original)
        for name, value in (('SOURCE.json', {'schema_version': True}), ('MANIFEST.json', {'archives': None}), ('SBOM.spdx.json', {'packages': 'wrong'})):
            self.write(name, value)
            self.rejects()

    def test_strict_json_size_depth_and_integer_bound(self):
        for raw, limit in ((b'{}', 1), (b'[]', 1), (b'[' * 66 + b'0' + b']' * 66, 1000),
                           (b'{"x":9223372036854775808}', 1000)):
            with self.assertRaises(ValueError):
                c.plain_json(raw, limit)

    def test_unsigned_candidate_legacy_and_mixed_source_identity(self):
        for changes in ({'mode': 'candidate', 'publishable': False, 'version': 'v0.0.0-candidate.g' + '1' * 40},
                        {'commit': '2' * 40}, {'version': 'v1.2.4'}, {'module': 'example.invalid/other'}):
            source = dict(self.source, **changes)
            self.write('SOURCE.json', source)
            self.rejects()
        self.write('SOURCE.json', self.source)
        (self.directory / 'TRUST.json').unlink()
        self.rejects()

    def test_trust_run_repository_workflow_source_and_manifest_bindings(self):
        for key, value in (('repository', 'other/repo'), ('signer_workflow', 'other/workflow'), ('source_commit', '2' * 40),
                           ('source_sha256', 'f' * 64), ('manifest_sha256', 'f' * 64), ('sbom_sha256', 'f' * 64),
                           ('verdict_sha256', 'not-a-hash'), ('publishable', 1), ('schema_version', True)):
            self.write('TRUST.json', dict(self.trust, **{key: value}))
            self.rejects()
        for key, value in (('event_sha', '2' * 40), ('run_id', True), ('run_attempt', 0), ('artifact_sha256', 'bad')):
            trust = deepcopy(self.trust); trust['archive_run'][key] = value
            self.write('TRUST.json', trust)
            self.rejects()

    def test_receipt_duplicate_subject_bundle_digest_and_boolean_cannot_authenticate(self):
        for key, value in (('schema_version', True), ('actual_signature_verification', 1), ('actual_signature_verification', False),
                           ('trust_sha256', 'f' * 64), ('source_commit', '2' * 40), ('bundle_sha256', {})):
            self.write('ATTESTATIONS.json', dict(self.receipt, **{key: value}))
            self.rejects()
        self.write('ATTESTATIONS.json', self.receipt)
        path = self.directory / 'ATTESTATIONS.json'
        path.write_bytes(path.read_bytes().replace(b'"schema_version": 1,', b'"schema_version": 1, "schema_version": 1,'))
        self.rejects()

    def test_malformed_ambiguous_duplicate_subject_and_wrong_predicate_crypto_output(self):
        bad = [b'[]', b'null', b'{}', b'NaN', b'\xff', b'{', b'[{"verificationResult":{}}]',
               json.dumps(self.response(t.PROVENANCE) * 2).encode()]
        for mutate in (lambda s: s['subject'].append(deepcopy(s['subject'][0])), lambda s: s['subject'].pop(),
                       lambda s: s.update(predicateType='wrong'), lambda s: s.update(predicate=[]),
                       lambda s: s.update(_type='wrong'), lambda s: s['subject'][0]['digest'].update(sha256='f' * 64)):
            response = self.response(t.PROVENANCE)
            mutate(response[0]['verificationResult']['statement'])
            bad.append(json.dumps(response).encode())
        for raw in bad:
            self.rejects(lambda argv, limit, deadline=None: self.output(argv, limit, deadline) if argv[0] == 'git' else raw)

    def test_spdx_crypto_predicate_must_match_exact_sidecar(self):
        def wrong(argv, limit, deadline=None):
            raw = self.output(argv, limit, deadline)
            if argv[0] == 'gh' and argv[argv.index('--predicate-type') + 1] == t.SPDX_PREDICATE:
                response = json.loads(raw)
                response[0]['verificationResult']['statement']['predicate']['name'] = 'mixed SBOM'
                return json.dumps(response).encode()
            return raw
        self.rejects(wrong)

    def test_provenance_claims_cannot_mix_gate_run_attempt_workflow_source_and_event(self):
        for mutation in (lambda p: p['runDetails']['metadata'].update(invocationId='https://github.com/' + t.REPOSITORY + '/actions/runs/999/attempts/1'),
                         lambda p: p['runDetails']['builder'].update(id='other builder'),
                         lambda p: p['buildDefinition']['externalParameters']['workflow'].update(ref='refs/heads/main'),
                         lambda p: p['buildDefinition']['resolvedDependencies'][0]['digest'].update(gitCommit='2' * 40),
                         lambda p: p['buildDefinition']['internalParameters']['github'].update(event_name='workflow_dispatch')):
            def wrong(argv, limit, deadline=None):
                raw = self.output(argv, limit, deadline)
                if argv[0] == 'gh':
                    response = json.loads(raw)
                    mutation(response[0]['verificationResult']['statement']['predicate'])
                    return json.dumps(response).encode()
                return raw
            self.rejects(wrong)

    def test_tampered_archive_checksum_list_bundle_and_download_changed_during_calls(self):
        for name in (self.manifest['archives'][0]['name'], 'SHA256SUMS', 'provenance.sigstore.json', 'sbom.sigstore.json'):
            path = self.directory / name
            original = path.read_bytes()
            path.write_bytes(original + b'tampered')
            self.rejects()
            path.write_bytes(original)
        self.crypto_calls = 0
        def changed(argv, limit, deadline=None):
            raw = self.output(argv, limit, deadline)
            if argv[0] == 'gh' and self.crypto_calls == 1:
                (self.directory / 'SHA256SUMS').write_bytes(b'changed original')
            return raw
        self.rejects(changed)

    def test_unsafe_archives_fail_even_if_recorded_checksum_is_consistent(self):
        path = self.directory / m.archive_name(self.source, 'linux', 'amd64')
        contents = m.read_archive(path, self.source, 'linux', 'amd64')
        for mode in ('traversal', 'duplicate', 'symlink', 'oversized_control'):
            with path.open('wb') as out, gzip.GzipFile(filename='', fileobj=out, mode='wb', mtime=self.source['source_epoch']) as compressed:
                with tarfile.open(fileobj=compressed, mode='w', format=tarfile.PAX_FORMAT) as tar:
                    for index, (name, raw) in enumerate(sorted(contents.items())):
                        member = tarfile.TarInfo(m.prefix(self.source, 'linux', 'amd64') + '/' + name)
                        member.size, member.mtime = len(raw), self.source['source_epoch']
                        member.mode = 0o755 if name == 'cutmy' else 0o644
                        if index == 0:
                            if mode == 'traversal':
                                member.name = '../outside'
                            elif mode == 'symlink':
                                member.type, member.linkname, member.size = tarfile.SYMTYPE, '../../outside', 0
                            elif mode == 'oversized_control':
                                member.pax_headers = {'path': 'x' * (m.MAX_CONTROL_HEADER + 1)}
                        tar.addfile(member, io.BytesIO(raw) if member.isfile() else None)
                        if index == 0 and mode == 'duplicate':
                            tar.addfile(member, io.BytesIO(raw))
            self.rebind_fixture()
            self.rejects()
        windows = self.directory / m.archive_name(self.source, 'windows', 'amd64')
        original = windows.read_bytes()
        for mode in ('traversal', 'duplicate', 'symlink'):
            with warnings.catch_warnings(), zipfile.ZipFile(io.BytesIO(original)) as source, zipfile.ZipFile(windows, 'w') as out:
                warnings.simplefilter('ignore', UserWarning)
                for index, info in enumerate(source.infolist()):
                    raw = source.read(info)
                    if index == 0:
                        if mode == 'traversal':
                            info.filename = '../outside'
                        elif mode == 'symlink':
                            info.external_attr = (stat.S_IFLNK | 0o644) << 16
                        elif mode == 'duplicate':
                            info.filename = source.infolist()[1].filename
                    out.writestr(info, raw)
            # Restore a valid Linux archive so the actual Windows parser runs.
            shutil.copyfile(self.original / path.name, path)
            self.rebind_fixture()
            self.rejects()

    def test_malformed_certificate_timestamp_and_finite_but_nonplain_crypto_metadata(self):
        for mutation in (lambda v: v.pop('signature'), lambda v: v.update(mediaType='wrong'),
                         lambda v: v.update(verifiedTimestamps=[]), lambda v: v['signature'].update(certificate=[]),
                         lambda v: v['verifiedTimestamps'][0].update(timestamp=1.5)):
            def wrong(argv, limit, deadline=None):
                raw = self.output(argv, limit, deadline)
                if argv[0] == 'gh':
                    response = json.loads(raw)
                    mutation(response[0]['verificationResult'])
                    return json.dumps(response).encode()
                return raw
            self.rejects(wrong)

    def test_official_cli_model_string_certificates_timestamps_and_extra_metadata(self):
        # CLI v2.89.0 APR + pinned sigstore-go v1.1.4 Go JSON types were
        # separately read/hash-retained. This is their model, not signed output.
        def modeled(argv, limit, deadline=None):
            raw = self.output(argv, limit, deadline)
            if argv[0] == 'gh':
                response = json.loads(raw)
                certificate = response[0]['verificationResult']['signature']['certificate']
                certificate.update(issuer='https://token.actions.githubusercontent.com',
                    buildSignerURI='https://github.com/' + t.WORKFLOW + '@refs/tags/v1.2.3', buildSignerDigest='1' * 40,
                    runnerEnvironment='github-hosted', sourceRepositoryURI='https://github.com/' + t.REPOSITORY,
                    sourceRepositoryDigest='1' * 40, sourceRepositoryRef='refs/tags/v1.2.3', sourceRepositoryIdentifier='123456',
                    sourceRepositoryOwnerURI='https://github.com/Kay0k1', sourceRepositoryOwnerIdentifier='654321',
                    buildConfigURI='https://github.com/' + t.WORKFLOW + '@refs/tags/v1.2.3', buildConfigDigest='1' * 40,
                    buildTrigger='push', runInvocationURI='https://github.com/' + t.REPOSITORY + '/actions/runs/123/attempts/1',
                    sourceRepositoryVisibilityAtSigning='public')
                response[0]['verificationResult']['verifiedIdentity'] = {'subjectAlternativeName': certificate['buildSignerURI'], 'issuer': certificate['issuer']}
                return json.dumps(response).encode()
            return raw
        self.assertTrue(self.verify(modeled)['ok'])

    def test_certificate_run_extension_required_for_every_provenance_and_spdx_subject(self):
        for target in range(1, 13):
            for value in (None, 123, 'https://github.com/' + t.REPOSITORY + '/actions/runs/999/attempts/1',
                          'https://github.com/' + t.REPOSITORY + '/actions/runs/123/attempts/2'):
                self.crypto_calls = 0
                def wrong(argv, limit, deadline=None):
                    raw = self.output(argv, limit, deadline)
                    if argv[0] == 'gh' and self.crypto_calls == target:
                        response = json.loads(raw)
                        certificate = response[0]['verificationResult']['signature']['certificate']
                        if value is None:
                            certificate.pop('runInvocationURI')
                        else:
                            certificate['runInvocationURI'] = value
                        return json.dumps(response).encode()
                    return raw
                self.rejects(wrong)

    def test_missing_tool_failure_is_fixed_and_does_not_echo_exception(self):
        with patch.object(c, 'bounded_output', side_effect=FileNotFoundError('SECRET should not appear')):
            with self.assertRaises(c.Refused) as caught:
                c.remote_tag('v1.2.3', '1' * 40)
        self.assertEqual(caught.exception.code, 'tool_unavailable')
        self.assertNotIn('SECRET', str(caught.exception))

    def test_recorded_metadata_and_spdx_graph_inconsistency(self):
        notices = (ROOT / 'THIRD_PARTY_NOTICES.md').read_bytes()
        license_bytes = (ROOT / 'LICENSE').read_bytes()
        for mutate in (lambda s: s['files'][0]['checksums'][0].update(checksumValue='f' * 64),
                       lambda s: s['packages'][0].update(versionInfo='wrong'), lambda s: s['relationships'].pop(),
                       lambda s: s.update(documentNamespace='https://wrong'), lambda s: s['documentDescribes'].append(s['documentDescribes'][0])):
            changed = deepcopy(self.sbom); mutate(changed)
            with self.assertRaises(ValueError):
                c.sbom_graph(self.source, self.manifest, changed, notices, license_bytes)
        for mutate in (lambda i: i['Main'].update(Version='v1.2.3'), lambda i: i['Deps'].append(deepcopy(i['Deps'][0])),
                       lambda i: i['Settings'].append(deepcopy(i['Settings'][0])), lambda i: i['Settings'][0].update(Value=1)):
            changed = deepcopy(self.manifest['archives'][0]['go_build_info']); mutate(changed)
            with self.assertRaises(ValueError):
                c.recorded_info(changed, self.source, 'linux', 'amd64')

    def test_failure_cli_is_stable_no_traceback_or_success_json(self):
        for raw in (b'null', b'{"files":1}', b'[]', b'NaN', b'\xff'):
            (self.directory / 'SOURCE.json').write_bytes(raw)
            out, err = io.StringIO(), io.StringIO()
            with patch.object(c, 'command_output', side_effect=self.output), patch('sys.stdout', out), patch('sys.stderr', err):
                self.assertEqual(c.main(['--directory', str(self.directory), '--expected-commit', '1' * 40, '--version', 'v1.2.3']), 1)
            self.assertEqual(out.getvalue(), '')
            self.assertEqual(err.getvalue(), 'Release verification refused: metadata. See docs/releases.md.\n')


class CommandControls(unittest.TestCase):
    @staticmethod
    def windows_api(active=(0,), events=None):
        events = [] if events is None else events
        api = Mock()
        api.CreateJobObjectW.return_value = 123
        api.SetInformationJobObject.return_value = 1
        api.AssignProcessToJobObject.return_value = 1
        api.OpenThread.return_value = 456
        api.ResumeThread.return_value = 1
        states = iter(active)
        def terminate(handle, code):
            events.append(('terminate', handle, code))
            return 1
        def query(handle, kind, data, size, length):
            count = next(states)
            ctypes.cast(data, ctypes.POINTER(processes.BasicAccounting)).contents.active = count
            ctypes.cast(length, ctypes.POINTER(ctypes.c_uint32)).contents.value = size
            events.append(('active', count))
            return 1
        def close(handle):
            events.append(('job_close', handle))
            return 1
        api.TerminateJobObject.side_effect = terminate
        api.QueryInformationJobObject.side_effect = query
        api.CloseHandle.side_effect = close
        return api

    def test_watchdog_timeout_flood_nonzero_and_output_bound_with_portable_mocks(self):
        for mode in ('timeout', 'flood', 'nonzero', 'success'):
            child = Mock(pid=123456)
            child.poll.return_value = None if mode == 'timeout' else (1 if mode == 'nonzero' else 0)
            def start(argv, **kwargs):
                kwargs['stdout'].write(b'a' * (100 if mode == 'flood' else 2))
                kwargs['stdout'].flush()
                return child
            with patch.object(processes.subprocess, 'Popen', side_effect=start), patch.object(processes.os, 'killpg', create=True) as kill, \
                    patch.object(processes.os, 'name', 'posix'), patch.object(processes.signal, 'SIGKILL', 9, create=True), \
                    patch.object(processes.time, 'monotonic', side_effect=[0, 0, 2] if mode == 'timeout' else [0, 0, 0]):
                if mode == 'success':
                    self.assertEqual(processes.output(['mock'], 10, deadline=1), b'aa')
                else:
                    with self.assertRaises(ValueError):
                        processes.output(['mock'], 10, deadline=1)
                kill.assert_called_once_with(child.pid, 9)
                if mode == 'timeout':
                    child.kill.assert_called_once()
                    child.wait.assert_called_once_with(timeout=5)

    def test_windows_api_pointer_layout_suspend_attach_resume_and_close(self):
        self.assertEqual(ctypes.sizeof(processes.ThreadEntry), 28)
        self.assertEqual(ctypes.sizeof(processes.BasicAccounting), 48)
        self.assertEqual(processes.BasicAccounting.active.offset, 40)
        if ctypes.sizeof(ctypes.c_void_p) == 8:
            self.assertEqual(ctypes.sizeof(processes.ExtendedLimits), 144)
        api = self.windows_api()
        job = processes.WindowsJob(api)
        with patch.object(job, 'initial_thread', return_value=789):
            job.attach_and_resume(SimpleNamespace(pid=99, _handle=101))
        api.AssignProcessToJobObject.assert_called_once_with(123, 101)
        api.OpenThread.assert_called_once_with(2, False, 789)
        api.ResumeThread.assert_called_once_with(456)
        job.close()
        self.assertIsNone(job.handle)

    def test_windows_attach_resume_and_configuration_failure_refuse(self):
        for failure in ('SetInformationJobObject', 'AssignProcessToJobObject', 'ResumeThread'):
            api = self.windows_api()
            getattr(api, failure).return_value = 0
            with self.assertRaises(ValueError):
                job = processes.WindowsJob(api)
                try:
                    with patch.object(job, 'initial_thread', return_value=789):
                        job.attach_and_resume(SimpleNamespace(pid=99, _handle=101))
                finally:
                    job.close()

    def test_windows_async_completion_releases_parent_before_owned_job_zero(self):
        events, clock = [], [0.0]
        api = self.windows_api((2, 1, 0), events)
        child = Mock()
        child.poll.side_effect = lambda: events.append(('parent_poll',)) or None
        child.kill.side_effect = lambda: events.append(('parent_kill',))
        child.wait.side_effect = lambda **kwargs: events.append(('parent_wait', kwargs['timeout'])) or 1
        child._handle.Close.side_effect = lambda: events.append(('parent_handle_close',))
        def sleep(delay):
            events.append(('sleep', delay))
            clock[0] += delay
        job = processes.WindowsJob(api)
        with patch.object(processes.time, 'monotonic', side_effect=lambda: clock[0]), \
                patch.object(processes.time, 'sleep', side_effect=sleep):
            job.close(child)
            job.close(child)  # Closing again must not touch any foreign/reused handle.
        self.assertEqual(events, [('terminate', 123, 1), ('parent_poll',), ('parent_kill',),
                                  ('parent_wait', 5.0), ('parent_handle_close',), ('active', 2), ('sleep', 0.02),
                                  ('active', 1), ('sleep', 0.02), ('active', 0), ('job_close', 123)])
        self.assertIsNone(job.handle)
        api.QueryInformationJobObject.assert_has_calls([
            unittest.mock.call(123, 1, unittest.mock.ANY, 48, unittest.mock.ANY)] * 3)

    def test_windows_failed_termination_accounting_and_close_never_accept_output(self):
        for mode in ('terminate', 'terminate_oserror', 'query', 'query_oserror', 'short_accounting', 'close', 'close_oserror'):
            events = []
            api = self.windows_api(events=events)
            if mode == 'terminate':
                api.TerminateJobObject.side_effect = None
                api.TerminateJobObject.return_value = 0
            elif mode == 'terminate_oserror':
                api.TerminateJobObject.side_effect = OSError('SECRET kernel path')
            elif mode == 'query':
                api.QueryInformationJobObject.side_effect = None
                api.QueryInformationJobObject.return_value = 0
            elif mode == 'query_oserror':
                api.QueryInformationJobObject.side_effect = OSError('SECRET kernel path')
            elif mode == 'short_accounting':
                api.QueryInformationJobObject.side_effect = lambda *args: 1  # Zero-filled data is not a valid receipt.
            elif mode == 'close':
                api.CloseHandle.side_effect = None
                api.CloseHandle.return_value = 0
            else:
                api.CloseHandle.side_effect = OSError('SECRET kernel path')
            child = Mock()
            child.poll.return_value = 0
            job = processes.WindowsJob(api)
            with patch.object(processes.time, 'monotonic', return_value=0):
                with self.assertRaises(processes.CommandRefused) as caught:
                    job.close(child)
            self.assertEqual(str(caught.exception), 'Verification command or process containment failed.')
            child.wait.assert_called_once_with(timeout=5)
            child._handle.Close.assert_called_once_with()
            api.CloseHandle.assert_called_once_with(123)
            self.assertIsNone(job.handle)

    def test_windows_completion_budget_and_parent_wait_failure_remain_refusals(self):
        for mode in ('active_timeout', 'parent_timeout', 'parent_oserror'):
            events, clock = [], [0.0]
            api = self.windows_api((1,), events)
            child = Mock()
            child.poll.return_value = None
            if mode == 'parent_timeout':
                child.wait.side_effect = subprocess.TimeoutExpired(['SECRET argument'], 5)
            elif mode == 'parent_oserror':
                child.wait.side_effect = OSError('SECRET private path')
            def sleep(delay):
                clock[0] = 5.0
            job = processes.WindowsJob(api)
            with patch.object(processes.time, 'monotonic', side_effect=lambda: clock[0]), \
                    patch.object(processes.time, 'sleep', side_effect=sleep) as sleeping:
                with self.assertRaises(processes.CommandRefused) as caught:
                    job.close(child)
            self.assertNotIn('SECRET', str(caught.exception))
            child.kill.assert_called_once_with()
            child.wait.assert_called_once_with(timeout=5)
            if mode == 'active_timeout':
                child._handle.Close.assert_called_once_with()
                sleeping.assert_called_once_with(0.02)
            else:
                child._handle.Close.assert_not_called()
                api.QueryInformationJobObject.assert_not_called()
            api.CloseHandle.assert_called_once_with(123)

    def test_windows_output_and_private_cleanup_order_and_fixed_failure(self):
        for mode in ('success', 'timeout', 'attach_failure'):
            events, clock = [], [0.0]
            api = self.windows_api((1, 0), events)
            job = processes.WindowsJob(api)
            child = Mock(pid=99)
            child.poll.return_value = 0 if mode == 'success' else None
            child.kill.side_effect = lambda: events.append(('parent_kill',))
            child.wait.side_effect = lambda **kwargs: events.append(('parent_wait',)) or 0
            child._handle.Close.side_effect = lambda: events.append(('parent_handle_close',))
            stream = Mock()
            stream.fileno.return_value = 7
            stream.read.side_effect = lambda count: events.append(('read',)) or b'aa'
            @contextmanager
            def private():
                try:
                    yield '/private/empty', stream
                finally:
                    events.append(('private_cleanup',))
            def sleep(delay):
                clock[0] += 2 if mode == 'timeout' and clock[0] == 0 else delay
            with patch.object(processes, 'private_output', side_effect=private), \
                    patch.object(processes, 'WindowsJob', return_value=job), \
                    patch.object(job, 'attach_and_resume', side_effect=processes.CommandRefused('fixed')
                                 if mode == 'attach_failure' else None) as attached, patch.object(processes.os, 'name', 'nt'), \
                    patch.object(processes.subprocess, 'Popen', return_value=child) as start, \
                    patch.object(processes.os, 'fstat', return_value=SimpleNamespace(st_size=2)), \
                    patch.object(processes.time, 'monotonic', side_effect=lambda: clock[0]), \
                    patch.object(processes.time, 'sleep', side_effect=sleep):
                if mode != 'success':
                    with self.assertRaises(processes.CommandRefused):
                        processes.output(['fixed', 'argument'], 10, deadline=1)
                else:
                    self.assertEqual(processes.output(['fixed', 'argument'], 10, deadline=1), b'aa')
            self.assertEqual(start.call_args.kwargs['creationflags'], 4)
            self.assertNotIn('start_new_session', start.call_args.kwargs)
            self.assertEqual(start.call_args.kwargs['cwd'], '/private/empty')
            attached.assert_called_once_with(child)
            self.assertLess(events.index(('parent_handle_close',)), events.index(('active', 0)))
            self.assertLess(events.index(('active', 0)), events.index(('job_close', 123)))
            if mode != 'success':
                stream.read.assert_not_called()
                child.kill.assert_called_once_with()
            else:
                self.assertLess(events.index(('job_close', 123)), events.index(('read',)))
            self.assertEqual(events[-1], ('private_cleanup',))
        directory = Mock(name='/SECRET/private')
        directory.name = '/SECRET/private'
        directory.cleanup.side_effect = PermissionError('SECRET locked path')
        with patch.object(processes.tempfile, 'TemporaryDirectory', return_value=directory), \
                patch.object(processes.tempfile, 'TemporaryFile'):
            with self.assertRaises(processes.CommandRefused) as caught:
                with processes.private_output():
                    pass
        self.assertNotIn('SECRET', str(caught.exception))
        directory.cleanup.side_effect = None
        with patch.object(processes.tempfile, 'TemporaryDirectory', return_value=directory), \
                patch.object(processes.tempfile, 'TemporaryFile'):
            with self.assertRaises(FileNotFoundError):
                with processes.private_output():
                    raise FileNotFoundError('SECRET tool path')


if __name__ == '__main__':
    unittest.main()
