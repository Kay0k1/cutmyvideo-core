#!/usr/bin/env python3
"""Postwindow-only sequential content verification for one frozen 24-run cohort.

Authoring/importing starts nothing. Root must explicitly invoke this after the
formal timing window closes. Every native command uses the frozen baseline
runtime. Failed evidence/containers are retained; no retry or row replacement.
"""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
import time

ROOT = Path('/root/.cache/cutmy-first-draft-20261016')
REGISTRATION_SHA = '8f408fee2296afeba50a7aa48fc4cca2c031acbb467c746fdca4d9547d75a7e7'
BASELINE_IMAGE = 'sha256:4e794f76aeb89fd4838f5509bc1890fc2fce7196ea189e11282bb15eedbec96d'
DOCKER = ['docker', '--host', 'unix:///var/run/docker.sock']
TOOLS = ('/usr/bin/ffmpeg', '/usr/bin/ffprobe')
HELPERS = ('run-python.py', 'content-oracle.py', 'verify-content.py', 'full-decode.py', 'compare-final.py')
STRATA = ('ready-fresh-process', 'ready-primed-process')
SHA = re.compile(r'[0-9a-f]{64}\Z')
ENV = {'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
       'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8'}


class Refused(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def need(value, code):
    if not value: raise Refused(code)


def path(raw, exists=False):
    value = Path(raw)
    need(value.is_absolute() and value.is_relative_to(ROOT) and '..' not in value.parts, 'outside_owned_task')
    for ancestor in (value, *value.parents):
        if ancestor == ROOT.parent: break
        need(not ancestor.is_symlink(), 'symlink_owned_path')
    if exists: need(value.exists(), 'owned_file_missing')
    return value


def digest(raw, maximum=128 << 20):
    value = path(raw, True); before = value.stat()
    need(stat.S_ISREG(before.st_mode) and 0 < before.st_size <= maximum, 'bounded_regular_file_required')
    hashed = hashlib.sha256()
    with value.open('rb') as stream:
        for chunk in iter(lambda: stream.read(64 << 10), b''): hashed.update(chunk)
    after = value.stat()
    need((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) ==
         (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), 'evidence_changed_during_hash')
    return hashed.hexdigest()


def load(raw):
    value = path(raw, True); info = value.stat()
    need(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and info.st_mode & 0o077 == 0
         and 0 < info.st_size <= 4 << 20, 'bounded_private_json_required')
    return json.loads(value.read_bytes())


def save(raw, value):
    target = path(raw)
    need(target.parent.is_dir() and not target.exists(), 'new_owned_output_required')
    with target.open('x', encoding='utf-8') as stream:
        os.chmod(target, 0o600)
        json.dump(value, stream, ensure_ascii=True, sort_keys=True, allow_nan=False, indent=2)
        stream.write('\n'); stream.flush(); os.fsync(stream.fileno())
    fd = os.open(target.parent, os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)


def command(argv, timeout=30):
    # Docker metadata only. Bounded output; never print raw stdout/stderr.
    result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=timeout, env=ENV, check=False)
    need(len(result.stdout) <= 4 << 20 and len(result.stderr) <= 1 << 20, 'metadata_command_budget')
    return result


def frozen(manifest):
    for raw, expected in manifest['frozen_inputs'].items():
        need(digest(raw) == expected, 'preregistered_input_changed')
    image = command([*DOCKER, 'image', 'inspect', '--format', '{{.Id}}', BASELINE_IMAGE])
    need(image.returncode == 0 and image.stdout.decode().strip() == BASELINE_IMAGE, 'baseline_runtime_image_changed')


def schedule(manifest, records, timings):
    need(manifest.get('samples') == 24 and manifest.get('pairs_per_stratum') == 6 and
         manifest.get('strata') == list(STRATA) and manifest.get('protocol_version') == 4,
         'registered_twentyfour_scope_required')
    need(timings.get('status') == 'all24_timings_collected_content_pending' and timings.get('completed_runs') == 24
         and timings.get('native_oracles_executed_by_controller') is False and
         timings.get('registration_sha256') == REGISTRATION_SHA, 'formal_timing_not_closed')
    need(isinstance(records, list) and len(records) == 24 and len(timings.get('rows', [])) == 24,
         'all_registered_records_required')
    expected = []
    for stratum in STRATA:
        for pair in range(1, 7):
            for side in (('baseline', 'candidate') if pair % 2 else ('candidate', 'baseline')):
                token = 'fresh' if stratum == STRATA[0] else 'primed'
                expected.append((side, stratum, pair, f'{manifest["config"]["cohort_id"]}-{token}-p{pair:02}-{side}'))
    for record, public, (side, stratum, pair, sample) in zip(records, timings['rows'], expected):
        need(record['sample_id'] == sample and record['record'] == public and
             all(public[k] == v for k, v in [('side', side), ('stratum', stratum), ('pair', pair)]),
             'registered_record_order_changed')
        directory = ROOT / 'samples' / sample
        need(path(record['receipt']) == directory / 'sdkrun/public-receipt.json' and
             path(record['backend']) == directory / 'backend-result.json' and
             digest(record['receipt'], 4 << 20) == record['receipt_sha256'] and
             digest(record['backend'], 4 << 20) == record['backend_sha256'], 'timed_record_binding_changed')
        backend = load(record['backend']); receipt = load(record['receipt'])
        need(backend.get('status') == 'collected_and_stopped' and backend.get('ok') is True and
             backend.get('full30_workload_verified') is True and
             set(backend['resources_removed']) == {'containers', 'network', 'anchor', 'slice'} and
             all(v is True for v in backend['resources_removed'].values()), 'sample_backend_not_stopped')
        counts = backend['database_counts']
        need((counts['jobs'], counts['draft_jobs'], counts['final_jobs'], counts['artifacts'], counts['render_attempts']) ==
             (30, 15, 15, 30, 30) and counts['succeeded_jobs'] == 30, 'sample_full_thirty_missing')
        need(receipt.get('complete') is True and receipt['harness_sha256'] ==
             manifest['frozen_inputs'][manifest['config']['sdk_driver_path']], 'sample_driver_changed')
        input_path = directory / 'client-input.json'
        need(receipt['controller_input_sha256'] == digest(input_path, 4 << 20), 'sample_private_input_changed')
        record['input_path'] = str(input_path)  # Private in-memory reference, never public output.
    for name in HELPERS:
        need(str(ROOT / 'inputs' / name) in manifest['frozen_inputs'], 'quality_helper_not_preregistered')
    need(manifest['images']['baseline']['id'] == BASELINE_IMAGE, 'wrong_baseline_oracle_runtime')


def native(directory, ordinal, script, arguments, output_path):
    name = 'cutmyfirstdraftoracle' + secrets.token_hex(16)
    need(re.fullmatch(r'cutmyfirstdraftoracle[a-f0-9]{32}', name), 'owned_native_name_invalid')
    exists = command([*DOCKER, 'inspect', name])
    need(exists.returncode != 0, 'owned_native_container_exists')
    logs = directory / (ordinal + '-stdout.log'), directory / (ordinal + '-stderr.log')
    labels = {'cutmy.first-draft': '20261016', 'cutmy.first-draft.role': 'quality-oracle',
              'cutmy.first-draft.oracle': name,
              'com.docker.compose.project': 'cutmy-firstdraft-bench',
              'com.docker.compose.service': 'quality-oracle'}
    argv = [*DOCKER, 'run', '--pull=never', '--name', name]
    for key, value in labels.items(): argv.extend(['--label', key + '=' + value])
    argv.extend(['--network=none', '--user=0:0', '--read-only', '--cap-drop=ALL',
                 '--security-opt=no-new-privileges:true', '--cpus=1', '--memory=512m', '--memory-swap=512m',
                 '--pids-limit=128', '--tmpfs', '/tmp:rw,nosuid,nodev,noexec,size=134217728',
                 '--mount', 'type=bind,source=' + str(ROOT) + ',target=' + str(ROOT),
                 '--entrypoint', '/usr/bin/python3', BASELINE_IMAGE, '-I', '-B', str(script), *map(str, arguments)])
    intent = {'schema_version': 1, 'ordinal': ordinal, 'container_name': name, 'labels': labels,
              'image_id': BASELINE_IMAGE, 'argv': argv,
              'started_at_utc': datetime.datetime.now(datetime.timezone.utc).isoformat()}
    save(directory / (ordinal + '-intent.json'), intent)
    with logs[0].open('xb') as out, logs[1].open('xb') as err:
        process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=out, stderr=err, env=ENV)
        try:
            code = process.wait(timeout=950)
        except subprocess.TimeoutExpired:
            inspected = command([*DOCKER, 'inspect', name])
            need(inspected.returncode == 0, 'timedout_oracle_not_inspectable')
            value = json.loads(inspected.stdout)[0]
            need(value['Image'] == BASELINE_IMAGE and all(value['Config']['Labels'].get(k) == v for k, v in labels.items()),
                 'timedout_oracle_scope_changed')
            stopped = command([*DOCKER, 'stop', '--time=5', value['Id']], timeout=20)
            need(stopped.returncode == 0, 'timedout_owned_oracle_stop_failed')
            process.wait(timeout=30); code = 124
    inspected = command([*DOCKER, 'inspect', name])
    need(inspected.returncode == 0, 'oracle_receipt_inspection_failed')
    value = json.loads(inspected.stdout)[0]
    need(value['Name'] == '/' + name and value['Image'] == BASELINE_IMAGE and
         all(value['Config']['Labels'].get(k) == v for k, v in labels.items()) and
         value['State']['Running'] is False, 'owned_oracle_scope_changed')
    ok = code == value['State']['ExitCode'] == 0 and value['State']['OOMKilled'] is False
    result = {**intent, 'exit_code': code, 'docker_exit_code': value['State']['ExitCode'],
              'oom_killed': value['State']['OOMKilled'], 'container_id': value['Id'], 'passed': ok,
              'stdout_sha256': digest(logs[0], 4 << 20) if logs[0].stat().st_size else None,
              'stderr_sha256': digest(logs[1], 4 << 20) if logs[1].stat().st_size else None,
              'container_removed': False}
    if ok:
        need(path(output_path).is_file(), 'successful_oracle_receipt_missing')
        result['output_sha256'] = digest(output_path, 4 << 20)
        removed = command([*DOCKER, 'rm', value['Id']])
        need(removed.returncode == 0, 'owned_oracle_remove_failed')
        result['container_removed'] = True
    save(directory / (ordinal + '-execution.json'), result)
    need(ok, 'native_quality_failed_evidence_preserved')
    return result


def run_locked(args):
    need(os.geteuid() == 0, 'owned_root_operator_required')
    registration = path(args.registration, True)
    need(digest(registration, 4 << 20) == REGISTRATION_SHA, 'exact_preregistration_required')
    manifest = load(registration)
    need(args.closed_window == manifest['config']['window_id'], 'explicit_closed_window_required')
    cohort = ROOT / 'proofs' / ('cohort-' + manifest['config']['cohort_id'])
    records_path, timing_path = cohort / 'private-records.json', cohort / 'PUBLIC-TIMING-RESULT.json'
    records, timings = load(records_path), load(timing_path)
    schedule(manifest, records, timings)
    frozen(manifest)
    directory = cohort / 'native-quality'
    need(not directory.exists(), 'new_postwindow_quality_directory_required')
    directory.mkdir(mode=0o700)
    save(directory / 'INPUT-BINDING.json', {'registration_sha256': REGISTRATION_SHA,
         'timing_result_sha256': digest(timing_path, 4 << 20), 'private_records_sha256': digest(records_path, 4 << 20),
         'orchestrator_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), 'native_window_after_formal_timing': True})
    completed, comparisons = [], []
    try:
        for index, record in enumerate(records):
            frozen(manifest)
            sample = ROOT / 'samples' / record['sample_id']; oracle = sample / 'sdkrun/content-oracle-receipt.json'
            need(not oracle.exists() and not (sample / 'sdkrun/full-decoded-fingerprints.json').exists(),
                 'existing_oracle_refused_no_retry')
            native(directory, f'run-{index + 1:02}', ROOT / 'inputs/verify-content.py', [record['input_path']], oracle)
            proof = load(oracle)
            need(proof.get('passed') is True and proof.get('full_video_audio_decoded_outputs') == 30 and
                 proof['timed_receipt_sha256'] == record['receipt_sha256'] and
                 proof['verifier_sha256'] == manifest['frozen_inputs'][str(ROOT / 'inputs/verify-content.py')]
                 and proof['full_decode_helper_sha256'] == manifest['frozen_inputs'][str(ROOT / 'inputs/full-decode.py')]
                 and proof['frozen_comparator_sha256'] == manifest['frozen_inputs'][str(ROOT / 'inputs/content-oracle.py')],
                 'full_decode_receipt_binding_changed')
            for tool in TOOLS:
                need(proof['tool_binding'][Path(tool).name]['executable_sha256'] ==
                     manifest['images']['baseline']['binding']['tool_sha256'][tool], 'baseline_family_tool_changed')
            completed.append({'ordinal': index + 1, 'side': record['record']['side'],
                              'stratum': record['record']['stratum'], 'pair': record['record']['pair'],
                              'sample_identity_sha256': hashlib.sha256(record['sample_id'].encode()).hexdigest(),
                              'timed_receipt_sha256': record['receipt_sha256'],
                              'backend_receipt_sha256': record['backend_sha256'],
                              'oracle_sha256': digest(oracle, 4 << 20),
                              'decoded_content_aggregate_sha256': proof['decoded_content_aggregate_sha256'],
                              'full_video_audio_decoded_outputs': 30, 'stages': proof['stages']})
            save(directory / f'COMPLETED-{index + 1:02}.json', completed[-1])
            print(json.dumps({'milestone': 'postwindow_oracle_complete', 'completed_samples': len(completed),
                              'decoded_outputs': len(completed) * 30}), flush=True)
        frozen(manifest)
        pilot_input = ROOT / 'samples/pilot-baseline-fresh-02/client-input.json'
        first = next(r for r in records if r['record']['side'] == 'baseline' and r['record']['stratum'] == STRATA[0]
                     and r['record']['pair'] == 1)
        repeat_output = ROOT / 'proofs' / ('baseline-repeat-' + manifest['config']['cohort_id'] + '.json')
        need(not repeat_output.exists(), 'new_baseline_repeat_proof_required')
        native(directory, 'baseline-repeat', ROOT / 'inputs/compare-final.py',
               [pilot_input, first['input_path'], repeat_output], repeat_output)
        repeat = load(repeat_output)
        need(repeat.get('passed') is True and repeat.get('roles') == ['baseline', 'baseline'] and
             repeat.get('final_clips') == 15, 'baseline_fixture_not_deterministic')
        for stratum in STRATA:
            for pair in range(1, 7):
                frozen(manifest)
                selected = {r['record']['side']: r for r in records if r['record']['stratum'] == stratum and r['record']['pair'] == pair}
                token = 'fresh' if stratum == STRATA[0] else 'primed'
                output = ROOT / 'proofs' / (manifest['config']['cohort_id'] + '-' + token + '-pair' + str(pair) + '.json')
                need(not output.exists(), 'new_pair_receipt_required')
                native(directory, token + '-pair' + str(pair), ROOT / 'inputs/compare-final.py',
                       [selected['baseline']['input_path'], selected['candidate']['input_path'], output], output)
                proof = load(output)
                need(proof.get('passed') is True and proof.get('roles') == ['baseline', 'candidate'] and
                     proof.get('final_clips') == 15 and proof.get('exact_decoded_video_audio_equal') is True and
                     proof.get('exact_packet_clocks_payloads_equal') is True and proof.get('geometry_and_codec_metadata_equal') is True,
                     'paired_final_quality_failed')
                comparisons.append({'stratum': stratum, 'pair': pair, 'receipt_sha256': digest(output, 4 << 20),
                                    'final_clips': 15, 'encoded_file_hashes_equal': proof['encoded_file_hashes_equal'],
                                    'decoded_final_aggregate_sha256': proof['decoded_final_aggregate_sha256']})
        frozen(manifest)
        result = {'schema_version': 1, 'record_kind': 'completed_postwindow_cohort_quality', 'passed': True,
                  'registration_sha256': REGISTRATION_SHA, 'timing_result_sha256': digest(timing_path, 4 << 20),
                  'orchestrator_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                  'samples': 24, 'full_video_audio_decoded_outputs': 720, 'paired_comparisons': 12,
                  'paired_final_outputs': 180, 'baseline_repeat_qualification_passed': True,
                  'baseline_repeat_receipt_sha256': digest(repeat_output, 4 << 20),
                  'original_content_thresholds_unchanged': True, 'baseline_runtime_image_sha256': BASELINE_IMAGE,
                  'oracle_helper_sha256': {name: manifest['frozen_inputs'][str(ROOT / 'inputs' / name)] for name in HELPERS},
                  'all_owned_success_containers_removed': True, 'native_oracles_after_formal_window': True,
                  'decoded_samples': completed, 'final_pairs': comparisons,
                  'performance_claim_ready': False, 'time_memory_strict_acceptance_still_independent': True}
        save(cohort / 'PUBLIC-QUALITY-RESULT.json', result)
        print(json.dumps({'passed': True, 'samples': 24, 'decoded_outputs': 720, 'paired_finals': 180,
                          'performance_claim_ready': False}), flush=True)
    except BaseException as error:
        code = getattr(error, 'code', 'quality_operation_failed')
        if not isinstance(code, str) or not re.fullmatch(r'[a-z0-9_]{1,100}', code): code = 'quality_operation_failed'
        save(directory / 'FAILED.json', {'passed': False, 'code': code, 'completed_oracles': len(completed),
                                        'completed_pairs': len(comparisons), 'no_retry_or_replacement': True,
                                        'failed_container_logs_and_receipts_retained': True, 'performance_claim_ready': False})
        raise


def main(args):
    # The same lock as the formal controller covers the ENTIRE native window.
    # A caller cannot accidentally overlap a new cohort with postwindow decode.
    lock_path = path(ROOT / '.first-draft-cohort-window.lock')
    descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'a') as lock:
        info = lock_path.stat()
        need(info.st_uid == os.geteuid() and info.st_mode & 0o077 == 0, 'private_window_lock_required')
        try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: raise Refused('owned_timing_or_native_window_still_active') from None
        run_locked(args)


if __name__ == '__main__':
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--registration', type=Path, required=True)
    parser.add_argument('--closed-window', required=True)
    args = parser.parse_args()
    try: main(args)
    except BaseException as error:
        code = getattr(error, 'code', 'quality_orchestration_failed')
        if not isinstance(code, str) or not re.fullmatch(r'[a-z0-9_]{1,100}', code): code = 'quality_orchestration_failed'
        print(json.dumps({'passed': False, 'code': code, 'performance_claim_ready': False}), file=sys.stderr)
        raise SystemExit(1)
