#!/usr/bin/env python3
"""Authenticate the exact versioned release asset set; never build or run it."""
import argparse
import base64
from contextlib import contextmanager
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import struct
import subprocess
import sys
import tempfile
import time
from urllib.parse import quote
import zipfile
import zlib

import release_manifest as m
from release_consumer_process import output as bounded_output

SPEC = importlib.util.spec_from_file_location('consumer_trust', Path(__file__).with_name('release-trust.py'))
t = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(t)
REMOTE = 'https://github.com/' + t.REPOSITORY + '.git'
JSON_LIMIT = t.MAX_SIDECAR
OUTPUT_LIMIT = 32 * 1024 * 1024
RECEIPT_SCOPE = 'All seven provenance subjects and all five SPDX subjects passed gh certificate identity and signature verification.'
TRUST_SCOPE = ('Authenticated GitHub-hosted workflow identity and tested archive hashes; trusts GitHub, repository administrators, '
               'workflow code and compiler/dependencies. No OS signing/notarization, SLSA level or independent hermetic rebuild claim.')
REFUSALS = (ValueError, OSError, KeyError, IndexError, TypeError, AttributeError, RecursionError, EOFError,
            OverflowError, struct.error, zlib.error, subprocess.SubprocessError, zipfile.BadZipFile)


class Refused(ValueError):
    def __init__(self, code):
        self.code = code
        super().__init__('Release verification refused: ' + code)


@contextmanager
def stage(code):
    try:
        yield
    except Refused:
        raise
    except REFUSALS:
        raise Refused(code) from None


def command_output(argv, limit, deadline=None):
    try:
        return bounded_output(argv, limit, deadline)
    except FileNotFoundError:
        raise Refused('tool_unavailable') from None


def plain_json(raw, limit=JSON_LIMIT):
    """Reject duplicates, floats, nonfinite values and excessive JSON structure."""
    m.require(0 < len(raw) <= limit, 'JSON exceeds size bounds.')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            m.require(key not in result, 'Duplicate JSON key.')
            result[key] = value
        return result
    def invalid(value):
        raise m.InvalidRelease('Only plain integer JSON numbers are supported.')
    value = json.loads(raw.decode('utf-8'), object_pairs_hook=unique, parse_float=invalid, parse_constant=invalid)
    pending, count = [(value, 0)], 0
    while pending:
        item, depth = pending.pop()
        count += 1
        m.require(depth <= 64 and count <= 200000, 'JSON structure exceeds bounds.')
        if type(item) is dict:
            pending.extend((v, depth + 1) for v in item.values())
        elif type(item) is list:
            pending.extend((v, depth + 1) for v in item)
        elif type(item) is int:
            m.require(abs(item) <= 2**63 - 1, 'JSON integer exceeds bounds.')
        else:
            m.require(item is None or type(item) in (str, bool), 'Unsupported JSON value.')
    return value


def control(directory, name):
    raw = (directory / name).read_bytes()
    value = plain_json(raw)
    m.require(type(value) is dict and raw == m.canonical(value), 'Noncanonical release control file.')
    return value


def remote_tag(version, commit, deadline=None):
    ref = 'refs/tags/' + version
    raw = command_output(['git', '-c', 'protocol.allow=never', '-c', 'protocol.https.allow=always', '-c', 'credential.helper=',
                          'ls-remote', '--exit-code', '--tags', REMOTE, ref, ref + '^{}'], 4096, deadline)
    rows = {}
    for line in raw.decode('ascii').splitlines():
        fields = line.split('\t')
        m.require(len(fields) == 2 and m.SHA.fullmatch(fields[0]) and fields[1] in (ref, ref + '^{}')
                  and fields[1] not in rows, 'Missing, malformed or ambiguous remote tag.')
        rows[fields[1]] = fields[0]
    m.require(ref in rows and rows.get(ref + '^{}', rows[ref]) == commit, 'Remote tag does not identify the reviewed commit.')
    return rows


def asset_names(version):
    source = {'version': version}
    names = {m.archive_name(source, os_, arch) for os_, arch in m.TARGETS} | {
        'SOURCE.json', 'MANIFEST.json', 'SHA256SUMS', *t.ATTESTED}
    sdk = load_sdk_verifier()
    if sdk.has_sdk_release(version):
        names |= sdk.release_names(version)
    return names


def load_sdk_verifier():
    spec = importlib.util.spec_from_file_location('consumer_release_sdk', Path(__file__).with_name('release-sdk.py'))
    sdk = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(sdk)
    return sdk


def regular_assets(directory, expected):
    m.require(directory.is_dir() and not directory.is_symlink()
              and {p.name for p in directory.iterdir()} == expected, 'Expected the exact versioned release asset set.')
    for name in expected:
        info = (directory / name).lstat()
        limit = 128 * 1024 * 1024 if name.endswith(('.tar.gz', '.zip', '.whl', '.tgz')) else JSON_LIMIT
        m.require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= limit, 'Nonregular or oversized release asset.')


def copy_assets(directory, scratch, expected):
    regular_assets(directory, expected)
    hashes = {}
    for name in sorted(expected):
        path = directory / name
        before = path.lstat()
        flags = os.O_RDONLY | getattr(os, 'O_BINARY', 0) | getattr(os, 'O_NOFOLLOW', 0)
        with os.fdopen(os.open(path, flags), 'rb') as source, (scratch / name).open('xb') as out:
            m.require(os.path.samestat(before, os.fstat(source.fileno())), 'Asset changed while opening.')
            digest, count = hashlib.sha256(), 0
            while block := source.read(1024 * 1024):
                count += len(block)
                m.require(count <= before.st_size, 'Asset changed while copying.')
                out.write(block)
                digest.update(block)
            after = path.lstat()
            m.require(count == before.st_size and stat.S_ISREG(after.st_mode) and os.path.samestat(before, after)
                      and (before.st_size, before.st_mtime_ns) == (after.st_size, after.st_mtime_ns), 'Asset changed while copying.')
        hashes[name] = digest.hexdigest()
    return hashes


def recorded_info(info, source, os_, arch):
    m.require(type(info) is dict and set(info) == {'GoVersion', 'Path', 'Main', 'Deps', 'Settings'}
              and type(info['Deps']) is list and type(info['Settings']) is list
              and info['Main'] == {'Path': source['module'], 'Version': '(devel)'}, 'Unsupported recorded BuildInfo.')
    m.require(all(type(s) is dict and set(s) == {'Key', 'Value'} and all(type(v) is str for v in s.values())
                  for s in info['Settings']), 'Invalid recorded build settings.')
    m.validate_binary_info(info, source, os_, arch)
    paths = set()
    for dep in info['Deps']:
        m.require(type(dep) is dict and set(dep) == {'Path', 'Version', 'Sum'}
                  and all(type(v) is str and v for v in dep.values()) and dep['Path'] not in paths
                  and re.fullmatch(r'h1:[A-Za-z0-9+/]{43}=', dep['Sum'])
                  and len(base64.b64decode(dep['Sum'][3:], validate=True)) == 32, 'Invalid recorded module identity.')
        paths.add(dep['Path'])


def archives(directory, source, manifest):
    rows = manifest['archives']
    m.require(type(manifest['schema_version']) is int and type(rows) is list and len(rows) == 5
              and manifest['publishable'] is True and type(manifest['source_epoch']) is int
              and {(r['os'], r['arch']) for r in rows} == set(m.TARGETS), 'Expected five unique recorded targets.')
    actual, notices = [], None
    for row in rows:
        os_, arch = row['os'], row['arch']
        name = m.archive_name(source, os_, arch)
        m.require(row['name'] == name and type(row['bytes']) is int
                  and all(type(v['bytes']) is int for v in row['contents'].values()), 'Invalid archive identity.')
        contents = m.read_archive(directory / name, source, os_, arch)
        info = row['go_build_info']
        recorded_info(info, source, os_, arch)
        text = contents['THIRD_PARTY_NOTICES.md'].decode('utf-8')
        for dep in info['Deps']:
            t.notice_section(text, dep['Path'] + ' ' + dep['Version'])
        if notices is None:
            notices = contents['THIRD_PARTY_NOTICES.md']
            license_bytes = contents['LICENSE']
        actual.append({'name': name, 'os': os_, 'arch': arch, 'bytes': (directory / name).stat().st_size,
            'sha256': file_hash(directory / name), 'contents': m.contents_identity(contents, os_), 'go_build_info': info})
    tools = manifest['tools']
    m.require(type(tools) is dict and set(tools) == {'go', 'python', 'zlib_compile', 'zlib_runtime'}
              and tools['go'] == source['toolchain'] and all(type(v) is str and v for v in tools.values()), 'Invalid recorded builder tools.')
    base = {k: source[k] for k in ('mode', 'publishable', 'version', 'commit', 'tree', 'source_epoch', 'build_time', 'toolchain')}
    base.update(schema_version=1, source_sha256=m.digest(m.canonical(source)), tools=tools, archives=actual)
    expected = dict(base, reproducibility={'builds': 2, 'scope': m.REPRO_SCOPE,
        'first_manifest_sha256': m.digest(m.canonical(base)), 'second_manifest_sha256': m.digest(m.canonical(base))})
    m.require(type(manifest['reproducibility']['builds']) is int and manifest == expected, 'Archive/content/recorded manifest differs.')
    m.require((directory / 'SHA256SUMS').read_bytes() == m.checksums(manifest), 'Archive checksum list differs.')
    return notices, license_bytes


def file_hash(path):
    result = hashlib.sha256()
    before = path.lstat()
    m.require(stat.S_ISREG(before.st_mode), 'Nonregular release asset.')
    with os.fdopen(os.open(path, os.O_RDONLY | getattr(os, 'O_BINARY', 0) | getattr(os, 'O_NOFOLLOW', 0)), 'rb') as file:
        m.require(os.path.samestat(before, os.fstat(file.fileno())), 'Asset changed while opening.')
        count = 0
        while block := file.read(1024 * 1024):
            count += len(block)
            m.require(count <= before.st_size, 'Asset changed while hashing.')
            result.update(block)
        after = path.lstat()
        m.require(count == before.st_size and stat.S_ISREG(after.st_mode) and os.path.samestat(before, after)
                  and (before.st_size, before.st_mtime_ns) == (after.st_size, after.st_mtime_ns), 'Asset changed while hashing.')
    return result.hexdigest()


def sbom_graph(source, manifest, sbom, notices, license_bytes):
    """Check signed composition against archived hashes and recorded BuildInfo.

    go.sum and fresh Go BuildInfo/native verdict are publisher gate evidence,
    not independently reproduced by a consumer with only the 13 public assets.
    """
    t.validate_spdx(sbom)
    m.require(set(sbom) == {'spdxVersion', 'dataLicense', 'SPDXID', 'name', 'documentNamespace', 'creationInfo',
                           'comment', 'documentDescribes', 'packages', 'files', 'relationships'}
              and sbom['documentNamespace'] == 'https://github.com/' + t.REPOSITORY + '/spdx/'
                  + m.digest(m.canonical(source)) + '/' + m.digest(m.canonical(manifest))
              and sbom['name'] == 'cutmyvideo-core ' + source['version'] + ' embedded CLI composition'
              and sbom['creationInfo'] == {'created': source['build_time'], 'creators': ['Tool: cutmy-release-trust-1']}
              and type(sbom['comment']) is str, 'SPDX source identity differs.')
    packages = {p['SPDXID']: p for p in sbom['packages']}
    expected, files, rels, described, modules = {}, [], [], [], {}
    def add(item):
        # Comments are authenticated descriptive text; structured graph is checked below.
        actual = packages.get(item['SPDXID'], {})
        m.require(type(actual.get('comment')) is str and actual['comment'], 'Missing SPDX package comment.')
        expected[item['SPDXID']] = dict(item, comment=actual['comment'])
    main_id, go_id = t.identifier('Main', source['module']), t.identifier('Go', source['toolchain'])
    add(t.package(main_id, source['module'], '(devel)', 'MIT' if m.digest(license_bytes) == t.PROJECT_LICENSE else 'NOASSERTION'))
    text = notices.decode('utf-8')
    go_license = 'BSD-3-Clause' if m.digest(t.notice_section(text, 'Go runtime and standard library').encode()) == t.GO_NOTICE else 'NOASSERTION'
    add(t.package(go_id, 'Go runtime and standard library', source['toolchain'], go_license))
    notice_ids = []
    for name, raw in (('LICENSE', license_bytes), ('THIRD_PARTY_NOTICES.md', notices)):
        identifier = t.identifier('Notice', name)
        notice_ids.append(identifier)
        files.append({'SPDXID': identifier, 'fileName': './' + name, 'checksums': [{'algorithm': 'SHA256', 'checksumValue': m.digest(raw)}],
            'licenseConcluded': 'NOASSERTION', 'licenseInfoInFiles': ['NOASSERTION'], 'copyrightText': 'NOASSERTION',
            'comment': 'Identical shipped bytes in all five archives.'})
    for row in manifest['archives']:
        target = row['os'] + '/' + row['arch']
        binary = 'cutmy.exe' if row['os'] == 'windows' else 'cutmy'
        checksum = [{'algorithm': 'SHA256', 'checksumValue': row['contents'][binary]['sha256']}]
        binary_id, file_id = t.identifier('Binary', target), t.identifier('File', target)
        described.append(binary_id)
        add(t.package(binary_id, 'cutmyvideo-core (' + target + ')', source['version'], checksums=checksum, primaryPackagePurpose='APPLICATION'))
        files.append({'SPDXID': file_id, 'fileName': './' + row['os'] + '-' + row['arch'] + '/' + binary,
            'checksums': checksum, 'licenseConcluded': 'NOASSERTION', 'licenseInfoInFiles': ['NOASSERTION'], 'copyrightText': 'NOASSERTION'})
        for identifier in (file_id, *notice_ids):
            rels.append({'spdxElementId': binary_id, 'relationshipType': 'CONTAINS', 'relatedSpdxElement': identifier})
        deps = [main_id, go_id]
        for dep in row['go_build_info']['Deps']:
            path, version = dep['Path'], dep['Version']
            m.require(path not in modules or modules[path] == dep, 'Recorded targets disagree about dependency identity.')
            modules[path] = dep
            identifier = t.identifier('Module', path + '@' + version)
            add(t.package(identifier, path, version, t.reviewed_license(text, path, version),
                externalRefs=[{'referenceCategory': 'PACKAGE-MANAGER', 'referenceType': 'purl',
                    'referenceLocator': 'pkg:golang/' + quote(path, safe='/') + '@' + quote(version, safe='')}]))
            m.require('Sum=' + dep['Sum'] + ' (Go module h1,' in expected[identifier]['comment'], 'SPDX recorded module checksum differs.')
            deps.append(identifier)
        for identifier in deps:
            rels.append({'spdxElementId': binary_id, 'relationshipType': 'DEPENDS_ON', 'relatedSpdxElement': identifier})
    m.require(packages == expected and sbom['files'] == sorted(files, key=lambda f: f['SPDXID'])
              and sbom['documentDescribes'] == sorted(described)
              and sbom['relationships'] == sorted(rels, key=lambda r: (r['spdxElementId'], r['relationshipType'], r['relatedSpdxElement'])),
              'Signed SPDX graph differs from archived hashes and recorded metadata.')


def identities(directory, source, manifest, sbom, trust, deadline=None):
    run = trust['archive_run']
    m.require(type(run) is dict and set(run) == {'repository', 'run_id', 'run_attempt', 'event_sha', 'artifact_id', 'artifact_sha256'}, 'Invalid archive run.')
    run = t.context(**run)
    t.signing_policy(source, run, 'push', 'refs/tags/' + source['version'])
    m.require(type(trust['schema_version']) is int and trust['publishable'] is True
              and m.HASH.fullmatch(trust['verdict_sha256']), 'Invalid signed verdict digest.')
    expected = {'schema_version': 1, 'repository': t.REPOSITORY, 'signer_workflow': t.WORKFLOW,
        'source_commit': source['commit'], 'source_tree': source['tree'], 'mode': 'release', 'publishable': True, 'version': source['version'],
        'source_sha256': file_hash(directory / 'SOURCE.json'), 'manifest_sha256': file_hash(directory / 'MANIFEST.json'),
        'sbom_sha256': file_hash(directory / 'SBOM.spdx.json'), 'verdict_sha256': trust['verdict_sha256'], 'archive_run': run,
        'archives': sorted([{k: r[k] for k in ('name', 'os', 'arch', 'sha256', 'bytes')} for r in manifest['archives']], key=lambda r: r['name']),
        'predicates': {'provenance': t.PROVENANCE, 'sbom': t.SPDX_PREDICATE}, 'scope': TRUST_SCOPE}
    m.require(trust == expected, 'Signed TRUST identity differs.')
    trust_sha = file_hash(directory / 'TRUST.json')
    subjects, sbom_subjects = t.subjects(trust, trust_sha), t.subjects(trust, trust_sha, sbom_only=True)
    receipt = control(directory, 'ATTESTATIONS.json')
    m.require(type(receipt['schema_version']) is int and receipt['actual_signature_verification'] is True
              and receipt == {'schema_version': 1, 'repository': t.REPOSITORY,
        'signer_workflow': t.WORKFLOW, 'source_commit': source['commit'], 'source_ref': 'refs/tags/' + source['version'],
        'source_sha256': trust['source_sha256'], 'trust_sha256': trust_sha, 'sbom_sha256': trust['sbom_sha256'],
        'provenance_subjects': subjects, 'sbom_subjects': sbom_subjects,
        'bundle_sha256': {n: file_hash(directory / n) for n in ('provenance.sigstore.json', 'sbom.sigstore.json')},
        'actual_signature_verification': True, 'scope': RECEIPT_SCOPE}, 'Attestation receipt differs.')
    for name in ('provenance.sigstore.json', 'sbom.sigstore.json'):
        bundle = plain_json((directory / name).read_bytes())
        m.require(type(bundle) is dict and bundle, 'Expected one plain JSON Sigstore bundle.')
    for predicate, bundle, expected_subjects, expected_predicate in (
            (t.PROVENANCE, 'provenance.sigstore.json', subjects, None),
            (t.SPDX_PREDICATE, 'sbom.sigstore.json', sbom_subjects, sbom)):
        for name in sorted(expected_subjects):
            argv = ['gh', 'attestation', 'verify', str(directory / name), '--bundle', str(directory / bundle),
                '--hostname', 'github.com', '--repo', t.REPOSITORY, '--signer-workflow', t.WORKFLOW,
                '--source-digest', source['commit'], '--signer-digest', source['commit'], '--source-ref', 'refs/tags/' + source['version'],
                '--deny-self-hosted-runners', '--predicate-type', predicate, '--format', 'json']
            with stage('provenance' if predicate == t.PROVENANCE else 'spdx'):
                result = plain_json(command_output(argv, OUTPUT_LIMIT, deadline), OUTPUT_LIMIT)
                m.require(type(result) is list and len(result) == 1 and type(result[0]) is dict
                          and set(result[0]) == {'attestation', 'verificationResult'} and type(result[0]['attestation']) is dict,
                          'Malformed or ambiguous gh verification result.')
                verification = result[0]['verificationResult']
                m.require(type(verification) is dict and type(verification.get('statement')) is dict
                          and verification.get('mediaType') == 'application/vnd.dev.sigstore.verificationresult+json;version=0.1'
                          and type(verification.get('signature')) is dict
                          and type(verification['signature'].get('certificate')) is dict and verification['signature']['certificate']
                          and type(verification.get('verifiedTimestamps')) is list and verification['verifiedTimestamps']
                          and all(type(stamp) is dict and set(stamp) == {'type', 'uri', 'timestamp'}
                                  and all(type(v) is str and v for v in stamp.values()) for stamp in verification['verifiedTimestamps']),
                          'Missing or malformed verification evidence.')
                certificate = verification['signature']['certificate']
                # Fulcio's OIDC certificate extension is independently covered
                # by gh's verified certificate, unlike predicate-controlled text.
                m.require(certificate.get('runInvocationURI') == 'https://github.com/' + t.REPOSITORY
                          + '/actions/runs/' + str(run['run_id']) + '/attempts/' + str(run['run_attempt']),
                          'Verified certificate run/attempt differs from signed TRUST.')
                statement = verification['statement']
                m.require(set(statement) == {'_type', 'subject', 'predicateType', 'predicate'}
                          and statement['_type'] == 'https://in-toto.io/Statement/v1' and type(statement['predicate']) is dict
                          and type(statement['subject']) is list, 'Malformed verified in-toto statement.')
                t.verified_statement(result, expected_subjects, predicate, expected_predicate)
                if predicate == t.PROVENANCE:
                    provenance_identity(statement['predicate'], source, run)


def provenance_identity(predicate, source, run):
    """Bind the official GitHub workflow/v1 predicate to the signed gate run.

    Predicate fields are authenticated workflow claims, not an independent
    native/recovery replay. Certificate identity remains enforced by gh.
    """
    build, details = predicate['buildDefinition'], predicate['runDetails']
    ref = 'refs/tags/' + source['version']
    repository = 'https://github.com/' + t.REPOSITORY
    m.require(build['buildType'] == 'https://actions.github.io/buildtypes/workflow/v1'
              and build['externalParameters']['workflow'] == {'ref': ref, 'repository': repository, 'path': '.github/workflows/release.yml'}
              and build['resolvedDependencies'] == [{'uri': 'git+' + repository + '@' + ref, 'digest': {'gitCommit': source['commit']}}]
              and details['builder']['id'] == 'https://github.com/' + t.WORKFLOW + '@' + ref
              and details['metadata']['invocationId'] == repository + '/actions/runs/' + str(run['run_id']) + '/attempts/' + str(run['run_attempt']),
              'Verified provenance workflow/source/run differs from signed TRUST.')
    github = build['internalParameters']['github']
    m.require(github['event_name'] == 'push' and github['runner_environment'] == 'github-hosted', 'Wrong provenance event or runner claim.')


def verify(directory, expected_commit, version):
    with stage('expected_identity'):
        m.require(type(expected_commit) is str and m.SHA.fullmatch(expected_commit), 'Expected an independently reviewed full commit.')
        m.release_version(version)
    deadline = time.monotonic() + 600
    with stage('tag_before'):
        before = remote_tag(version, expected_commit, deadline)
    directory = Path(directory).absolute()
    expected = asset_names(version)
    with tempfile.TemporaryDirectory(prefix='cutmy-consumer-') as temporary:
        scratch = Path(temporary)
        with stage('assets'):
            hashes = copy_assets(directory, scratch, expected)
        with stage('metadata'):
            source = control(scratch, 'SOURCE.json')
            m.validate_source_shape(source)
            m.require(source['commit'] == expected_commit and source['version'] == version
                      and source['mode'] == 'release' and source['publishable'] is True
                      and source['module'] == 'github.com/' + t.REPOSITORY, 'Downloaded source differs from reviewed release.')
            manifest, sbom, trust = (control(scratch, n) for n in ('MANIFEST.json', 'SBOM.spdx.json', 'TRUST.json'))
        # Authenticate all claims before parsing/decompressing archive contents.
        with stage('metadata'):
            identities(scratch, source, manifest, sbom, trust, deadline)
        sdk_subjects = 0
        sdk = load_sdk_verifier()
        if sdk.has_sdk_release(version):
            with stage('sdk_provenance'):
                sdk.verify_signed(scratch, source, expected_run=trust['archive_run'],
                                  command=lambda argv: command_output(argv, OUTPUT_LIMIT, deadline))
            sdk_subjects = 4
        with stage('archive_content'):
            notices, license_bytes = archives(scratch, source, manifest)
        with stage('composition'):
            sbom_graph(source, manifest, sbom, notices, license_bytes)
        with stage('changed_assets'):
            regular_assets(directory, expected)
            m.require(all(file_hash(directory / n) == value for n, value in hashes.items()), 'Downloaded asset changed during verification.')
        with stage('tag_after'):
            m.require(remote_tag(version, expected_commit, deadline) == before, 'Remote tag object or peeled commit changed during verification.')
    return {'schema_version': 1, 'ok': True, 'repository': t.REPOSITORY, 'version': version, 'source_commit': expected_commit,
        'assets': hashes, 'provenance_verified_subjects': 7, 'spdx_verified_subjects': 5, 'sdk_provenance_verified_subjects': sdk_subjects, 'remote_tag': before,
        'metadata_scope': 'Recorded BuildInfo, go.sum matching and native/recovery verdict are authenticated publisher claims; '
                          'this consumer checks actual archive/document/BUILD hashes without Go or executing binaries.'}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--expected-commit', required=True)
    parser.add_argument('--version', required=True)
    args = parser.parse_args(argv)
    try:
        print(json.dumps(verify(args.directory, args.expected_commit, args.version), sort_keys=True))
    except Refused as error:
        print('Release verification refused: ' + error.code + '. See docs/releases.md.', file=sys.stderr)
        return 1
    except REFUSALS:
        print('Release verification refused: local_io. See docs/releases.md.', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
