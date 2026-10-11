#!/usr/bin/env python3
"""Derive SPDX/trust sidecars from verified archives; never build or sign code."""
import argparse
import base64
import importlib.util
import json
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from urllib.parse import quote
import zipfile

import release_manifest as m

REPOSITORY = 'Kay0k1/cutmyvideo-core'
WORKFLOW = REPOSITORY + '/.github/workflows/release.yml'
PROVENANCE = 'https://slsa.dev/provenance/v1'
SPDX_PREDICATE = 'https://spdx.dev/Document/v2.3'
PREPARED = {'SBOM.spdx.json', 'TRUST.json', 'ATTESTATION-SUBJECTS.sha256'}
ATTESTED = {'SBOM.spdx.json', 'TRUST.json', 'provenance.sigstore.json',
            'sbom.sigstore.json', 'ATTESTATIONS.json'}
MAX_SIDECAR = 16 * 1024 * 1024
SPEC = importlib.util.spec_from_file_location('trust_fetch', Path(__file__).with_name('release-fetch.py'))
fetcher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(fetcher)

# Exact reviewed notice sections (UTF-8, outer whitespace removed). A module
# upgrade or changed notice loses the assertion until explicitly reviewed.
LICENSES = {
    ('github.com/jackc/pgpassfile', 'v1.0.0'): ('MIT', '5d42f329e2c8d964c4004ab4157fd3419f4008ab79f7369250d06f982f3a51a5'),
    ('github.com/jackc/pgservicefile', 'v0.0.0-20240606120523-5a60cdf6a761'): ('MIT', '8315d1df6a5478111f4c09faf321e1de102633945c2fab89560841ff439781e7'),
    ('github.com/jackc/pgx/v5', 'v5.9.2'): ('MIT', '69fbe9315493d8e267ca688d5b20df1a1fde17a5a5223d51a5567b83bba2e10e'),
    ('github.com/jackc/puddle/v2', 'v2.2.2'): ('MIT', 'd6cbcd066041a771a5a9cf5fe81bce5679275858431fff7a10a8959ffb57d932'),
    ('golang.org/x/sync', 'v0.22.0'): ('BSD-3-Clause', 'e5abec394985e8f4b263766c5222204b80c3413ec7f7c3e4c15fc5b575591cb1'),
    ('golang.org/x/text', 'v0.41.0'): ('BSD-3-Clause', 'e5abec394985e8f4b263766c5222204b80c3413ec7f7c3e4c15fc5b575591cb1'),
}
GO_NOTICE = '07e803b805ec67f8d2a26183c1a1e3bd7ef8f0c7c44cd4b35c7b775980630c4c'
PROJECT_LICENSE = 'dbd22263d7218a09000bf90ab07b53de55b246220392696d52e71b4b0b4b1548'
SCHEMAS = {'20261005-storage-queue-v3', '20261010-cache-retention-v1',
           '20261010-ordered-migrations-v1', '20261010-storage-maintenance-v1', '20261011-source-retention-v1'}


def identifier(kind, value):
    return 'SPDXRef-' + kind + '-' + m.digest(value.encode())[:32]


def notice_section(notices, heading):
    match = re.search(r'^## ' + re.escape(heading) + r'\n(.*?)(?=^## |\Z)', notices, re.M | re.S)
    m.require(match is not None, 'An embedded dependency has no archived notice section.')
    return match[1].strip()


def reviewed_license(notices, path, version):
    text = notice_section(notices, path + ' ' + version)
    reviewed = LICENSES.get((path, version))
    return reviewed[0] if reviewed and m.digest(text.encode()) == reviewed[1] else 'NOASSERTION'


def package(spdx_id, name, version, license_id='NOASSERTION', **extra):
    return {'SPDXID': spdx_id, 'name': name, 'versionInfo': version,
            'downloadLocation': 'NOASSERTION', 'filesAnalyzed': False,
            'licenseConcluded': 'NOASSERTION', 'licenseDeclared': license_id,
            'copyrightText': 'NOASSERTION', **extra}


def immutable_bytes(repo, source, name):
    identity = source['files'][name]
    raw = m.git(repo, 'cat-file', 'blob', identity['git_blob'])
    m.require(len(raw) == identity['bytes'] and m.digest(raw) == identity['sha256'], 'Immutable input differs: ' + name)
    return raw


def spdx(source, manifest, *, go_sum, notices, project_license):
    """Inputs have already been freshly checked against the actual five binaries."""
    m.validate_source_shape(source)
    m.require(m.digest(go_sum) == source['files']['go.sum']['sha256'], 'go.sum differs from the immutable source.')
    m.require(m.digest(notices) == source['files']['THIRD_PARTY_NOTICES.md']['sha256']
              and m.digest(project_license) == source['files']['LICENSE']['sha256'], 'Archived notices/license differ.')
    rows = manifest['archives']
    m.require(len(rows) == 5 and {(r['os'], r['arch']) for r in rows} == set(m.TARGETS), 'Exactly five unique targets are required.')
    notices_text = notices.decode('utf-8')
    sums = set(go_sum.decode('utf-8').splitlines())
    packages, files, relationships, seen_modules = [], [], [], {}
    main_id = identifier('Main', source['module'])
    main_license = 'MIT' if m.digest(project_license) == PROJECT_LICENSE else 'NOASSERTION'
    packages.append(package(main_id, source['module'], '(devel)', main_license,
        comment='Embedded Go main-module Version is (devel); CLI version is ' + source['version']
                + '; immutable source commit ' + source['commit'] + '. Project LICENSE is shipped in every archive.'))
    go_id = identifier('Go', source['toolchain'])
    go_license = 'BSD-3-Clause' if m.digest(notice_section(notices_text, 'Go runtime and standard library').encode()) == GO_NOTICE else 'NOASSERTION'
    packages.append(package(go_id, 'Go runtime and standard library', source['toolchain'], go_license,
        comment='Compiled-in Go runtime/standard-library contribution, not a separate bundled toolchain. '
                'Includes Go-vendored contributions described in THIRD_PARTY_NOTICES.md; not a standard-library source-file inventory.'))
    notice_id, license_id = identifier('Notice', 'THIRD_PARTY_NOTICES.md'), identifier('Notice', 'LICENSE')
    for name, data, file_id in (('LICENSE', project_license, license_id), ('THIRD_PARTY_NOTICES.md', notices, notice_id)):
        files.append({'SPDXID': file_id, 'fileName': './' + name,
            'checksums': [{'algorithm': 'SHA256', 'checksumValue': m.digest(data)}],
            'licenseConcluded': 'NOASSERTION', 'licenseInfoInFiles': ['NOASSERTION'],
            'copyrightText': 'NOASSERTION', 'comment': 'Identical shipped bytes in all five archives.'})
    described = []
    for row in sorted(rows, key=lambda r: (r['os'], r['arch'])):
        target = row['os'] + '/' + row['arch']
        info = row['go_build_info']
        m.validate_binary_info(info, source, row['os'], row['arch'])
        m.require(info['Main'] == {'Path': source['module'], 'Version': '(devel)'}, 'Unexpected embedded main-module version or checksum.')
        binary = 'cutmy.exe' if row['os'] == 'windows' else 'cutmy'
        binary_sha = row['contents'][binary]['sha256']
        m.require(m.HASH.fullmatch(binary_sha) and m.HASH.fullmatch(row['sha256']), 'Invalid executable/archive checksum.')
        binary_id = identifier('Binary', target)
        described.append(binary_id)
        binary_file = identifier('File', target)
        packages.append(package(binary_id, 'cutmyvideo-core (' + target + ')', source['version'],
            checksums=[{'algorithm': 'SHA256', 'checksumValue': binary_sha}],
            comment='Executable bytes from archive ' + row['name'] + '; archive SHA256 ' + row['sha256']
                    + '. CLI release version differs from the embedded main-module version.',
            primaryPackagePurpose='APPLICATION'))
        files.append({'SPDXID': binary_file, 'fileName': './' + row['os'] + '-' + row['arch'] + '/' + binary,
            'checksums': [{'algorithm': 'SHA256', 'checksumValue': binary_sha}],
            'licenseConcluded': 'NOASSERTION', 'licenseInfoInFiles': ['NOASSERTION'], 'copyrightText': 'NOASSERTION'})
        for file_id in (binary_file, license_id, notice_id):
            relationships.append({'spdxElementId': binary_id, 'relationshipType': 'CONTAINS', 'relatedSpdxElement': file_id})
        for dep_id in (main_id, go_id):
            relationships.append({'spdxElementId': binary_id, 'relationshipType': 'DEPENDS_ON', 'relatedSpdxElement': dep_id})
        deps = info.get('Deps', [])
        m.require(isinstance(deps, list), 'Embedded dependency array missing.')
        target_modules = set()
        for dep in deps:
            m.require(set(dep) == {'Path', 'Version', 'Sum'} and all(isinstance(dep[k], str) and dep[k] for k in dep), 'Malformed or replaced embedded dependency.')
            path, version, checksum = dep['Path'], dep['Version'], dep['Sum']
            m.require(path not in target_modules, 'Duplicate embedded dependency.')
            target_modules.add(path)
            m.require(re.fullmatch(r'h1:[A-Za-z0-9+/]{43}=', checksum) and len(base64.b64decode(checksum[3:], validate=True)) == 32,
                      'Invalid Go module h1 checksum.')
            m.require(path + ' ' + version + ' ' + checksum in sums, 'Embedded module/version/checksum absent from immutable go.sum.')
            module_id = identifier('Module', path + '@' + version)
            if path in seen_modules:
                m.require(seen_modules[path] == dep, 'Targets disagree about an embedded dependency.')
            else:
                seen_modules[path] = dict(dep)
                packages.append(package(module_id, path, version, reviewed_license(notices_text, path, version),
                    externalRefs=[{'referenceCategory': 'PACKAGE-MANAGER', 'referenceType': 'purl',
                                   'referenceLocator': 'pkg:golang/' + quote(path, safe='/') + '@' + quote(version, safe='')}],
                    comment='Embedded Go module Sum=' + checksum + ' (Go module h1, not an archive-file SHA256). '
                            'Exact archived notice: THIRD_PARTY_NOTICES.md heading "' + path + ' ' + version + '"; file SHA256 ' + m.digest(notices)))
            relationships.append({'spdxElementId': binary_id, 'relationshipType': 'DEPENDS_ON', 'relatedSpdxElement': module_id})
    doc = {'spdxVersion': 'SPDX-2.3', 'dataLicense': 'CC0-1.0', 'SPDXID': 'SPDXRef-DOCUMENT',
        'name': 'cutmyvideo-core ' + source['version'] + ' embedded CLI composition',
        'documentNamespace': 'https://github.com/' + REPOSITORY + '/spdx/' + m.digest(m.canonical(source)) + '/' + m.digest(m.canonical(manifest)),
        'creationInfo': {'created': source['build_time'], 'creators': ['Tool: cutmy-release-trust-1']},
        'comment': 'Go executable composition from embedded BuildInfo, immutable go.sum and shipped notices. '
                   'External FFmpeg, ffprobe, yt-dlp, Node.js, PostgreSQL and runner OS packages are excluded; '
                   'this is not a vulnerability scan or a complete dependency-source inventory.',
        'documentDescribes': sorted(described), 'packages': sorted(packages, key=lambda p: p['SPDXID']),
        'files': sorted(files, key=lambda f: f['SPDXID']),
        'relationships': sorted(relationships, key=lambda r: (r['spdxElementId'], r['relationshipType'], r['relatedSpdxElement']))}
    validate_spdx(doc)
    return doc


def validate_spdx(doc):
    m.require(doc['spdxVersion'] == 'SPDX-2.3' and doc['dataLicense'] == 'CC0-1.0' and doc['SPDXID'] == 'SPDXRef-DOCUMENT', 'Wrong SPDX document identity.')
    ids = [doc['SPDXID']] + [item['SPDXID'] for item in doc['packages'] + doc['files']]
    m.require(len(ids) == len(set(ids)) and all(re.fullmatch(r'SPDXRef-[A-Za-z0-9.-]+', i) for i in ids), 'Duplicate or invalid SPDX identifiers.')
    rels = [(r['spdxElementId'], r['relationshipType'], r['relatedSpdxElement']) for r in doc['relationships']]
    m.require(len(rels) == len(set(rels)) and all(a in ids and b in ids and t in ('CONTAINS', 'DEPENDS_ON') for a, t, b in rels), 'Invalid or duplicate SPDX relationships.')
    m.require(len(doc['documentDescribes']) == 5 and len(set(doc['documentDescribes'])) == 5
              and all(i in ids for i in doc['documentDescribes']), 'Five executable descriptions required.')
    for item in doc['packages'] + doc['files']:
        for checksum in item.get('checksums', []):
            m.require(checksum['algorithm'] == 'SHA256' and m.HASH.fullmatch(checksum['checksumValue']), 'Invalid SPDX file checksum.')


def context(repository, run_id, run_attempt, event_sha, artifact_id, artifact_sha256):
    m.require(repository == REPOSITORY and m.SHA.fullmatch(event_sha) and m.HASH.fullmatch(artifact_sha256)
              and all(type(v) is int and v > 0 for v in (run_id, run_attempt, artifact_id)), 'Invalid archive workflow identity.')
    return {'repository': repository, 'run_id': run_id, 'run_attempt': run_attempt, 'event_sha': event_sha,
            'artifact_id': artifact_id, 'artifact_sha256': artifact_sha256}


def validate_verdict(verdict, source, manifest, run):
    expected = dict(run, source_commit=source['commit'], source_tree=source['tree'],
        source_sha256=m.digest(m.canonical(source)), manifest_sha256=m.digest(m.canonical(manifest)),
        mode=source['mode'], publishable=source['publishable'])
    m.require(verdict.get('schema_version') == 1 and verdict.get('ok') is True
              and all(verdict.get(k) == v for k, v in expected.items()), 'Archive verdict identity differs or is unsuccessful.')
    native = verdict.get('native', [])
    m.require(len(native) == 5 and {(n['os'], n['arch']) for n in native} == set(m.TARGETS), 'Verdict lacks five native archives.')
    for n in native:
        row = next(r for r in manifest['archives'] if (r['os'], r['arch']) == (n['os'], n['arch']))
        binary = 'cutmy.exe' if n['os'] == 'windows' else 'cutmy'
        m.require(n['archive'] == {k: row[k] for k in ('name', 'sha256', 'bytes')}
                  and n['binary_sha256'] == row['contents'][binary]['sha256'] and n['required_groups'] == 7
                  and n['required_checks'] == (37 if n['os'] == 'windows' else 39) and m.HASH.fullmatch(n['report_sha256']), 'Native verdict receipt differs.')
    recovery = verdict.get('linux_archive_recovery', {})
    linux = next(r for r in manifest['archives'] if (r['os'], r['arch']) == ('linux', 'amd64'))
    m.require(recovery.get('required_groups') == 9 and recovery.get('decoded_media_observations') == 11
              and set(recovery.get('schema_versions', [])) == SCHEMAS and len(recovery['schema_versions']) == 5
              and recovery.get('binary_sha256') == linux['contents']['cutmy']['sha256']
              and m.HASH.fullmatch(recovery.get('report_sha256', '')), 'Actual Linux archive recovery verdict missing.')


def make_trust(source, manifest, sbom, verdict, run):
    validate_verdict(verdict, source, manifest, run)
    return {'schema_version': 1, 'repository': REPOSITORY, 'signer_workflow': WORKFLOW,
        'source_commit': source['commit'], 'source_tree': source['tree'], 'mode': source['mode'],
        'publishable': source['publishable'], 'version': source['version'],
        'source_sha256': m.digest(m.canonical(source)), 'manifest_sha256': m.digest(m.canonical(manifest)),
        'sbom_sha256': m.digest(m.canonical(sbom)), 'verdict_sha256': m.digest(m.canonical(verdict)),
        'archive_run': run, 'archives': sorted([{k: r[k] for k in ('name', 'os', 'arch', 'sha256', 'bytes')} for r in manifest['archives']], key=lambda r: r['name']),
        'predicates': {'provenance': PROVENANCE, 'sbom': SPDX_PREDICATE},
        'scope': 'Authenticated GitHub-hosted workflow identity and tested archive hashes; trusts GitHub, repository administrators, workflow code and compiler/dependencies. No OS signing/notarization, SLSA level or independent hermetic rebuild claim.'}


def subjects(trust, trust_sha256, *, sbom_only=False):
    result = {r['name']: r['sha256'] for r in trust['archives']}
    m.require(len(result) == 5 and all(Path(n).name == n and m.HASH.fullmatch(v) for n, v in result.items()), 'Invalid exact archive subjects.')
    if not sbom_only:
        result.update({'SBOM.spdx.json': trust['sbom_sha256'], 'TRUST.json': trust_sha256})
    return result


def checksums(values):
    return ''.join(v + '  ' + k + '\n' for k, v in sorted(values.items())).encode()


def exact_files(directory, expected):
    directory = Path(directory)
    m.require(directory.is_dir() and not directory.is_symlink() and {p.name for p in directory.iterdir()} == expected
              and all(p.is_file() and not p.is_symlink() and p.stat().st_size <= MAX_SIDECAR for p in directory.iterdir()), 'Missing, extra or nonregular trust sidecars.')


def extract_attested(archive, destination, source):
    fetcher.preflight_zip(archive, len(ATTESTED))
    destination = Path(destination)
    m.require(not destination.exists(), 'Sidecar destination must be new.')
    destination.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix='.cutmy-trust-download-', dir=destination.parent))
    try:
        with zipfile.ZipFile(archive) as zipped:
            entries = zipped.infolist()
            m.require(len(entries) == len(ATTESTED) and {e.filename for e in entries} == ATTESTED, 'Trust artifact has missing, duplicate or extra files.')
            for item in entries:
                m.require(not item.is_dir() and stat.S_IFMT(item.external_attr >> 16) in (0, stat.S_IFREG)
                          and not item.flag_bits & 1 and item.file_size <= MAX_SIDECAR
                          and item.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED), 'Unsupported trust artifact member.')
                data = zipped.read(item)
                m.require(len(data) == item.file_size, 'Trust artifact member size mismatch.')
                (stage / item.filename).write_bytes(data)
        exact_files(stage, ATTESTED)
        stage.rename(destination)
    finally:
        if stage.exists():
            shutil.rmtree(stage)


def signing_policy(source, run, event_name, ref):
    m.require(event_name == 'push' and source['mode'] == 'release' and source['publishable'] is True
              and ref == 'refs/tags/' + source['version'] and run['event_sha'] == source['commit'], 'Only the original clean release tag push may attest or publish.')


def verified_statement(results, expected_subjects, predicate_type, expected_predicate=None):
    # Results MUST come from a successful gh attestation verify invocation with
    # exact certificate repo/workflow/ref/digest policy, never raw bundle parsing.
    m.require(isinstance(results, list) and len(results) == 1, 'Exactly one verified attestation statement is required.')
    statement = results[0]['verificationResult']['statement']
    rows = statement['subject']
    actual = {r['name']: r['digest']['sha256'] for r in rows}
    m.require(len(rows) == len(actual) and all(set(r) == {'name', 'digest'} and set(r['digest']) == {'sha256'} for r in rows)
              and actual == expected_subjects and statement['predicateType'] == predicate_type, 'Signed subjects or predicate type differ.')
    if expected_predicate is not None:
        m.require(statement['predicate'] == expected_predicate, 'Signed SBOM predicate differs from the exact sidecar.')


def verify_signature(file, bundle, source, predicate_type):
    command = ['gh', 'attestation', 'verify', str(file), '--bundle', str(bundle), '--repo', REPOSITORY,
        '--signer-workflow', WORKFLOW, '--source-digest', source['commit'], '--signer-digest', source['commit'],
        '--source-ref', 'refs/tags/' + source['version'], '--deny-self-hosted-runners', '--predicate-type', predicate_type, '--format', 'json']
    raw = subprocess.check_output(command, timeout=60)
    m.require(len(raw) <= 32 * 1024 * 1024, 'Attestation verification output too large.')
    return json.loads(raw)


def load_objects(args):
    source = m.load_source(args.source, args.repo, args.expected_source_sha256)
    m.require(m.HASH.fullmatch(args.expected_manifest_sha256)
              and m.digest((args.directory / 'MANIFEST.json').read_bytes()) == args.expected_manifest_sha256, 'Manifest SHA256 differs.')
    manifest = m.verify(args.directory, source)
    verdict_raw = args.verdict.read_bytes()
    verdict = m.read_json(args.verdict)
    m.require(verdict_raw == m.canonical(verdict) and m.digest(verdict_raw) == args.expected_verdict_sha256, 'Verdict bytes differ.')
    run = context(args.repository, args.run_id, args.run_attempt, args.event_sha, args.artifact_id, args.expected_artifact_sha256)
    validate_verdict(verdict, source, manifest, run)
    sbom = spdx(source, manifest, go_sum=immutable_bytes(args.repo, source, 'go.sum'),
        notices=immutable_bytes(args.repo, source, 'THIRD_PARTY_NOTICES.md'), project_license=immutable_bytes(args.repo, source, 'LICENSE'))
    trust = make_trust(source, manifest, sbom, verdict, run)
    return source, sbom, trust


def verify_sidecars(directory, sbom, trust, attested=False):
    exact_files(directory, ATTESTED if attested else PREPARED)
    for name, value in (('SBOM.spdx.json', sbom), ('TRUST.json', trust)):
        m.require((directory / name).read_bytes() == m.canonical(value), 'Derived trust sidecar differs: ' + name)
    if not attested:
        m.require((directory / 'ATTESTATION-SUBJECTS.sha256').read_bytes() == checksums(subjects(trust, m.digest(m.canonical(trust)))), 'Explicit signing subjects differ.')


def finalize(args, source, sbom, trust):
    signing_policy(source, trust['archive_run'], args.event_name, args.ref)
    verify_sidecars(args.prepared, sbom, trust)
    trust_sha = m.digest(m.canonical(trust))
    expected = subjects(trust, trust_sha)
    # Each archive and both derived files need actual signature verification;
    # do not infer coverage merely from the first subject in a bundled statement.
    for name in sorted(expected):
        file = args.directory / name if name in {r['name'] for r in trust['archives']} else args.prepared / name
        verified_statement(verify_signature(file, args.provenance_bundle, source, PROVENANCE), expected, PROVENANCE)
    archive_subjects = subjects(trust, trust_sha, sbom_only=True)
    for name in sorted(archive_subjects):
        verified_statement(verify_signature(args.directory / name, args.sbom_bundle, source, SPDX_PREDICATE), archive_subjects, SPDX_PREDICATE, sbom)
    m.outside_repo(args.output, args.repo)
    m.require(not args.output.resolve().is_relative_to(args.directory.resolve()), 'Attestations must be outside the sealed bundle.')
    args.output.mkdir()
    for name in ('SBOM.spdx.json', 'TRUST.json'):
        shutil.copyfile(args.prepared / name, args.output / name)
    for name, path in (('provenance.sigstore.json', args.provenance_bundle), ('sbom.sigstore.json', args.sbom_bundle)):
        raw = path.read_bytes()
        m.require(0 < len(raw) <= MAX_SIDECAR, 'Retained attestation bundle exceeds size bounds.')
        (args.output / name).write_bytes(raw)
    record = {'schema_version': 1, 'repository': REPOSITORY, 'signer_workflow': WORKFLOW,
        'source_commit': source['commit'], 'source_ref': args.ref, 'source_sha256': trust['source_sha256'],
        'trust_sha256': trust_sha, 'sbom_sha256': trust['sbom_sha256'],
        'provenance_subjects': expected, 'sbom_subjects': archive_subjects,
        'bundle_sha256': {n: m.digest((args.output / n).read_bytes()) for n in ('provenance.sigstore.json', 'sbom.sigstore.json')},
        'actual_signature_verification': True, 'scope': 'All seven provenance subjects and all five SPDX subjects passed gh certificate identity and signature verification.'}
    (args.output / 'ATTESTATIONS.json').write_bytes(m.canonical(record))
    exact_files(args.output, ATTESTED)


def verify_attested(args, source, sbom, trust):
    signing_policy(source, trust['archive_run'], args.event_name, args.ref)
    verify_sidecars(args.sidecars, sbom, trust, attested=True)
    record = m.read_json(args.sidecars / 'ATTESTATIONS.json')
    m.require((args.sidecars / 'ATTESTATIONS.json').read_bytes() == m.canonical(record), 'Noncanonical attestation receipt.')
    trust_sha = m.digest(m.canonical(trust))
    expected, archive_subjects = subjects(trust, trust_sha), subjects(trust, trust_sha, sbom_only=True)
    m.require(record == {'schema_version': 1, 'repository': REPOSITORY, 'signer_workflow': WORKFLOW,
        'source_commit': source['commit'], 'source_ref': args.ref, 'source_sha256': trust['source_sha256'],
        'trust_sha256': trust_sha, 'sbom_sha256': trust['sbom_sha256'], 'provenance_subjects': expected, 'sbom_subjects': archive_subjects,
        'bundle_sha256': {n: m.digest((args.sidecars / n).read_bytes()) for n in ('provenance.sigstore.json', 'sbom.sigstore.json')},
        'actual_signature_verification': True, 'scope': 'All seven provenance subjects and all five SPDX subjects passed gh certificate identity and signature verification.'}, 'Attestation receipt differs.')
    for name in sorted(expected):
        file = args.directory / name if name in archive_subjects else args.sidecars / name
        verified_statement(verify_signature(file, args.sidecars / 'provenance.sigstore.json', source, PROVENANCE), expected, PROVENANCE)
    for name in sorted(archive_subjects):
        verified_statement(verify_signature(args.directory / name, args.sidecars / 'sbom.sigstore.json', source, SPDX_PREDICATE), archive_subjects, SPDX_PREDICATE, sbom)
    # Recheck current remote tag identity and checkout again after network calls.
    m.validate_source(args.repo, source)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    for name in ('generate', 'prepare-signing', 'finalize', 'verify-attested'):
        p = sub.add_parser(name)
        for flag in ('repo', 'directory', 'source', 'verdict'):
            p.add_argument('--' + flag, type=Path, required=True)
        for flag in ('expected-source-sha256', 'expected-manifest-sha256', 'expected-verdict-sha256', 'expected-artifact-sha256', 'repository', 'event-sha'):
            p.add_argument('--' + flag, required=True)
        for flag in ('run-id', 'run-attempt', 'artifact-id'):
            p.add_argument('--' + flag, type=int, required=True)
        if name in ('generate', 'prepare-signing', 'finalize'):
            p.add_argument('--output', type=Path, required=True)
        if name in ('prepare-signing', 'finalize', 'verify-attested'):
            p.add_argument('--event-name', required=True)
            p.add_argument('--ref', required=True)
        if name == 'prepare-signing':
            p.add_argument('--expected-sbom-sha256', required=True)
            p.add_argument('--expected-trust-sha256', required=True)
        if name == 'finalize':
            for flag in ('prepared', 'provenance-bundle', 'sbom-bundle'):
                p.add_argument('--' + flag, type=Path, required=True)
        if name == 'verify-attested':
            p.add_argument('--sidecars', type=Path, required=True)
    p = sub.add_parser('fetch-attested')
    for flag in ('repo', 'artifact-name', 'expected-artifact-sha256', 'expected-source-sha256'):
        p.add_argument('--' + flag, required=True)
    for flag in ('artifact-id', 'run-id'):
        p.add_argument('--' + flag, type=int, required=True)
    for flag in ('source', 'directory', 'evidence-dir'):
        p.add_argument('--' + flag, type=Path, required=True)
    args = parser.parse_args()
    if args.command == 'fetch-attested':
        source = m.load_source(args.source, expected_sha256=args.expected_source_sha256)
        fetcher.fetch(args.repo, args.artifact_id, args.run_id, args.artifact_name, args.expected_artifact_sha256,
                      source, args.directory, args.evidence_dir, extractor=extract_attested)
        return
    source, sbom, trust = load_objects(args)
    if args.command in ('generate', 'prepare-signing'):
        if args.command == 'prepare-signing':
            signing_policy(source, trust['archive_run'], args.event_name, args.ref)
            m.require(m.digest(m.canonical(sbom)) == args.expected_sbom_sha256
                      and m.digest(m.canonical(trust)) == args.expected_trust_sha256, 'Prepared trust identity differs from the required read-only gate.')
        m.outside_repo(args.output, args.repo)
        m.require(not args.output.resolve().is_relative_to(args.directory.resolve()), 'Sidecars must be outside the sealed bundle.')
        args.output.mkdir()
        for name, value in (('SBOM.spdx.json', sbom), ('TRUST.json', trust)):
            (args.output / name).write_bytes(m.canonical(value))
        (args.output / 'ATTESTATION-SUBJECTS.sha256').write_bytes(checksums(subjects(trust, m.digest(m.canonical(trust)))))
        verify_sidecars(args.output, sbom, trust)
        print(json.dumps({'sbom_sha256': m.digest(m.canonical(sbom)), 'trust_sha256': m.digest(m.canonical(trust))}, sort_keys=True))
    elif args.command == 'finalize':
        finalize(args, source, sbom, trust)
    else:
        verify_attested(args, source, sbom, trust)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError, StopIteration, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        # Network failures may contain signed URLs or headers; never render them.
        print('Release trust verification failed: ' + type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
