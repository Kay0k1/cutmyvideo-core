"""Trust controls use synthetic executables/signatures, never prove signing."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import release_test_fixtures as fixture

SPEC = importlib.util.spec_from_file_location('release_trust', Path(__file__).with_name('release-trust.py'))
t = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(t)
m = t.m
ROOT = Path(__file__).resolve().parents[1]
NATIVE_RECEIPT_COUNTS = {('linux', 'amd64'): 53, ('linux', 'arm64'): 53,
                         ('darwin', 'amd64'): 53, ('darwin', 'arm64'): 53, ('windows', 'amd64'): 51}


class TrustControls(unittest.TestCase):
    def setUp(self):
        self.fixture = fixture.ReleaseFixture()
        self.fixture.initialize(self.addCleanup)
        self.repo, self.root = self.fixture.repo, self.fixture.root
        for name in ('go.sum', 'LICENSE', 'THIRD_PARTY_NOTICES.md'):
            (self.repo / name).write_bytes((ROOT / name).read_bytes())
        self.fixture.commit()
        self.source = m.immutable_source(self.repo, 'candidate', self.fixture.command('rev-parse', 'HEAD'))
        self.fixture.source = self.source
        self.fixture.source_path.write_bytes(m.canonical(self.source))
        sums = (self.repo / 'go.sum').read_text().splitlines()
        self.deps = [{'Path': p, 'Version': v, 'Sum': s} for p, v, s in (line.split() for line in sums)
                     if (p, v) in t.LICENSES]
        def read_info(binary, source, os_, arch, go='go'):
            return dict(fixture.info_for(source, os_, arch), Deps=deepcopy(self.deps))
        self.reader = read_info
        self.fixture.go_reader = read_info
        self.directory, self.manifest = self.fixture.sealed()
        self.run = t.context(t.REPOSITORY, 123, 1, self.source['commit'], 456, 'a' * 64)
        self.verdict = dict(self.run, schema_version=1, ok=True, source_commit=self.source['commit'],
            source_tree=self.source['tree'], source_sha256=m.digest(m.canonical(self.source)),
            manifest_sha256=m.digest(m.canonical(self.manifest)), mode='candidate', publishable=False,
            native=[{'os': a['os'], 'arch': a['arch'], 'archive': {k: a[k] for k in ('name', 'sha256', 'bytes')},
                'binary_sha256': a['contents']['cutmy.exe' if a['os'] == 'windows' else 'cutmy']['sha256'],
                'required_groups': 7, 'required_checks': NATIVE_RECEIPT_COUNTS[(a['os'], a['arch'])],
                'report_sha256': 'b' * 64} for a in self.manifest['archives']],
            linux_archive_recovery={'required_groups': 9, 'decoded_media_observations': 11,
                'schema_versions': ['20261005-storage-queue-v3', '20261010-cache-retention-v1',
                                    '20261010-ordered-migrations-v1', '20261010-storage-maintenance-v1',
                                    '20261011-source-retention-v1', '20261012-source-media-cleanup-v1', '20261014-metadata-resolution-leases-v1', '20261016-agent-projects-v1'],
                'binary_sha256': self.manifest['archives'][0]['contents']['cutmy']['sha256'],
                'report_sha256': 'c' * 64})
        self.verdict_path = self.root / 'verdict.json'
        self.verdict_path.write_bytes(m.canonical(self.verdict))
        self.args = SimpleNamespace(repo=self.repo, directory=self.directory, source=self.fixture.source_path,
            expected_source_sha256=m.digest(m.canonical(self.source)), expected_manifest_sha256=m.digest(m.canonical(self.manifest)),
            verdict=self.verdict_path, expected_verdict_sha256=m.digest(m.canonical(self.verdict)),
            repository=t.REPOSITORY, run_id=123, run_attempt=1, event_sha=self.source['commit'], artifact_id=456,
            expected_artifact_sha256='a' * 64)

    def sbom(self, manifest=None, **kwargs):
        inputs = {'go_sum': (self.repo / 'go.sum').read_bytes(), 'notices': (self.repo / 'THIRD_PARTY_NOTICES.md').read_bytes(),
                  'project_license': (self.repo / 'LICENSE').read_bytes()}
        inputs.update(kwargs)
        return t.spdx(self.source, self.manifest if manifest is None else manifest, **inputs)

    def objects(self):
        with patch.object(m, 'go_identity', return_value=self.source['toolchain']), patch.object(m, 'binary_info', side_effect=self.reader):
            return t.load_objects(self.args)

    def reject(self, fn, *args, **kwargs):
        with self.assertRaises((ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError)):
            fn(*args, **kwargs)

    def test_deterministic_actual_composition_license_file_hashes_and_main_version(self):
        a, b = self.sbom(), self.sbom()
        self.assertEqual(m.canonical(a), m.canonical(b))
        self.assertEqual(len(a['packages']), 7 + len(self.deps))
        self.assertEqual(len(a['files']), 7)
        self.assertEqual(len(a['relationships']), 5 * (5 + len(self.deps)))
        main = next(p for p in a['packages'] if p['name'] == self.source['module'])
        self.assertEqual(main['versionInfo'], '(devel)')
        self.assertIn(self.source['version'], main['comment'])
        self.assertEqual(main['licenseDeclared'], 'MIT')
        self.assertEqual(a['creationInfo']['created'], self.source['build_time'])
        self.assertEqual({p['licenseDeclared'] for p in a['packages']}, {'MIT', 'BSD-3-Clause', 'NOASSERTION'})
        self.assertFalse(any(p['name'] in ('FFmpeg', 'PostgreSQL', 'Node.js') for p in a['packages']))
        for f in a['files']:
            self.assertTrue(m.HASH.fullmatch(f['checksums'][0]['checksumValue']))

    def test_linux_only_module_has_one_package_and_only_linux_dependency_edges(self):
        changed = deepcopy(self.manifest)
        for row in changed['archives']:
            if row['os'] != 'linux':
                row['go_build_info']['Deps'] = [dep for dep in row['go_build_info']['Deps']
                                                if dep['Path'] != 'golang.org/x/sys']
        doc = self.sbom(changed)
        modules = [p for p in doc['packages'] if p['name'] == 'golang.org/x/sys']
        self.assertEqual(len(modules), 1)
        self.assertEqual(modules[0]['versionInfo'], 'v0.44.0')
        self.assertEqual(modules[0]['licenseDeclared'], 'BSD-3-Clause')
        references = [r for r in doc['relationships'] if r['relatedSpdxElement'] == modules[0]['SPDXID']]
        self.assertEqual(len(references), 2)
        self.assertEqual({r['relationshipType'] for r in references}, {'DEPENDS_ON'})
        self.assertEqual({r['spdxElementId'] for r in references},
                         {t.identifier('Binary', 'linux/amd64'), t.identifier('Binary', 'linux/arm64')})
        self.assertEqual(len(doc['packages']), 7 + len(self.deps))
        self.assertEqual(len(doc['files']), 7)
        self.assertEqual(len(doc['relationships']), 5 * (5 + len(self.deps)) - 3)

    def test_missing_extra_duplicate_and_unsupported_targets(self):
        for rows in (self.manifest['archives'][:-1], self.manifest['archives'] + [deepcopy(self.manifest['archives'][0])]):
            changed = deepcopy(self.manifest); changed['archives'] = rows
            self.reject(self.sbom, changed)
        changed = deepcopy(self.manifest); changed['archives'][0]['arch'] = 'riscv64'
        self.reject(self.sbom, changed)

    def test_toolchain_main_and_build_setting_mismatch(self):
        for mutation in (lambda i: i.update(GoVersion='go9.0.0'), lambda i: i['Main'].update(Version='v1.2.3'),
                         lambda i: i['Main'].update(Replace={'Path': '/private'}),
                         lambda i: i['Settings'].append({'Key': 'CGO_ENABLED', 'Value': '1'})):
            changed = deepcopy(self.manifest); mutation(changed['archives'][0]['go_build_info'])
            self.reject(self.sbom, changed)

    def test_module_path_version_sum_replacement_duplicate_and_cross_target_disagreement(self):
        mutations = [lambda d: d.update(Path='example.invalid/fake'), lambda d: d.update(Version='v9.9.9'),
                     lambda d: d.update(Sum='h1:' + 'A' * 43 + '='), lambda d: d.update(Sum='not-a-sum'),
                     lambda d: d.update(Replace={'Path': '/private'})]
        for mutation in mutations:
            changed = deepcopy(self.manifest); mutation(changed['archives'][0]['go_build_info']['Deps'][0])
            self.reject(self.sbom, changed)
        changed = deepcopy(self.manifest); changed['archives'][0]['go_build_info']['Deps'].append(deepcopy(self.deps[0]))
        self.reject(self.sbom, changed)

    def test_immutable_go_sum_notice_and_license_inputs(self):
        for key in ('go_sum', 'notices', 'project_license'):
            self.reject(self.sbom, **{key: b'changed bytes'})

    def test_unknown_or_changed_license_has_explicit_noassertion(self):
        notices = '## example.invalid/mod v1.0.0\n\n### LICENSE\n\nunknown terms\n'
        self.assertEqual(t.reviewed_license(notices, 'example.invalid/mod', 'v1.0.0'), 'NOASSERTION')
        known = (self.repo / 'THIRD_PARTY_NOTICES.md').read_text()
        self.assertEqual(t.reviewed_license(known.replace('Copyright (c) 2019', 'Different copyright'), 'github.com/jackc/pgpassfile', 'v1.0.0'), 'NOASSERTION')
        heading = '## golang.org/x/sys v0.44.0\n'
        before, after = known.split(heading, 1)
        changed = before + heading + after.replace('Copyright 2009', 'Changed copyright', 1)
        self.assertEqual(t.reviewed_license(changed, 'golang.org/x/sys', 'v0.44.0'), 'NOASSERTION')
        changed = known.replace(heading, '## golang.org/x/sys v0.43.0\n', 1)
        self.assertEqual(t.reviewed_license(changed, 'golang.org/x/sys', 'v0.43.0'), 'NOASSERTION')

    def test_duplicate_spdx_ids_relationships_missing_refs_and_invalid_hash(self):
        mutations = [lambda d: d['packages'].append(deepcopy(d['packages'][0])),
                     lambda d: d['relationships'].append(deepcopy(d['relationships'][0])),
                     lambda d: d['relationships'][0].update(relatedSpdxElement='SPDXRef-missing'),
                     lambda d: d['files'][0]['checksums'][0].update(checksumValue='invalid')]
        for mutation in mutations:
            changed = self.sbom(); mutation(changed); self.reject(t.validate_spdx, changed)

    def test_fresh_archive_binary_or_buildinfo_change_is_refused_before_derivation(self):
        changed = self.directory / m.archive_name(self.source, 'linux', 'amd64')
        changed.write_bytes(changed.read_bytes() + b'changed')
        self.reject(self.objects)

    def test_fresh_reader_must_match_sealed_embedded_metadata(self):
        def altered(*args, **kwargs):
            value = self.reader(*args, **kwargs); value['Main']['Version'] = 'v9.9.9'; return value
        with patch.object(m, 'go_identity', return_value=self.source['toolchain']), patch.object(m, 'binary_info', side_effect=altered):
            self.reject(t.load_objects, self.args)

    def test_dirty_source_refused_before_go_metadata(self):
        (self.repo / 'untracked').write_text('extra')
        with patch.object(m, 'go_identity', side_effect=AssertionError('reader reached too early')):
            self.reject(t.load_objects, self.args)

    def test_verdict_run_attempt_source_manifest_artifact_and_required_evidence(self):
        fields = ['run_id', 'run_attempt', 'event_sha', 'artifact_id', 'artifact_sha256', 'repository', 'source_commit', 'source_tree',
                  'source_sha256', 'manifest_sha256', 'mode', 'publishable', 'ok']
        for key in fields:
            changed = deepcopy(self.verdict); changed[key] = 'wrong'
            self.reject(t.validate_verdict, changed, self.source, self.manifest, self.run)
        changed = deepcopy(self.verdict); changed['native'].pop(); self.reject(t.validate_verdict, changed, self.source, self.manifest, self.run)
        changed = deepcopy(self.verdict); changed['linux_archive_recovery']['schema_versions'][0] = 'future-schema'
        self.reject(t.validate_verdict, changed, self.source, self.manifest, self.run)

    def test_generation_uses_fresh_verified_input_and_trust_binds_every_stage(self):
        source, sbom, trust = self.objects()
        self.assertEqual(source, self.source)
        self.assertEqual(trust['sbom_sha256'], m.digest(m.canonical(sbom)))
        self.assertEqual(trust['verdict_sha256'], self.args.expected_verdict_sha256)
        self.assertEqual(trust['manifest_sha256'], self.args.expected_manifest_sha256)
        self.assertEqual(trust['archive_run'], self.run)
        self.assertEqual(len(t.subjects(trust, m.digest(m.canonical(trust)))), 7)

    def test_trust_requires_exact_six_migration_archive_recovery(self):
        t.validate_verdict(self.verdict, self.source, self.manifest, self.run)
        for mutation in ('old005', 'omitted', 'changed006', 'duplicate', 'extra'):
            with self.subTest(mutation=mutation):
                changed = deepcopy(self.verdict)
                versions = changed['linux_archive_recovery']['schema_versions']
                if mutation == 'old005':
                    versions.pop()
                elif mutation == 'omitted':
                    versions.pop(0)
                elif mutation == 'changed006':
                    versions[-1] = '20261012-source-media-cleanup-v2'
                elif mutation == 'duplicate':
                    versions.append(versions[-1])
                else:
                    versions.append('20261013-unreviewed-v1')
                self.reject(t.validate_verdict, changed, self.source, self.manifest, self.run)

    def test_frozen_native_inventory_matches_archive_gate_policy(self):
        spec = importlib.util.spec_from_file_location('trust_native_policy', ROOT / 'scripts/release-native.py')
        native = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(native)
        self.assertEqual(tuple(group for group, _ in t.NATIVE_GROUP_CHECK_COUNTS), native.REQUIRED_GROUPS)
        for os_, arch in NATIVE_RECEIPT_COUNTS:
            with self.subTest(os=os_, arch=arch):
                expected = dict(t.NATIVE_GROUP_CHECK_COUNTS)
                if os_ != 'windows':
                    expected['process'] += t.NATIVE_UNIX_PROCESS_CHECKS
                observed = {group: len(checks) for group, checks in native.required_checks(os_).items()}
                self.assertEqual(observed, expected)
                self.assertEqual(sum(observed.values()), NATIVE_RECEIPT_COUNTS[(os_, arch)])

    def test_trust_rejects_old_or_changed_native_group_and_check_counts(self):
        t.validate_verdict(self.verdict, self.source, self.manifest, self.run)
        for index, receipt in enumerate(self.verdict['native']):
            for mutation in ('old', 'pre_collection', 'missing', 'extra', 'other_platform', 'missing_group', 'extra_group',
                             'checks_float', 'groups_float'):
                with self.subTest(os=receipt['os'], arch=receipt['arch'], mutation=mutation):
                    changed = deepcopy(self.verdict)
                    native = changed['native'][index]
                    if mutation == 'old':
                        native['required_checks'] = 37 if receipt['os'] == 'windows' else 39
                    elif mutation == 'pre_collection':
                        native['required_checks'] = 45 if receipt['os'] == 'windows' else 47
                    elif mutation == 'other_platform':
                        native['required_checks'] = 53 if receipt['os'] == 'windows' else 51
                    elif mutation == 'checks_float':
                        native['required_checks'] = float(native['required_checks'])
                    elif mutation == 'groups_float':
                        native['required_groups'] = float(native['required_groups'])
                    elif mutation in ('missing_group', 'extra_group'):
                        native['required_groups'] += -1 if mutation == 'missing_group' else 1
                    else:
                        native['required_checks'] += -1 if mutation == 'missing' else 1
                    self.reject(t.validate_verdict, changed, self.source, self.manifest, self.run)

    def test_candidate_manual_pr_wrong_ref_or_event_cannot_sign(self):
        self.reject(t.signing_policy, self.source, self.run, 'push', 'refs/tags/' + self.source['version'])
        source = dict(self.source, mode='release', publishable=True, version='v1.2.3')
        t.signing_policy(source, self.run, 'push', 'refs/tags/v1.2.3')
        for event, ref in [('pull_request', 'refs/tags/v1.2.3'), ('workflow_dispatch', 'refs/tags/v1.2.3'),
                           ('push', 'refs/heads/main'), ('push', 'refs/tags/v1.2.4')]:
            self.reject(t.signing_policy, source, self.run, event, ref)
        self.reject(t.signing_policy, source, dict(self.run, event_sha='9' * 40), 'push', 'refs/tags/v1.2.3')

    def test_exact_signing_subjects_and_changed_sbom_or_extra_sidecar(self):
        _, sbom, trust = self.objects(); directory = self.root / 'prepared'; directory.mkdir()
        for n, v in [('SBOM.spdx.json', sbom), ('TRUST.json', trust)]: (directory / n).write_bytes(m.canonical(v))
        (directory / 'ATTESTATION-SUBJECTS.sha256').write_bytes(t.checksums(t.subjects(trust, m.digest(m.canonical(trust)))))
        t.verify_sidecars(directory, sbom, trust)
        (directory / 'SBOM.spdx.json').write_bytes(m.canonical(sbom) + b' '); self.reject(t.verify_sidecars, directory, sbom, trust)
        (directory / 'SBOM.spdx.json').write_bytes(m.canonical(sbom))
        (directory / 'unrelated').write_text('extra'); self.reject(t.verify_sidecars, directory, sbom, trust)

    def test_verified_statement_requires_exact_all_subjects_predicate_and_sbom(self):
        expected = {'first': 'a' * 64, 'second': 'b' * 64}
        statement = {'subject': [{'name': n, 'digest': {'sha256': v}} for n, v in expected.items()],
                     'predicateType': t.SPDX_PREDICATE, 'predicate': {'synthetic': True}}
        result = [{'verificationResult': {'statement': statement}}]
        t.verified_statement(result, expected, t.SPDX_PREDICATE, {'synthetic': True})
        for mutation in (lambda r: r[0]['verificationResult']['statement']['subject'].pop(),
                         lambda r: r[0]['verificationResult']['statement']['subject'].append(deepcopy(statement['subject'][0])),
                         lambda r: r[0]['verificationResult']['statement']['subject'][0]['digest'].update(sha256='9' * 64),
                         lambda r: r[0]['verificationResult']['statement'].update(predicateType=t.PROVENANCE),
                         lambda r: r[0]['verificationResult']['statement'].update(predicate={'fabricated': True})):
            changed = deepcopy(result); mutation(changed)
            self.reject(t.verified_statement, changed, expected, t.SPDX_PREDICATE, {'synthetic': True})

    def test_signature_verifier_enforces_certificate_identity_and_failed_crypto_refuses(self):
        source = dict(self.source, mode='release', publishable=True, version='v1.2.3')
        with patch.object(t.subprocess, 'check_output', return_value=b'[]') as command:
            self.assertEqual(t.verify_signature(Path('asset'), Path('bundle'), source, t.PROVENANCE), [])
            args = command.call_args.args[0]
            for flag, value in [('--repo', t.REPOSITORY), ('--signer-workflow', t.WORKFLOW),
                                ('--source-digest', source['commit']), ('--signer-digest', source['commit']),
                                ('--source-ref', 'refs/tags/v1.2.3'), ('--predicate-type', t.PROVENANCE)]:
                self.assertEqual(args[args.index(flag) + 1], value)
            self.assertIn('--deny-self-hosted-runners', args)
        with patch.object(t.subprocess, 'check_output', side_effect=subprocess.CalledProcessError(1, ['gh'])):
            self.reject(t.verify_signature, Path('corrupt'), Path('bundle'), source, t.PROVENANCE)

    def test_attested_zip_exact_files_duplicates_unsafe_and_nonregular(self):
        for names in (sorted(t.ATTESTED - {'TRUST.json'}), sorted(t.ATTESTED) + ['extra'],
                      sorted(t.ATTESTED - {'TRUST.json'}) + ['../TRUST.json'],
                      sorted(t.ATTESTED - {'TRUST.json'}) + ['SBOM.spdx.json']):
            archive = self.root / 'test.zip'
            with zipfile.ZipFile(archive, 'w') as z:
                for name in names: z.writestr(name, '{}')
            self.reject(t.extract_attested, archive, self.root / 'out', self.source)
        archive = self.root / 'valid.zip'
        with zipfile.ZipFile(archive, 'w') as z:
            for name in sorted(t.ATTESTED): z.writestr(name, '{}')
        t.extract_attested(archive, self.root / 'valid', self.source)
        self.assertEqual({p.name for p in (self.root / 'valid').iterdir()}, t.ATTESTED)
        for mode in (0o120777, 0o040755):
            archive = self.root / 'nonregular.zip'
            with zipfile.ZipFile(archive, 'w') as z:
                for name in sorted(t.ATTESTED):
                    info = zipfile.ZipInfo(name); info.create_system = 3
                    info.external_attr = (mode if name == 'TRUST.json' else 0o100644) << 16
                    z.writestr(info, '{}')
            self.reject(t.extract_attested, archive, self.root / 'nonregular', self.source)


if __name__ == '__main__':
    unittest.main()
