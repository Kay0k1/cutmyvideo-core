#!/usr/bin/env python3
"""Require all five native archive reports bound to one source and artifact."""

import argparse
import importlib.util
import json
import math
from pathlib import Path, PurePosixPath, PureWindowsPath
import sys

import release_manifest as m

SPEC = importlib.util.spec_from_file_location('release_native', Path(__file__).with_name('release-native.py'))
native = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(native)

ARCHIVE_RECOVERY_CHECKS = (
    'current_external_archive_verified',
    'old_real_upload_two_session_ownership_and_queued_idempotency',
    'old_SIGTERM_partial_and_queued_jobs_upgrade_after_real_lease_expiry',
    'real_first_item_SIGKILL_before_any_artifact_publication',
    'actual_binary_future_schema_refusal_without_data_or_ledger_changes',
    'SIGKILL_coordinated_pg_dump_media_backup_fresh_database_and_directory_restore',
    'restored_zero_result_partial_and_queued_jobs_recover_without_duplicate_results',
    'restored_new_upload_MP3_and_copy_MP4_download_range_probe_decode',
    'restored_media_ledger_files_and_counters_match',
)
SCHEMA_VERSIONS = {'20261005-storage-queue-v3', '20261010-cache-retention-v1',
                   '20261010-ordered-migrations-v1', '20261010-storage-maintenance-v1'}
RECOVERY_MEDIA_LABELS = {'before-upgrade', 'after-upgrade-retained', 'after-upgrade-recovered',
                         'after-upgrade-queued', 'before-backup', 'after-restore-retained',
                         'after-restore-recovered', 'after-restore-queued', 'after-restore-first-result',
                         'fresh-after-restore-mp3', 'fresh-after-restore-copy'}


def validate_recovery(report, source, manifest, *, repository, run_id, run_attempt, artifact_id, artifact_sha256):
    m.require(report.get('ok') is True and report.get('checks') == list(ARCHIVE_RECOVERY_CHECKS),
              'All nine actual archive upgrade/recovery groups are required.')
    m.require(report['historical_revision'] == 'f2be62c42a6a0d3d7a017b40bd683596bc578b95'
              and report['current_revision'] == source['commit']
              and report['go_version'] == f"go version {source['toolchain']} linux/amd64"
              and set(report['schema_versions']) == SCHEMA_VERSIONS and len(report['schema_versions']) == 4
              and report['upgrade_storage_defaults_checked'] == 3,
              'Archive recovery revision, schemas or upgrade defaults differ.')
    media = report['artifacts']
    m.require(isinstance(media, list) and len(media) == 11
              and {item.get('label') for item in media} == RECOVERY_MEDIA_LABELS
              and all(type(item['bytes']) is int and item['bytes'] > 0 and m.HASH.fullmatch(item['sha256'])
                      and type(item['duration_seconds']) in (int, float) and math.isfinite(item['duration_seconds'])
                      and item['duration_seconds'] > 0 and isinstance(item['codecs'], dict)
                      and item['codecs'] == ({'audio': 'mp3'} if item['label'] == 'fresh-after-restore-mp3'
                                             else {'video': 'h264', 'audio': 'aac'}) for item in media),
              'Archive recovery requires eleven decoded media observations.')
    external = report['current_external_archive']
    m.require(external['source_sha256'] == m.digest(m.canonical(source))
              and external['source_commit'] == source['commit'] and external['source_tree'] == source['tree']
              and external['current_application_built'] is False,
              'Recovery must use the supplied archive without rebuilding the current application.')
    entry = next(item for item in manifest['archives'] if (item['os'], item['arch']) == ('linux', 'amd64'))
    binary_sha = entry['contents']['cutmy']['sha256']
    m.require(external['expected_binary_sha256'] == external['captured_binary_sha256_before']
              == external['captured_binary_sha256_after'] == report['binary_sha256']['current'] == binary_sha,
              'Recovery executable differs from the sealed Linux archive.')
    native.runtime_version(external['runtime_version'], source, ('linux', 'amd64'))
    receipt = external['archive_receipt']
    m.require(receipt['repository'] == repository and receipt['run_id'] == run_id
              and receipt['artifact_id'] == artifact_id and receipt['artifact_sha256'] == artifact_sha256
              and receipt['artifact_name'] == f'release-bundle-{run_id}-{run_attempt}'
              and receipt['source_commit'] == source['commit']
              and receipt['source_sha256'] == m.digest(m.canonical(source)) and m.HASH.fullmatch(receipt['sha256']),
              'Archive recovery download is not bound to the packaging artifact.')
    return {'required_groups': 9, 'decoded_media_observations': 11, 'schema_versions': sorted(SCHEMA_VERSIONS),
            'binary_sha256': binary_sha, 'report_sha256': m.digest(m.canonical(report))}


def validate_reports(reports, source, manifest, *, manifest_sha256, repository, run_id,
                     run_attempt, event_sha, artifact_id, artifact_sha256):
    m.require(len(reports) == len(m.TARGETS), 'Exactly five native reports are required.')
    expected_source = {key: source[key] for key in ('commit', 'tree', 'mode', 'version', 'build_time', 'toolchain')}
    expected_source['sha256'] = m.digest(m.canonical(source))
    seen = set()
    receipts = []
    for report in reports:
        native.validate_report(report)
        target = (report['target']['os'], report['target']['arch'])
        m.require(target in m.TARGETS and target not in seen, 'Native target is missing, duplicated or unsupported.')
        seen.add(target)
        m.require(report['source'] == expected_source and report['manifest_sha256'] == manifest_sha256,
                  'Native report source or manifest differs from the resolved source.')
        m.require(report['run'] == {'id': str(run_id), 'attempt': str(run_attempt), 'job': 'native', 'workflow_sha': event_sha},
                  'Native report belongs to a different workflow run, attempt, job or event.')
        artifact = report['artifact_receipt']
        m.require(artifact['repository'] == repository and artifact['run_id'] == run_id
                  and artifact['artifact_id'] == artifact_id and artifact['artifact_sha256'] == artifact_sha256
                  and artifact['artifact_name'] == f'release-bundle-{run_id}-{run_attempt}'
                  and m.HASH.fullmatch(artifact['sha256']) and type(artifact['artifact_bytes']) is int
                  and artifact['artifact_bytes'] > 0, 'Native download differs from the packaged artifact.')
        entry = next(item for item in manifest['archives'] if (item['os'], item['arch']) == target)
        m.require(report['archive'] == {key: entry[key] for key in ('name', 'sha256', 'bytes')},
                  'Native report archive differs from the sealed manifest.')
        name = 'cutmy.exe' if target[0] == 'windows' else 'cutmy'
        binary = entry['contents'][name]
        path_type = PureWindowsPath if target[0] == 'windows' else PurePosixPath
        path = path_type(report['binary']['absolute_path'])
        m.require(path.is_absolute() and path.name == name
                  and report['binary']['bytes'] == binary['bytes']
                  and report['binary']['sha256_before'] == report['binary']['sha256_after'] == binary['sha256'],
                  'Native executable path, size or hash differs from the archived executable.')
        archive_evidence = report['groups']['archive']['evidence']
        m.require(archive_evidence['contents'] == entry['contents']
                  and archive_evidence['go_build_info'] == entry['go_build_info'],
                  'Native embedded build/document identities differ from the sealed manifest.')
        helper = report['process_helper']
        helper_path = path_type(helper['binary_path'])
        m.require(helper['source_path'] == 'scripts/testdata/release-process-helper.go'
                  and helper['source_sha256'] == source['files'][helper['source_path']]['sha256']
                  and m.HASH.fullmatch(helper['binary_sha256']) and helper_path.is_absolute()
                  and helper_path != path and helper['application_binary'] is False,
                  'Process helper is not separately bound to its source and binary.')
        tools = report['tools']
        m.require(isinstance(tools.get('python'), str) and tools['python']
                  and all(isinstance(tools.get(tool), dict) and isinstance(tools[tool].get('version'), str)
                          and tools[tool]['version'] for tool in ('ffmpeg', 'ffprobe', 'psql')),
                  'Native prerequisite tool versions are missing.')
        receipts.append({'os': target[0], 'arch': target[1], 'archive': report['archive'],
                         'binary_sha256': binary['sha256'], 'required_groups': len(native.REQUIRED_GROUPS),
                         'required_checks': sum(len(checks) for checks in native.required_checks(target[0]).values()),
                         'report_sha256': m.digest(m.canonical(report))})
    m.require(seen == set(m.TARGETS), 'All five native targets must execute.')
    return sorted(receipts, key=lambda value: (value['os'], value['arch']))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--expected-source-sha256', required=True)
    parser.add_argument('--expected-manifest-sha256', required=True)
    parser.add_argument('--reports-dir', type=Path, required=True)
    parser.add_argument('--recovery-report', type=Path, required=True)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--run-id', type=int, required=True)
    parser.add_argument('--run-attempt', type=int, required=True)
    parser.add_argument('--event-sha', required=True)
    parser.add_argument('--artifact-id', type=int, required=True)
    parser.add_argument('--expected-artifact-sha256', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    m.require(not args.output.resolve().is_relative_to(args.directory.resolve()),
              'Verdict must be outside the sealed bundle.')
    source = m.load_source(args.source, expected_sha256=args.expected_source_sha256)
    m.require(m.HASH.fullmatch(args.expected_manifest_sha256)
              and m.digest((args.directory / 'MANIFEST.json').read_bytes()) == args.expected_manifest_sha256,
              'Sealed MANIFEST digest mismatch.')
    manifest = m.verify(args.directory, source)
    paths = sorted(args.reports_dir.rglob('native-report.json'))
    reports = [m.read_json(path) for path in paths]
    native_receipts = validate_reports(reports, source, manifest, manifest_sha256=args.expected_manifest_sha256,
                                      repository=args.repository, run_id=args.run_id, run_attempt=args.run_attempt,
                                      event_sha=args.event_sha, artifact_id=args.artifact_id,
                                      artifact_sha256=args.expected_artifact_sha256)
    recovery = validate_recovery(m.read_json(args.recovery_report), source, manifest, repository=args.repository,
                                 run_id=args.run_id, run_attempt=args.run_attempt, artifact_id=args.artifact_id,
                                 artifact_sha256=args.expected_artifact_sha256)
    verdict = {'schema_version': 1, 'ok': True, 'source_commit': source['commit'], 'source_tree': source['tree'],
               'source_sha256': args.expected_source_sha256, 'manifest_sha256': args.expected_manifest_sha256,
               'mode': source['mode'], 'publishable': source['publishable'], 'repository': args.repository,
               'run_id': args.run_id, 'run_attempt': args.run_attempt, 'event_sha': args.event_sha,
               'artifact_id': args.artifact_id, 'artifact_sha256': args.expected_artifact_sha256,
               'native': native_receipts, 'linux_archive_recovery': recovery,
               'scope': 'Five extracted native CLI packages with required media/process/service/deletion scenarios and actual Linux archive upgrade/restore; source CI remains a separate prerequisite.'}
    m.require(not args.output.exists(), 'Verdict output must be new.')
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open('xb') as output:
        output.write(m.canonical(verdict))
    print(m.digest(m.canonical(verdict)))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, RuntimeError, KeyError, TypeError, StopIteration) as error:
        print('Native archive gate failed: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
