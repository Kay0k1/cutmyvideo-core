#!/usr/bin/env python3
"""Offline install smoke of SDK bytes authenticated by the signed-download step.

Consumes that step's private receipt; does not replace signature verification,
build packages, download dependencies, or run media services.
"""
from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import sys
import tempfile
import time

import release_manifest as m

HERE = Path(__file__).resolve().parent
REPOSITORY = 'Kay0k1/cutmyvideo-core'
LIMIT = 2 << 20
TOTAL_SECONDS = 300


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


consumer = load('sdk_install_consumer', 'release-consumer.py')
transport = load('sdk_install_transport', 'release-consumer-download.py')
sdk = consumer.load_sdk_verifier()


def regular(path, maximum=LIMIT):
    info = path.lstat()
    m.require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= maximum, 'Unsafe SDK smoke control file.')
    raw = path.read_bytes()
    m.require(len(raw) == info.st_size and os.path.samestat(info, path.lstat()), 'SDK smoke control changed.')
    return raw


def checked_inputs(evidence, scratch, version, commit, target):
    """Authenticate the hand-off to this step before interpreting package code."""
    m.release_version(version)
    m.require(m.SHA.fullmatch(commit) and sdk.has_sdk_release(version), 'SDK releases start at 1.4.0.')
    m.require(evidence.is_dir() and not evidence.is_symlink(), 'Missing signed download evidence.')
    receipt_raw = regular(evidence / 'consumer-verification.json')
    receipt = consumer.plain_json(receipt_raw)
    # The download status contains bounded elapsed-time floats; all trust input
    # is the strict integer-only signed-consumer receipt above.
    status_raw = regular(evidence / 'result.json')
    status = json.loads(status_raw)
    m.require(type(status) is dict and status.get('pass') is True
              and status.get('private_cleanup') is True and not status.get('failure')
              and status.get('version') == version and status.get('expected_commit') == commit
              and status.get('target') == {'os': target[0], 'arch': target[1]}
              and status.get('consumer') == {'path': 'consumer-verification.json', 'bytes': len(receipt_raw), 'sha256': m.digest(receipt_raw)},
              'Signed download/native step did not pass for this target.')
    m.require(type(receipt) is dict and receipt.get('schema_version') == 1 and receipt.get('ok') is True
              and receipt.get('repository') == REPOSITORY and receipt.get('version') == version
              and receipt.get('source_commit') == commit and receipt.get('provenance_verified_subjects') == 7
              and receipt.get('spdx_verified_subjects') == 5 and receipt.get('sdk_provenance_verified_subjects') == 4,
              'Missing actual SDK signature verification.')
    snapshot = scratch / 'assets'
    snapshot.mkdir(mode=0o700)
    hashes = consumer.copy_assets(evidence / 'assets', snapshot, consumer.asset_names(version))
    m.require(hashes == receipt.get('assets'), 'Downloaded assets differ from authenticated bytes.')
    source = m.load_source(snapshot / 'SOURCE.json')
    m.require(source['version'] == version and source['commit'] == commit and source['publishable'] is True,
              'SDK smoke source scope differs.')
    metadata, _ = sdk.load_manifest(snapshot, source, signed=True)
    m.require(metadata['package_version'] == version[1:], 'SDK package version differs from tag.')
    archive = m.archive_name(source, *target)
    native = status.get('native_smoke', {})
    m.require(native.get('archive') == archive and native.get('archive_sha256') == hashes[archive]
              and native.get('binary_sha256_before') == native.get('binary_sha256_after')
              and m.HASH.fullmatch(native.get('binary_sha256_before', '')), 'Missing native binary smoke receipt.')
    contents = m.read_archive(snapshot / archive, source, *target)
    name = 'cutmy.exe' if target[0] == 'windows' else 'cutmy'
    binary = scratch / name
    with binary.open('xb') as stream:
        stream.write(contents[name])
    binary.chmod(0o700)
    m.require(m.digest(contents[name]) == native['binary_sha256_before'], 'Extracted native binary differs.')
    controls = {'consumer-verification.json': m.digest(receipt_raw), 'result.json': m.digest(status_raw)}
    return source, metadata, hashes, controls, binary


def checked_unchanged(evidence, hashes, controls, binary, binary_hash):
    consumer.regular_assets(evidence / 'assets', set(hashes))
    m.require(all(consumer.file_hash(evidence / 'assets' / name) == digest for name, digest in hashes.items()),
              'Original authenticated assets changed during installation.')
    snapshot = binary.parent / 'assets'
    consumer.regular_assets(snapshot, set(hashes))
    m.require(all(consumer.file_hash(snapshot / name) == digest for name, digest in hashes.items()),
              'Private authenticated snapshot changed during installation.')
    m.require(all(m.digest(regular(evidence / name)) == digest for name, digest in controls.items()),
              'Signed verification receipt changed during installation.')
    m.require(consumer.file_hash(binary) == binary_hash, 'Native binary changed during SDK execution.')


def subtitle_case():
    text = '1\n00:00:10,000 --> 00:00:13,000\nПривет, редактор!\n'
    expected = '1\n00:00:00,000 --> 00:00:02,000\nПривет, редактор!\n\n'
    request = {'version': 1, 'format': 'srt', 'text': text, 'outputs': [{'clip_id': 'answer', 'segments': [{
        'index': 0, 'label': 'Ответ', 'source_start_ms': 11000, 'source_end_ms': 13000,
        'output_start_ms': 0, 'output_end_ms': 2000}]}]}
    result = {'version': 1, 'outputs': [{'clip_id': 'answer', 'format': 'srt', 'text': expected,
              'cue_count': 1, 'sha256': m.digest(expected.encode())}]}
    return request, result


def python_program(version):
    request, expected = subtitle_case()
    return '''import importlib.metadata as metadata, json, pathlib, sys
import cutmyvideo as sdk
from cutmyvideo import ProjectClipJob, ProjectCreateRequest
from cutmyvideo.sources import prepare_source, resume_source
assert metadata.version('cutmyvideo') == VERSION
assert not metadata.requires('cutmyvideo')
assert pathlib.Path(sdk.__file__).resolve().is_relative_to(pathlib.Path(sys.prefix).resolve())
assert 'clip_jobs' in ProjectCreateRequest.__annotations__
assert set(ProjectClipJob.__annotations__) == {'clip_id', 'job_id'}
for name in ('prepare_source','resume_source','validate_collection','submit_collection','run_collection','resume_collection','create_project','get_project','edit_project','create_project_handoff','retime_subtitles'):
    assert callable(getattr(sdk.CutmyClient, name))
assert callable(sdk.export_portable_project) and callable(sdk.import_portable_project)
assert callable(prepare_source) and callable(resume_source)
core = sdk.LocalCutmy(sys.argv[1], env={'FFMPEG_PATH':sys.argv[2], 'FFPROBE_PATH':sys.argv[2]})
assert core.retime_subtitles(REQUEST, timeout=20) == EXPECTED
print(json.dumps({'version':VERSION, 'exports':True, 'no_runtime_dependencies':True, 'subtitle':EXPECTED}, ensure_ascii=True))
'''.replace('VERSION', repr(version)).replace('REQUEST', repr(request)).replace('EXPECTED', repr(expected))


def node_program(version):
    request, expected = subtitle_case()
    return '''import assert from 'node:assert/strict';
import {readFile,realpath,lstat} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {CutmyClient,exportPortableProject,importPortableProject} from '@cutmyvideo/sdk';
import {LocalCutmy} from '@cutmyvideo/sdk/local';
import {runCollection,resumeCollection} from '@cutmyvideo/sdk/collections';
import {prepareSource,resumeSource} from '@cutmyvideo/sdk/sources';
const root = fileURLToPath(new URL('./node_modules/@cutmyvideo/sdk/', import.meta.url));
assert(!(await lstat(root)).isSymbolicLink());
assert((await realpath(fileURLToPath(import.meta.resolve('@cutmyvideo/sdk')))).startsWith(await realpath(root)));
const metadata = JSON.parse(await readFile(new URL('./node_modules/@cutmyvideo/sdk/package.json', import.meta.url)));
assert.equal(metadata.version, VERSION); assert.equal(metadata.name, '@cutmyvideo/sdk');
assert.deepEqual(metadata.dependencies ?? {}, {});
for (const name of ['getSourcePreparation','validateCollection','submitCollection','createProject','getProject','editProject','createProjectHandoff','retimeSubtitles']) assert.equal(typeof CutmyClient.prototype[name], 'function');
for (const fn of [runCollection,resumeCollection,prepareSource,resumeSource,exportPortableProject,importPortableProject]) assert.equal(typeof fn,'function');
const core = new LocalCutmy({binary:process.argv[2],env:{FFMPEG_PATH:process.argv[3],FFPROBE_PATH:process.argv[3]}});
assert.deepEqual(await core.retimeSubtitles(REQUEST,{timeoutMs:20000}),EXPECTED);
console.log(JSON.stringify({version:VERSION,exports:true,no_runtime_dependencies:true,subtitle:EXPECTED}));
'''.replace('VERSION', json.dumps(version)).replace('REQUEST', json.dumps(request, ensure_ascii=True)).replace('EXPECTED', json.dumps(expected, ensure_ascii=True))


def installed_smoke(snapshot, metadata, binary, private, command):
    packages = {row['kind']: snapshot / row['name'] for row in metadata['packages']}
    version = metadata['package_version']
    command([sys.executable, '-I', '-m', 'venv', str(private / 'python')], 'python-venv', seconds=90)
    python = private / 'python' / ('Scripts/python.exe' if os.name == 'nt' else 'bin/python')
    command([str(python), '-I', '-m', 'pip', '--isolated', 'install', '--no-index', '--no-deps', '--no-cache-dir',
             '--disable-pip-version-check', '--no-compile', str(packages['wheel'])], 'python-install', seconds=60)
    program = private / 'python-smoke.py'
    program.write_text(python_program(version), encoding='utf-8')
    missing = str(private / 'no-media-executable')
    py = command([str(python), '-I', str(program), str(binary), missing], 'python-smoke', seconds=30)
    node, npm = shutil.which('node'), shutil.which('npm')
    m.require(node is not None and npm is not None, 'Node and npm are required.')
    npm_entry = Path(npm).parent / 'node_modules/npm/bin/npm-cli.js'
    npm_command = [node, str(npm_entry)] if os.name == 'nt' else [npm]
    m.require(os.name != 'nt' or npm_entry.is_file(), 'Missing actual npm JavaScript entry.')
    project = private / 'node-project'
    project.mkdir(mode=0o700)
    (project / 'package.json').write_text('{"private":true,"type":"module"}\n')
    config, global_config = private / 'npm-user.conf', private / 'npm-global.conf'
    config.write_text(''); global_config.write_text('')
    command(npm_command + ['--userconfig', str(config), '--globalconfig', str(global_config), '--cache', str(private / 'npm-cache'),
            'install', '--offline', '--ignore-scripts', '--no-audit', '--no-fund', '--package-lock=false', '--omit=dev',
            '--prefix', str(project), str(packages['npm'])], 'npm-install', seconds=60)
    program = project / 'smoke.mjs'
    program.write_text(node_program(version), encoding='utf-8')
    ts = command([node, str(program), str(binary), missing], 'typescript-smoke', seconds=30)
    _, expected = subtitle_case()
    verdict = {'version': version, 'exports': True, 'no_runtime_dependencies': True, 'subtitle': expected}
    m.require(consumer.plain_json(py.read_bytes(), LIMIT) == verdict and consumer.plain_json(ts.read_bytes(), LIMIT) == verdict,
              'Installed SDKs did not produce exact independent Unicode sidecars.')
    return {'python': transport.file_identity(py), 'typescript': transport.file_identity(ts),
            'subtitle_sha256': expected['outputs'][0]['sha256'], 'cue_count': 1}


def run(args):
    evidence, output = args.evidence.absolute(), args.output.absolute()
    m.require(not output.exists() and not output.is_symlink() and output.parent.is_dir()
              and output.resolve() == output and not output.is_relative_to(HERE.parent), 'Fresh external SDK install output required.')
    output.mkdir(mode=0o700)
    report = {'schema_version': 1, 'pass': False, 'failure': None, 'private_cleanup': False, 'repository': REPOSITORY,
              'version': args.version, 'expected_commit': args.expected_commit, 'target': {'os': args.target_os, 'arch': args.target_arch},
              'scope': 'Offline installed authenticated wheel/npm bytes and exact Unicode local subtitle retime; no new signature, media or service claim.'}
    temporary, phase = None, 'native_target'
    started = time.monotonic()
    try:
        native = load('sdk_install_native', 'release-native.py')
        target = (args.target_os, args.target_arch)
        m.require(native.native_target() == target, 'Wrong native target.')
        temporary = tempfile.TemporaryDirectory(prefix='cutmy-signed-sdk-install-')
        private = Path(temporary.name)
        phase = 'authenticated_inputs'
        source, metadata, hashes, controls, binary = checked_inputs(evidence, private, args.version, args.expected_commit, target)
        binary_hash = consumer.file_hash(binary)
        def command(argv, name, seconds):
            return transport.capture(argv, output, name, private, started + TOTAL_SECONDS, limit=LIMIT, seconds=seconds)
        phase = 'offline_install_and_retime'
        report['installed'] = installed_smoke(private / 'assets', metadata, binary, private, command)
        phase = 'unchanged_inputs'
        checked_unchanged(evidence, hashes, controls, binary, binary_hash)
        report.update(asset_sha256=hashes, verification_receipts_sha256=controls, binary_sha256_before=binary_hash,
                      binary_sha256_after=consumer.file_hash(binary), source_tree=source['tree'])
        m.require(time.monotonic() < started + TOTAL_SECONDS, 'SDK install deadline exceeded.')
    except (ValueError, RuntimeError, OSError, KeyError, TypeError, AttributeError, RecursionError):
        report['failure'] = phase
    finally:
        if temporary is not None:
            path = Path(temporary.name)
            try:
                temporary.cleanup()
                report['private_cleanup'] = not path.exists()
            except OSError:
                report['private_cleanup'] = False
        report['pass'] = report['failure'] is None and report['private_cleanup']
        report['elapsed_seconds'] = time.monotonic() - started
        transport.exclusive(output / 'result.json', report)
    m.require(report['pass'], 'SDK install smoke refused: ' + str(report['failure'] or 'private_cleanup'))
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--version', required=True)
    parser.add_argument('--expected-commit', required=True)
    parser.add_argument('--target-os', choices=['linux','darwin','windows'], required=True)
    parser.add_argument('--target-arch', choices=['amd64','arm64'], required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    signal.signal(signal.SIGINT, transport.interrupt)
    signal.signal(signal.SIGTERM, transport.interrupt)
    try:
        report = run(args)
        print(json.dumps({'pass': True, 'version': report['version'], 'target': report['target']}, sort_keys=True))
    except (ValueError, RuntimeError, OSError):
        print('Offline signed SDK installation refused; inspect its bounded evidence.', file=sys.stderr)
        raise SystemExit(1)


if __name__ == '__main__':
    main()
