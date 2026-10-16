"""Synthetic metadata controls only. Any child-process creation is forbidden."""
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import statistics
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location('render_v6', Path(__file__).with_name('render-v6-report.py'))
M = importlib.util.module_from_spec(spec); spec.loader.exec_module(M)


def sha(value):
    return hashlib.sha256(value if isinstance(value, bytes) else value.encode()).hexdigest()


class Controls(unittest.TestCase):
    def setUp(self):
        self.no_process = patch.object(subprocess, 'Popen', side_effect=AssertionError('no process permitted'))
        self.no_process.start(); self.tmp = tempfile.TemporaryDirectory(prefix='cutmy-v6-report-synthetic-')
        self.root = Path(self.tmp.name); self.patches = []; self.files = {}
        self.override('ROOT', self.root)
        self.helper_shas = {n: self.write(self.root / 'inputs' / n, ('SYNTHETIC NEVER EXECUTE ' + n).encode())
                            for n in M.HELPERS}
        self.override('HELPERS', self.helper_shas)
        self.source = self.write(self.root / 'inputs/interview.mp4', b'SYNTHETIC NOT MEDIA')
        self.override('SOURCE_SHA', self.source); self.override('SOURCE_SIZE', len(b'SYNTHETIC NOT MEDIA'))
        wheel = io.BytesIO(); package = self.root / 'sdk/lib/python3.11/site-packages/cutmyvideo'
        with zipfile.ZipFile(wheel, 'w') as archive:
            archive.writestr('cutmyvideo/__init__.py', b'# SYNTHETIC NEVER IMPORT\n')
            archive.writestr('cutmyvideo-1.4.0.dist-info/METADATA', b'Name: cutmyvideo\nVersion: 1.4.0\n')
        self.write(package / '__init__.py', b'# SYNTHETIC NEVER IMPORT\n')
        self.override('SDK_PAYLOAD', M.key({'cutmyvideo/__init__.py': sha(b'# SYNTHETIC NEVER IMPORT\n')}))
        self.wheel = self.write(self.root / 'inputs/cutmyvideo-1.4.0-py3-none-any.whl', wheel.getvalue())
        self.override('WHEEL_SHA', self.wheel)
        self.plan = {'version': 1, 'options': {'format': 'mp4', 'quality': 'best', 'cut_mode': 'accurate'},
                     'clips': [{'id': f'clip-{i:02}', 'label': 'Cyrillic label: тема',
                                'ranges': [{'start_ms': i * 30000, 'end_ms': i * 30000 + 6000, 'label': 'first'},
                                           {'start_ms': i * 30000 + 8000, 'end_ms': i * 30000 + 11000, 'label': 'second'}]}
                               for i in range(15)]}
        fixture = self.write(self.root / 'inputs/requests.json', {'initial': {'plan': self.plan}})
        self.override('FIXTURE_SHA', fixture)
        self.tools = {n: {'executable_sha256': value, 'version_stdout_sha256': sha(n + 'VERSION')}
                      for n, value in M.TOOLS.items()}
        self.env = M.environment(self.tools); self.fps = {}
        for stage in ('draft', 'final'):
            for i in range(15):
                fp = {n: sha(stage + str(i) + n) if n.endswith('_sha256') else 100 for n in M.FP_FIELDS}
                fp['structure'] = {'streams': [{'codec_type': 'video', 'width': 1280, 'height': 720}, {'codec_type': 'audio'}]}
                fp['media_structure_sha256'] = M.key(fp['structure']); self.fps[stage, i] = fp
        self.prior = self.sample('qualified-baseline-pilot', 'baseline', M.STRATA[0], M.BASELINE_COMMIT)
        self.certificates, self.prior_private, oracle_sha = self.native(self.prior)
        self.override('PRIOR_ORACLES', {oracle_sha})
        self.cache = {}; M.insert(self.cache, self.certificates, 'prior')
        self.inventory_path = self.root / 'proofs/inventory.json'
        self.inventory = {'schema_version': 1, 'kind': 'prospective_content_addressed_quality_inventory',
            'cohort_id': 'synthetic-v6', 'frozen_at_utc': '2026-10-16T12:00:00+00:00',
            'algorithm_sha256': self.helper_shas['quality-reuse-v6.py'],
            'native_orchestrator_sha256': self.helper_shas['run-cohort-oracles.py'],
            'source_sha256': self.source, 'fixture_sha256': fixture,
            'original_helpers': {n: self.helper_shas[n] for n in M.ORIGINAL}, 'baseline_runtime_image_id': M.BASELINE_IMAGE,
            'seeds': [{'input': str(self.prior['input']), 'input_sha256': self.d(self.prior['input']),
                'timed_receipt_sha256': self.prior['timed_sha256'], 'oracle_sha256': oracle_sha,
                'certificates_sha256': M.key(self.certificates)}], 'distinct_source_certificates': len(self.cache),
            'certificate_aggregate_sha256': M.key([self.cache[n] for n in sorted(self.cache)]),
            'prospective_only_no_existing_cohort_reclassification': True}
        inv_sha = self.write(self.inventory_path, self.inventory)
        self.cohort = self.root / 'proofs/cohort-synthetic-v6'; self.qdir = self.cohort / 'content-addressed-quality'
        self.mode = {'mode': 'content_addressed_v1', 'runner_sha256': self.helper_shas['quality-reuse-v6.py'],
                     'inventory_path': str(self.inventory_path), 'inventory_sha256': inv_sha,
                     'native_orchestrator_sha256': self.helper_shas['run-cohort-oracles.py']}
        images = {}
        for side in M.SIDES:
            commit = M.BASELINE_COMMIT if side == 'baseline' else 'b' * 40
            image_id = M.BASELINE_IMAGE if side == 'baseline' else 'sha256:' + 'b' * 64
            binding = {'version': {'commit': commit, 'os': 'linux', 'arch': 'amd64', 'go_version': 'go1.27.2'},
                       'image_id': image_id, 'sdk_harness_sha256': self.helper_shas['run-python.py'],
                       'tool_sha256': {'/usr/bin/' + n: h for n, h in M.TOOLS.items()}}
            binding['tool_sha256']['/usr/local/bin/cutmy'] = M.BASELINE_CLI if side == 'baseline' else 'b' * 64
            images[side] = {'id': image_id, 'commit': commit, 'reference': 'SYNTHETIC-' + side,
                           'binding': binding, 'layers': ['sha256:' + '1' * 64] + (['sha256:' + '2' * 64] if side == 'candidate' else [])}
        base_binding = self.root / 'proofs/baseline-binding.json'; self.write(base_binding, images['baseline']['binding'])
        candidate_binding = self.root / 'proofs/candidate-binding.json'; self.write(candidate_binding, images['candidate']['binding'])
        build = self.root / 'proofs/build.json'; self.write(build, {'commit': 'b' * 40, 'image': 'SYNTHETIC-candidate',
             'image_id': images['candidate']['id'], 'baseline_runtime_base_image': M.BASELINE_IMAGE,
             'timed_workload_overlapped': False, 'source_archive_sha256': 'c' * 64, 'dockerfile_sha256': 'd' * 64})
        decision_path = self.root / 'MEMORY-PROTOCOL-V6-DECISION.json'
        decision_bytes = Path(__file__).with_name('MEMORY-PROTOCOL-V6-DECISION.json').read_bytes()
        decision_sha = self.write(decision_path, decision_bytes); decision = json.loads(decision_bytes)
        self.manifest = {'schema_version': 1, 'kind': 'first_draft_preregistration', 'protocol_version': 6,
            'samples': 24, 'pairs_per_stratum': 6, 'strata': list(M.STRATA), 'registered_at_utc': '2026-10-16T12:01:00+00:00',
            'config': {'cohort_id': 'synthetic-v6', 'window_id': 'synthetic-v6-window', 'candidate_commit': 'b' * 40, 'baseline_binding': str(base_binding),
                'candidate_binding': str(candidate_binding), 'build_provenance': str(build), 'candidate_image': 'SYNTHETIC-candidate',
                'pg_image_id': 'sha256:' + 'c' * 64, 'memory_protocol_decision_path': str(decision_path),
                'memory_protocol_decision_sha256': decision_sha, 'coordinator_launcher_path': str(self.root / 'inputs/launch-cohort.py'),
                'coordinator_launcher_sha256': self.helper_shas['launch-cohort.py']}, 'images': images, 'limits': M.LIMITS, 'application_config': M.APP,
            'network_internal': False, 'prospective_quality': self.mode, 'frozen_inputs': dict(self.files),
            'production_before_registration': {'roles': {role: {'healthy': True, 'id': 'PRIVATE_' + role}
                                                        for role in ('postgres', 'api', 'worker', 'web', 'edge')}},
            'metric_gates': {'first_draft_candidate_over_baseline_median_max': .8, 'first_draft_faster_pairs_min': 5,
                'all_drafts_final_phase_whole_cycle_ratio_median_max': 1.0,
                'actual_kernel_common_parent_peak_ratio_median_max': .98,
                'maximum_candidate_common_parent_peak_no_greater_than_maximum_baseline': True},
            'diagnostic_gates': {'actual_kernel_parent_api_worker_pg_client_peak_ratio_median_max': 1.0,
                'maximum_candidate_kernel_peak_no_greater_than_maximum_baseline': True, 'acceptance_blocking': False},
            'memory_protocol_decision': {'path': str(decision_path), 'sha256': decision_sha,
                'decided_at_utc': decision['decided_at_utc'], 'original_registration_sha256': M.OLD_REGISTRATION,
                'original_negative_witness_sha256': M.OLD_NEGATIVE_WITNESS},
            'coordinator_instrumentation': {'kind': 'detached_owned_systemd_coordinator', 'outside_measured_parents': True,
                'memory_max_bytes': 128 << 20, 'memory_swap_max_bytes': 0, 'cpu_quota_percent': 10, 'tasks_max': 64,
                'runtime_max_seconds': 14400, 'remain_after_exit': True, 'stdout_file_max_bytes': 8 << 20,
                'launcher_sha256': self.helper_shas['launch-cohort.py']},
            'negative_only_futility': {'predeclared': True, 'after_valid_stopped_fresh_pairs': 6, 'after_completed_runs': 12,
                'only_if_any_registered_v6_fresh_acceptance_gate_fails': True, 'diagnostic_role_failures_trigger_futility': False,
                'no_primed_sample_starts_on_negative': True,
                'positive_early_acceptance': False, 'resume_replacement_pooling_allowed': False}}
        # Freeze only immutable source/config inventory, not historical samples.
        self.manifest['frozen_inputs'] = {k: v for k, v in self.files.items()
            if str(self.root / 'inputs') in k or k in (str(self.inventory_path), str(base_binding), str(candidate_binding), str(build), str(decision_path))}
        self.reg_path = self.root / 'proofs/registration.json'; self.reg_sha = self.write(self.reg_path, self.manifest)
        self.args = SimpleNamespace(registration=self.reg_path, expected_registration_sha256=self.reg_sha)
        self.rows = []; self.records = []; self.samples = []; self.views = {}; self.summaries = []
        for index, (stratum, pair, side) in enumerate(M.order(), 1):
            name = f'synthetic-v6-{"fresh" if stratum == M.STRATA[0] else "primed"}-p{pair:02}-{side}'
            sample = self.sample(name, side, stratum, images[side]['commit']); self.samples.append(sample)
            peaks = {r: (100 if r == 'parent' else 8) * (1 << 20) for r in M.ROLES}
            if side == 'candidate': peaks['parent'] = 96 << 20; peaks['pg'] = 9 << 20
            backend = self.backend(sample['public'], peaks, side)
            backend_path = self.root / 'samples' / name / 'backend-result.json'; backend_sha = self.write(backend_path, backend)
            row = {'side': side, 'stratum': stratum, 'pair': pair,
                   'timings_seconds': {n: sample['public'][n] for n in M.TIMES}, 'actual_kernel_peak_bytes': peaks,
                   'all30_database_counts_passed': True, 'owned_cleanup_passed': True, 'production_unchanged': True}
            self.rows.append(row); self.records.append({'sample_id': name, 'receipt': str(sample['directory'] / 'public-receipt.json'),
                'receipt_sha256': sample['timed_sha256'], 'backend': str(backend_path), 'backend_sha256': backend_sha, 'record': row,
                'host_oom_before': {'boot_id': '11111111-1111-1111-1111-111111111111', 'oom_kill': 0},
                'host_oom_after': {'boot_id': '11111111-1111-1111-1111-111111111111', 'oom_kill': 0}})
            view, mappings = self.materialize(sample)
            proof = {'schema_version': 1, 'kind': 'content_addressed_sample_qualification', 'passed': True,
                'timed_receipt_sha256': sample['timed_sha256'], 'backend_receipt_sha256': backend_sha,
                'outputs_freshly_hashed': 30, 'current_request_manifest_checks': 30, 'full_output_decodes_executed_now': 0,
                'qualifications': mappings, 'exact_native_environment': self.env,
                'current_bound_document_digests': sample['bound_document_digests'], 'native_decoding_not_repeated_for_reused_bytes': True}
            proof_sha = self.write(self.qdir / f'SAMPLE-{index:02}.json', proof)
            self.write(self.qdir / f'FINGERPRINTS-{index:02}.json', view); self.views[stratum, pair, side] = view
            self.summaries.append({'ordinal': index, 'side': side, 'stratum': stratum, 'pair': pair,
                'qualification_receipt_sha256': proof_sha, 'timed_receipt_sha256': sample['timed_sha256'],
                'backend_receipt_sha256': backend_sha, 'outputs_freshly_hashed': 30, 'current_request_manifest_checks': 30,
                'full_output_decodes_executed_now': 0})
        self.timing = {'status': 'all24_timings_collected_content_pending', 'completed_runs': 24, 'registration_sha256': self.reg_sha,
                       'rows': self.rows, 'native_oracles_executed_by_controller': False, 'performance_claim_ready': False,
                       'all_failed_adverse_rows_retained': True}
        self.coordinator_fixture()
        timing_sha = self.write(self.cohort / 'PUBLIC-TIMING-RESULT.json', self.timing)
        records_sha = self.write(self.cohort / 'private-records.json', self.records)
        self.quality = {'kind': 'prospective_content_addressed_cohort_quality', 'passed': True, 'registration_sha256': self.reg_sha,
            'timing_result_sha256': timing_sha, 'private_records_sha256': records_sha, 'inventory_sha256': self.mode['inventory_sha256'],
            'runner_sha256': self.helper_shas['quality-reuse-v6.py'], 'baseline_runtime_image_id': M.BASELINE_IMAGE,
            'original_helper_sha256': {n: self.helper_shas[n] for n in M.ORIGINAL}, 'samples_receipts': self.summaries,
            'samples': 24, 'outputs_freshly_hashed': 720, 'current_request_manifest_checks': 720, 'final_target_file_rehash_checks': 720,
            'logical_final_pairs': 12, 'logical_final_output_comparisons': 180, 'full_output_decode_executions_now': 0,
            'whole_sample_native_verifier_invocations_now': 0, 'additional_target_full_file_hash_passes_after_native': 0,
            'prior_certificate_reuse_outputs': 720, 'current_run_certificate_reuse_outputs': 0, 'distinct_encoded_objects_seen': 30,
            'baseline_repeat_passed': True, 'all720_qualified_but_not_claimed_as720_native_executions': True,
            'performance_claim_ready': False, 'strict_time_memory_gates_independent': True}
        final_pairs = []
        for index, (s, p) in enumerate(((s, p) for s in M.STRATA for p in range(1, 7)), 1):
            result = {'stratum': s, 'pair': p, **M.compare(self.views[s, p, 'baseline'], self.views[s, p, 'candidate'])}
            self.write(self.qdir / f'PAIR-{index:02}.json', result); final_pairs.append(result)
        self.quality['final_pairs'] = final_pairs
        repeat = M.compare(self.prior_private, self.views[M.STRATA[0], 1, 'baseline'])
        self.quality['baseline_repeat_receipt_sha256'] = self.write(self.qdir / 'BASELINE-REPEAT.json', repeat)
        self.write(self.qdir / 'INPUT-BINDING.json', {n: self.quality[n] for n in
             ('registration_sha256', 'timing_result_sha256', 'private_records_sha256', 'inventory_sha256', 'runner_sha256')})
        self.write(self.qdir / 'PUBLIC-QUALITY-REUSE-RESULT.json', self.quality)

    def override(self, name, value):
        p = patch.object(M, name, value); p.start(); self.patches.append(p)

    def tearDown(self):
        for p in reversed(self.patches): p.stop()
        self.tmp.cleanup(); self.no_process.stop()

    def write(self, path, value):
        path = Path(path); path.parent.mkdir(parents=True, exist_ok=True)
        raw = value if isinstance(value, bytes) else M.canonical(value) + b'\n'
        path.write_bytes(raw); path.chmod(0o600); digest = sha(raw); self.files[str(path)] = digest
        return digest

    def read(self, path): return json.loads(Path(path).read_bytes())
    def d(self, path): return sha(Path(path).read_bytes())

    def sample(self, name, side, stratum, commit):
        directory = self.root / 'samples' / name / 'sdkrun'; input_path = directory.parent / 'client-input.json'
        config = {'run_dir': str(directory), 'role': side, 'server_commit': commit, 'cache_stratum': stratum,
                  'base_url': 'http://PRIVATE_ENDPOINT', 'require_client_cgroup': True, 'cleanup_source': False}
        input_sha = self.write(input_path, config); requests = {}; outputs = []
        public = {'schema_version': 1, 'record_kind': 'installed_sdk_first_download_draft_measurement',
            'complete': True, 'controller_input_sha256': input_sha, 'harness_sha256': self.helper_shas['run-python.py'],
            'sdk_version': '1.4.0', 'source_size_bytes': M.SOURCE_SIZE, 'clips': 15, 'ranges_per_clip': 2,
            'requested_duration_ms_per_clip': 9000,
            'source_sha256': self.source, 'fixture_sha256': M.FIXTURE_SHA, 'wheel_sha256': self.wheel,
            'installed_module_payload_sha256': M.SDK_PAYLOAD, 'owner_history_observed_jobs': 30, 'draft_then_final_unique_jobs': 30,
            'role': side, 'expected_server_commit_from_controller': commit, 'cache_stratum': stratum,
            'client_costs_in_owned_shared_cgroup': True, 'client_cgroup_peak_bytes_after_timed_cycle': 8 << 20,
            'fault_injection': False, 'production_operation': False, 'source_preparation_excluded_from_first_download': True,
            'whole30_clock_excludes_source_preparation_and_priming': True, 'first_output_clock_includes_consumer_rehash': True,
            'draft_files_retained_unchanged': True, 'full_fifteen_prime_count': int(stratum == M.STRATA[1]),
            'poll_interval_seconds': 1, 'outstanding_admission_limit': 3, 'retained_output_byte_budget': 256 << 20,
            M.TIMES[0]: 70.0 if side == 'candidate' else 100.0, M.TIMES[1]: 200.0, M.TIMES[2]: 200.0,
            M.TIMES[3]: 400.01, 'draft_to_final_handoff_seconds': .01}
        for stage in ('draft', 'final'):
            rows = []
            for i, clip in enumerate(self.plan['clips']):
                cursor = 0; segments = []
                for j, r in enumerate(clip['ranges']):
                    segments.append({'index': j, 'label': r['label'], 'source_start_ms': r['start_ms'], 'source_end_ms': r['end_ms'],
                        'output_start_ms': cursor, 'output_end_ms': cursor + r['end_ms'] - r['start_ms']}); cursor = segments[-1]['output_end_ms']
                out = {'clip_id': clip['id'], 'job_id': f'PRIVATE_JOB_{name}_{stage}_{i}',
                    'artifact_id': f'PRIVATE_ARTIFACT_{name}_{stage}_{i}', 'path': str(directory / 'collection/files' / f'{stage}-{i}.mp4'),
                    'sha256': sha(stage + str(i) + 'ENCODED'), 'size_bytes': 8 + i, 'segments': segments,
                    'track_manifest': [{'index': 0, 'type': 'video', 'codec': 'h264', 'included': True, 'default': True},
                                       {'index': 1, 'type': 'audio', 'codec': 'aac', 'included': True, 'default': True}]}
                rows.append(out); outputs.append({'output': out, 'intent': M.intent(self.plan, clip, stage),
                    'tracks': out['track_manifest'], 'rendition': stage, 'ordinal': i})
            requests[stage] = {'source_id': 'PRIVATE_SOURCE_' + name, 'plan': self.plan, 'rendition': stage}
            self.write(directory / f'{stage}-private-receipt.json', {'complete': True, 'pending': [], 'source_id': requests[stage]['source_id'],
                'rendition': stage, 'outputs': rows})
            public[stage] = {'outputs': [{n: out[n] for n in ('sha256', 'size_bytes')} for out in rows],
                'first_verified_download_ms': public[M.TIMES[0]] * 1000 if stage == 'draft' else 100000.0,
                'all_verified_downloads_ms': 200000.0, 'first_output': {n: rows[0][n] for n in ('sha256', 'size_bytes')}}
        self.write(directory / 'requests.json', requests); timed_sha = self.write(directory / 'public-receipt.json', public)
        bound = [input_path, directory / 'public-receipt.json', directory / 'requests.json',
                 directory / 'draft-private-receipt.json', directory / 'final-private-receipt.json']
        return {'input': input_path, 'directory': directory, 'public': public, 'timed_sha256': timed_sha,
                'rows': outputs, 'bound_document_digests': [self.d(p) for p in bound]}

    def native(self, sample):
        decoded = []
        for row in sample['rows']:
            out = row['output']; decoded.append({'rendition': row['rendition'], 'clip_id': out['clip_id'], 'path': out['path'],
                'encoded_file_sha256': out['sha256'], 'encoded_size_bytes': out['size_bytes'], **self.fps[row['rendition'], row['ordinal']]})
        private = {'schema_version': 1, 'complete': True, 'timed_receipt_sha256': sample['timed_sha256'], 'source_sha256': self.source,
            'fixture_sha256': M.FIXTURE_SHA, 'tool_binding': self.tools, 'outputs': decoded}
        private_sha = self.write(sample['directory'] / 'full-decoded-fingerprints.json', private)
        oracle = {'schema_version': 1, 'record_kind': 'independent_fifteen_draft_then_final_content_oracle',
            'passed': True, 'private_fingerprints_sha256': private_sha, 'timed_receipt_sha256': sample['timed_sha256'],
            'source_sha256': self.source, 'fixture_sha256': M.FIXTURE_SHA, 'full_video_audio_decoded_outputs': 30,
            'verifier_sha256': self.helper_shas['verify-content.py'], 'full_decode_helper_sha256': self.helper_shas['full-decode.py'],
            'frozen_comparator_sha256': self.helper_shas['content-oracle.py'], 'frame_mae_limit': 12.0, 'audio_correlation_minimum': .75,
            'source_reference_times_come_from_frozen_caller_ranges': True, 'sdk_timing_completed_before_decode': True,
            'tool_binding': self.tools, 'stages': [{'rendition': stage, 'clips': 15, 'segments': 30, 'frame_samples': 60,
                'max_frame_mae': 1.0, 'audio_samples': 30, 'silent_segments': 0, 'min_audio_correlation': .99} for stage in ('draft', 'final')],
            'decoded_content_aggregate_sha256': M.key([{'rendition': row['rendition'], 'ordinal': i % 15,
                **{n: row[n] for n in M.FP_FIELDS}} for i, row in enumerate(decoded)])}
        oracle_sha = self.write(sample['directory'] / 'content-oracle-receipt.json', oracle)
        certs, observed = M.native_certificates(M.Evidence(), sample, oracle_sha)
        return certs, observed, oracle_sha

    def materialize(self, sample):
        out = []; mappings = []
        for row in sample['rows']:
            value = row['output']; decode, source = M.certificate_keys(value, row['intent'], self.env); cert = self.cache[source]
            out.append({'rendition': row['rendition'], 'clip_id': value['clip_id'], 'encoded_file_sha256': value['sha256'],
                        'encoded_size_bytes': value['size_bytes'], **cert['fingerprint']})
            mappings.append({'rendition': row['rendition'], 'ordinal': row['ordinal'], 'encoded_sha256': value['sha256'],
                'size_bytes': value['size_bytes'], 'decode_key': decode, 'source_key': source,
                'qualification_oracle_sha256': cert['oracle_sha256'], 'certificate_origin': cert['origin']})
        return {'tool_binding': self.tools, 'outputs': out}, mappings

    def backend(self, public, peaks, side):
        def kernel(role):
            value = {'memory.events': 'max 0\noom 0\noom_kill 0\n', 'memory_peak_bytes': peaks[role], 'memory.swap.max': '0'}
            if role == 'parent': value.update({'cpu.max': '150000 100000', 'memory.max': '1610612736', 'cpu.weight': '10', 'pids.max': '512'})
            else: value.update({'cpu.max': {'api': '50000 100000', 'worker': '100000 100000', 'pg': '25000 100000'}[role],
                'memory.max': str(M.LIMITS[role]['memory_bytes']), 'pids.max': '128'})
            return value
        production = copy.deepcopy(self.manifest['production_before_registration'])
        roles = {r: {'healthy': True, 'kernel': kernel(r)} for r in ('api', 'worker', 'pg')}
        roles['client'] = {'kernel_memory_peak_bytes': peaks['client'], 'unit': {'MemoryPeak': str(peaks['client']),
            'ExecMainStatus': '0', 'Result': 'success', 'SubState': 'exited'}}
        return {'schema_version': 1, 'kind': 'first_draft_backend_resource_receipt',
            'ok': True, 'status': 'collected_and_stopped', 'full30_workload_verified': True,
            'resources_removed': {r: True for r in ('containers', 'network', 'anchor', 'slice')}, 'database_counts': dict(M.COUNTS),
            'limits': M.LIMITS, 'application_config': M.APP, 'core_image_id': self.manifest['images'][side]['id'],
            'pg_image_id': self.manifest['config']['pg_image_id'], 'protocol_sha256': self.reg_sha, 'network_internal': False,
            'production_before': production, 'production_before_cleanup': production, 'production_after_cleanup': production,
            'resources_before_stop': {'parent': kernel('parent'), 'roles': roles}, 'parent_after_container_stop': kernel('parent')}

    def update_quality(self): self.write(self.qdir / 'PUBLIC-QUALITY-REUSE-RESULT.json', self.quality)

    def coordinator_fixture(self):
        nonce = 'a' * 32; name = 'cutmyfirstdraftcoordinator' + nonce + '.service'
        self.coordinator_dir = self.root / 'proofs' / ('coordinator-' + nonce)
        intent = {'kind': 'immutable_owned_coordinator_launch_intent', 'nonce': nonce, 'unit_name': name,
            'description': f'cutmy.first-draft=20261016 coordinator={nonce} registration={self.reg_sha}',
            'cohort_id': 'synthetic-v6', 'window': self.manifest['config']['window_id'], 'registration_path': str(self.reg_path),
            'registration_sha256': self.reg_sha, 'controller_path': str(self.root / 'inputs/controller-cohort-v6.py'),
            'controller_sha256': self.helper_shas['controller-cohort-v6.py'], 'launcher_sha256': self.helper_shas['launch-cohort.py'],
            'policy': self.manifest['coordinator_instrumentation'], 'log_path': str(self.coordinator_dir / 'coordinator.log')}
        intent_sha = self.write(self.root / 'proofs/COORDINATOR-synthetic-v6-LAUNCH.json', intent)
        actual = {'kind': 'actual_detached_coordinator_runtime', 'protocol_version': 6, 'launch_intent_sha256': intent_sha,
            'registration_sha256': self.reg_sha, 'controller_sha256': self.helper_shas['controller-cohort-v6.py'],
            'launcher_sha256': self.helper_shas['launch-cohort.py'], 'unit_name': name, 'invocation_id': 'd' * 32,
            'cgroup': '/sys/fs/cgroup/system.slice/' + name, 'actual_fsize_limits': [8 << 20, 8 << 20],
            'outside_all_measured_task_parents': True, 'actual_description_verified': True, 'detached_from_ssh_stdout': True,
            'actual_kernel': {'memory.max': str(128 << 20), 'memory.swap.max': '0', 'cpu.max': '10000 100000', 'pids.max': '64'}}
        actual_sha = self.write(self.coordinator_dir / 'ACTUAL-COORDINATOR.json', actual)
        accepted = {'launch_intent_sha256': intent_sha, 'registration_sha256': self.reg_sha,
            'controller_sha256': self.helper_shas['controller-cohort-v6.py'], 'launcher_sha256': self.helper_shas['launch-cohort.py'],
            'detached_from_ssh_stdout': True, 'automatic_takeover_or_retry': False,
            'actual_unit': {'LoadState': 'loaded', 'Description': intent['description'], 'MemoryMax': str(128 << 20),
                'MemorySwapMax': '0', 'TasksMax': '64', 'CPUQuotaPerSecUSec': '100ms', 'RuntimeMaxUSec': '4h',
                'RemainAfterExit': 'yes', 'LimitFSIZE': str(8 << 20), 'ControlGroup': '/system.slice/' + name, 'ActiveState': 'active'}}
        self.write(self.coordinator_dir / 'LAUNCH-ACCEPTED.json', accepted)
        final = {'kind': 'coordinator_outside_instrumentation_final_cost', 'actual_coordinator_receipt_sha256': actual_sha,
            'outside_measured_parent_and_reported_separately': True, 'performance_claim_ready': False,
            'actual_kernel': {'memory.peak': '24000000', 'memory.current': '20000000', 'cpu.stat': 'usage_usec 100\n',
                              'memory.events': 'max 0\noom 0\noom_kill 0\n'}}
        self.timing['coordinator_final_cost_sha256'] = self.write(self.coordinator_dir / 'COORDINATOR-FINAL-COST.json', final)
        runtime = {'receipt_sha256': actual_sha, 'launch_intent_sha256': intent_sha,
                   'launcher_sha256': self.helper_shas['launch-cohort.py']}
        self.timing['bindings'] = M.public_bindings(self.manifest, runtime)
        self.timing['paired_comparisons'] = self.controller_summaries()

    def controller_summaries(self):
        # Synthetic producer-shaped summary, independently computed from raw rows.
        result = []
        for stratum in M.STRATA:
            subset = [r for r in self.rows if r['stratum'] == stratum]; pairs = []
            for pair in range(1, 7):
                found = {r['side']: r for r in subset if r['pair'] == pair}; b, c = found['baseline'], found['candidate']
                pairs.append({'pair': pair, 'timing_ratios_candidate_over_baseline': {
                    n: c['timings_seconds'][n] / b['timings_seconds'][n] for n in M.TIMES},
                    'kernel_peak_ratios_candidate_over_baseline': {
                    n: c['actual_kernel_peak_bytes'][n] / b['actual_kernel_peak_bytes'][n] for n in M.ROLES}})
            medians = {n: statistics.median(p['timing_ratios_candidate_over_baseline'][n] for p in pairs) for n in M.TIMES}
            ram = {n: statistics.median(p['kernel_peak_ratios_candidate_over_baseline'][n] for p in pairs) for n in M.ROLES}
            maxima = {n: {side: max(r['actual_kernel_peak_bytes'][n] for r in subset if r['side'] == side) for side in M.SIDES} for n in M.ROLES}
            guards = {n: v['candidate'] <= v['baseline'] for n, v in maxima.items()}
            faster = sum(p['timing_ratios_candidate_over_baseline'][M.TIMES[0]] < 1 for p in pairs)
            tpass = medians[M.TIMES[0]] <= .8 and faster >= 5 and all(medians[n] <= 1 for n in M.TIMES[1:])
            rpass = ram['parent'] <= .98 and guards['parent']; original = all(v <= 1 for v in ram.values()) and all(guards.values())
            result.append({'stratum': stratum, 'complete_pairs': 6, 'pairs': pairs,
                'timing_median_matched_ratios': medians, 'kernel_peak_median_matched_ratios': ram,
                'timing_ratio_min_max': {n: [min(p['timing_ratios_candidate_over_baseline'][n] for p in pairs), max(p['timing_ratios_candidate_over_baseline'][n] for p in pairs)] for n in M.TIMES},
                'kernel_peak_ratio_min_max': {n: [min(p['kernel_peak_ratios_candidate_over_baseline'][n] for p in pairs), max(p['kernel_peak_ratios_candidate_over_baseline'][n] for p in pairs)] for n in M.ROLES},
                'faster_first_draft_pairs': faster, 'maximum_peak_no_increase': guards,
                'timing_gates_passed': tpass, 'parent_memory_acceptance_passed': rpass,
                'original_all_five_scope_zero_increase_gates_passed': original,
                'original_scope_diagnostics': {n: {'matched_median_ratio': ram[n],
                    'maximum_baseline_peak_bytes': maxima[n]['baseline'], 'maximum_candidate_peak_bytes': maxima[n]['candidate'],
                    'median_zero_increase_passed': ram[n] <= 1, 'maximum_zero_increase_passed': guards[n],
                    'original_zero_increase_passed': ram[n] <= 1 and guards[n]} for n in M.ROLES},
                'original_v4_v5_full_timing_and_memory_gates_passed': tpass and original,
                'timing_and_memory_gates_passed': tpass and rpass})
        return result

    def refresh_top(self):
        self.timing['paired_comparisons'] = self.controller_summaries()
        self.quality['timing_result_sha256'] = self.write(self.cohort / 'PUBLIC-TIMING-RESULT.json', self.timing)
        self.quality['private_records_sha256'] = self.write(self.cohort / 'private-records.json', self.records)
        self.write(self.qdir / 'INPUT-BINDING.json', {n: self.quality[n] for n in
             ('registration_sha256', 'timing_result_sha256', 'private_records_sha256', 'inventory_sha256', 'runner_sha256')})
        self.update_quality()

    def native_execution(self, index, sample, oracle_sha):
        name = 'cutmyfirstdraftoracle' + f'{index:032x}'
        labels = {'cutmy.first-draft': '20261016', 'cutmy.first-draft.role': 'quality-oracle',
                  'cutmy.first-draft.oracle': name, 'com.docker.compose.project': 'cutmy-firstdraft-bench',
                  'com.docker.compose.service': 'quality-oracle'}
        argv = ['docker', '--host', 'unix:///var/run/docker.sock', 'run', '--pull=never', '--name', name,
                '--network=none', '--user=0:0', '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges:true',
                '--cpus=1', '--memory=512m', '--memory-swap=512m', '--pids-limit=128', '--mount',
                f'type=bind,source={self.root},target={self.root}', '--entrypoint', '/usr/bin/python3', M.BASELINE_IMAGE,
                '-I', '-B', str(self.root / 'inputs/verify-content.py'), str(sample['input'])]
        intent = {'ordinal': f'fallback-{index:02}', 'container_name': name, 'labels': labels, 'image_id': M.BASELINE_IMAGE,
                  'argv': argv, 'started_at_utc': '2026-10-16T14:00:00+00:00'}
        self.write(self.qdir / f'fallback-{index:02}-intent.json', intent)
        stdout_sha = self.write(self.qdir / f'fallback-{index:02}-stdout.log', b'SYNTHETIC NO NATIVE EXECUTION\n')
        self.write(self.qdir / f'fallback-{index:02}-stderr.log', b'')
        value = {**intent, 'passed': True, 'exit_code': 0, 'docker_exit_code': 0, 'oom_killed': False,
            'container_removed': True, 'output_sha256': oracle_sha, 'stdout_sha256': stdout_sha, 'stderr_sha256': None}
        self.write(self.qdir / f'fallback-{index:02}-execution.json', value)

    def with_one_native_fallback_and_later_current_reuse(self):
        for index in (1, 2):
            sample = self.samples[index]
            sample['rows'][0]['output']['sha256'] = 'f' * 64
            path = sample['directory'] / 'draft-private-receipt.json'; report = self.read(path)
            report['outputs'][0]['sha256'] = 'f' * 64; self.write(path, report)
            sample['public']['draft']['outputs'][0]['sha256'] = 'f' * 64
            sample['public']['draft']['first_output']['sha256'] = 'f' * 64
            sample['timed_sha256'] = self.write(sample['directory'] / 'public-receipt.json', sample['public'])
            self.records[index]['receipt_sha256'] = sample['timed_sha256']
            sample['bound_document_digests'] = [self.d(p) for p in [sample['input'], sample['directory'] / 'public-receipt.json',
                sample['directory'] / 'requests.json', sample['directory'] / 'draft-private-receipt.json',
                sample['directory'] / 'final-private-receipt.json']]
        new_certs, _, oracle_sha = self.native(self.samples[1]); self.native_execution(2, self.samples[1], oracle_sha)
        old_cache = self.cache; self.cache = copy.deepcopy(old_cache)
        counters = {'now': 0, 'prior': 0, 'current': 0}; distinct = set()
        for index, sample in enumerate(self.samples, 1):
            native_now = index == 2
            if native_now:
                M.insert(self.cache, new_certs, 'current'); counters['now'] += 30
            view, mapping = self.materialize(sample)
            for item in mapping:
                distinct.add(item['decode_key'])
                if not native_now: counters[item['certificate_origin']] += 1
            proof = self.read(self.qdir / f'SAMPLE-{index:02}.json')
            proof.update(timed_receipt_sha256=sample['timed_sha256'], qualifications=mapping,
                current_bound_document_digests=sample['bound_document_digests'], full_output_decodes_executed_now=30 if native_now else 0,
                native_decoding_not_repeated_for_reused_bytes=not native_now)
            proof_sha = self.write(self.qdir / f'SAMPLE-{index:02}.json', proof)
            self.write(self.qdir / f'FINGERPRINTS-{index:02}.json', view)
            self.summaries[index - 1].update(qualification_receipt_sha256=proof_sha, timed_receipt_sha256=sample['timed_sha256'],
                full_output_decodes_executed_now=30 if native_now else 0)
        self.quality.update(full_output_decode_executions_now=30, whole_sample_native_verifier_invocations_now=1,
            additional_target_full_file_hash_passes_after_native=30, prior_certificate_reuse_outputs=counters['prior'],
            current_run_certificate_reuse_outputs=counters['current'], distinct_encoded_objects_seen=len(distinct))
        self.refresh_top(); self.cache = old_cache

    def test_complete_synthetic_metadata_is_accepted_without_executing_media(self):
        report = M.audit(self.args)
        self.assertTrue(report['performance_claim_ready']); self.assertEqual(len(report['all24_rows']), 24)
        self.assertEqual(report['quality']['full_output_decode_executions_now'], 0)
        self.assertEqual(report['quality']['prior_certificate_reuse_outputs'], 720)
        self.assertTrue(all(not r['diagnostic_original_all5_zero_increase_passed'] for r in report['strata']))

    def test_missing12_and_failed_cohort_never_qualifies(self):
        self.timing['completed_runs'] = 12; self.timing['rows'] = self.rows[:12]
        self.write(self.cohort / 'PUBLIC-TIMING-RESULT.json', self.timing)
        with self.assertRaises(M.Refused): M.audit(self.args)

    def test_negative_prefix_and_operational_failure_are_refused(self):
        for name in ('FAILED.json', 'NEGATIVE-FRESH-FUTILITY.json', 'NEGATIVE-INCOMPLETE-TIMING-RESULT.json'):
            path = self.cohort / name; self.write(path, {'failed': True})
            with self.subTest(name=name), self.assertRaisesRegex(M.Refused, 'failed_or_incomplete'): M.audit(self.args)
            path.unlink()

    def test_external_registration_hash_is_required(self):
        self.args.expected_registration_sha256 = '0' * 64
        with self.assertRaisesRegex(M.Refused, 'externally_bound_new_registration'): M.audit(self.args)

    def test_frozen_helper_substitution_refused(self):
        self.write(self.root / 'inputs/run-python.py', b'CHANGED')
        with self.assertRaisesRegex(M.Refused, 'frozen_input_substitution'): M.audit(self.args)

    def test_source_and_wheel_substitution_refused(self):
        for name in ('interview.mp4', 'cutmyvideo-1.4.0-py3-none-any.whl'):
            p = self.root / 'inputs' / name; old = p.read_bytes(); self.write(p, b'CHANGED')
            with self.subTest(name=name), self.assertRaisesRegex(M.Refused, 'frozen_input_substitution'): M.audit(self.args)
            self.write(p, old)

    def test_current_private_maps_checked_even_with_unchanged_encoded_hashes(self):
        p = self.samples[0]['directory'] / 'draft-private-receipt.json'; v = self.read(p)
        v['outputs'][0]['segments'][0]['source_start_ms'] += 1; self.write(p, v)
        with self.assertRaisesRegex(M.Refused, 'current_source_order_or_timeline_changed'): M.audit(self.args)

    def test_view_fingerprints_not_trusted_without_exact_certificate(self):
        p = self.qdir / 'FINGERPRINTS-01.json'; v = self.read(p); v['outputs'][0]['decoded_audio_sha256'] = 'f' * 64; self.write(p, v)
        with self.assertRaisesRegex(M.Refused, 'qualification_mapping_or_fingerprints_changed'): M.audit(self.args)

    def test_forged_native_execution_count_refused(self):
        self.quality['full_output_decode_executions_now'] = 720; self.quality['prior_certificate_reuse_outputs'] = 0; self.update_quality()
        with self.assertRaisesRegex(M.Refused, 'dishonest_quality_counts_or_readiness'): M.audit(self.args)

    def test_whole30_native_fallback_and_later_current_reuse_counts_are_reconstructed(self):
        self.with_one_native_fallback_and_later_current_reuse(); report = M.audit(self.args)
        self.assertTrue(report['performance_claim_ready'])
        self.assertEqual(report['quality']['full_output_decode_executions_now'], 30)
        self.assertEqual(report['quality']['current_run_certificate_reuse_outputs'], 1)
        self.assertEqual(report['quality']['prior_certificate_reuse_outputs'], 689)

    def test_native_fallback_requires_actual_retained_success_no_oom_and_removed_container(self):
        self.with_one_native_fallback_and_later_current_reuse()
        path = self.qdir / 'fallback-02-execution.json'; original = self.read(path)
        for field, value in [('oom_killed', True), ('exit_code', 1), ('container_removed', False), ('image_id', 'sha256:' + 'e' * 64)]:
            changed = copy.deepcopy(original); changed[field] = value; self.write(path, changed)
            with self.subTest(field=field), self.assertRaisesRegex(M.Refused, 'native_execution_incomplete'): M.audit(self.args)

    def test_host_oom_increment_and_reboot_are_refused_after_digest_rebinding(self):
        original = copy.deepcopy(self.records[0]['host_oom_after'])
        for field, value in [('oom_kill', 1), ('boot_id', 'other')]:
            self.records[0]['host_oom_after'] = {**original, field: value}; self.refresh_top()
            with self.subTest(field=field), self.assertRaisesRegex(M.Refused, 'host_oom_or_reboot'): M.audit(self.args)

    def test_reordered_records_are_refused_even_with_rebound_top_digest(self):
        self.records[0], self.records[1] = self.records[1], self.records[0]; self.refresh_top()
        with self.assertRaisesRegex(M.Refused, 'sample_order_or_pooling_changed'): M.audit(self.args)

    def test_fresh_source_or_track_scope_not_inferred_from_identical_bytes(self):
        path = self.samples[0]['directory'] / 'draft-private-receipt.json'; report = self.read(path)
        report['source_id'] = 'OTHER_SOURCE'; self.write(path, report)
        with self.assertRaisesRegex(M.Refused, 'current_private_source_or_plan_changed'): M.audit(self.args)

    def test_missing_final720_rehash_count_is_not_accepted(self):
        self.quality['final_target_file_rehash_checks'] = 719; self.update_quality()
        with self.assertRaisesRegex(M.Refused, 'dishonest_quality_counts_or_readiness'): M.audit(self.args)

    def test_installed_sdk_extra_or_mutated_module_is_refused(self):
        package = self.root / 'sdk/lib/python3.11/site-packages/cutmyvideo'
        self.write(package / '__init__.py', b'MODIFIED')
        with self.assertRaisesRegex(M.Refused, 'installed_sdk_does_not_match_signed_wheel'): M.audit(self.args)

    def test_frozen_sdk_is_checked_without_importing_its_code(self):
        p = self.root / 'sdk/lib/python3.11/site-packages/cutmyvideo/__init__.py'; p.chmod(0o644)
        report = M.audit(self.args); self.assertTrue(report['performance_claim_ready'])

    def test_pair_proof_digest_and_fingerprint_comparison_are_checked(self):
        p = self.qdir / 'PAIR-01.json'; v = self.read(p); v['decoded_final_aggregate_sha256'] = 'f' * 64; self.write(p, v)
        with self.assertRaisesRegex(M.Refused, 'paired_final_proof_changed'): M.audit(self.args)

    def test_baseline_repeat_proof_is_required(self):
        p = self.qdir / 'BASELINE-REPEAT.json'; v = self.read(p); v['encoded_file_hashes_equal'] = 0; self.write(p, v)
        with self.assertRaisesRegex(M.Refused, 'baseline_repeat_qualification_changed'): M.audit(self.args)

    def test_backend_and_timing_digest_mismatch_refused(self):
        p = Path(self.records[0]['backend']); v = self.read(p); v['database_counts']['jobs'] = 29; self.write(p, v)
        with self.assertRaisesRegex(M.Refused, 'sample_runtime_receipts_changed'): M.audit(self.args)

    def test_public_output_contains_no_private_names_urls_or_ids(self):
        report = M.audit(self.args); rendered = json.dumps(report) + M.render(report)
        for secret in ('PRIVATE_', 'http://', str(self.root), 'owner_cookie', 'DATABASE_URL', 'container_id'):
            with self.subTest(secret=secret): self.assertNotIn(secret, rendered)

    def test_actual_counts_caps_oom_and_production_checked_independently(self):
        b = self.read(self.records[0]['backend']); raw = self.samples[0]['public']; m = self.manifest
        for mutation in ('jobs', 'max', 'cap', 'production', 'client_peak'):
            v = copy.deepcopy(b)
            if mutation == 'jobs': v['database_counts']['jobs'] = 29
            if mutation == 'max': v['resources_before_stop']['roles']['pg']['kernel']['memory.events'] = 'max 1\noom 0\noom_kill 0\n'
            if mutation == 'cap': v['resources_before_stop']['parent']['cpu.max'] = '200000 100000'
            if mutation == 'production': v['production_after_cleanup']['roles']['api']['id'] = 'OTHER'
            if mutation == 'client_peak': v['resources_before_stop']['roles']['client']['kernel_memory_peak_bytes'] = 0
            with self.subTest(mutation=mutation), self.assertRaises(M.Refused):
                M.backend_scope(m, v, raw, 'baseline', M.STRATA[0], self.reg_sha)

    def test_parent_median_threshold_and_max_are_both_required(self):
        for ratio in (.980001, 1.0):
            rows = copy.deepcopy(self.rows)
            for row in rows:
                if row['side'] == 'candidate': row['actual_kernel_peak_bytes']['parent'] = int(100_000_000 * ratio)
                else: row['actual_kernel_peak_bytes']['parent'] = 100_000_000
            self.assertFalse(any(v['timing_and_memory_gates_passed'] for v in M.metrics(rows)))
        rows = copy.deepcopy(self.rows); rows[1]['actual_kernel_peak_bytes']['parent'] = 101 << 20
        self.assertFalse(M.metrics(rows)[0]['timing_and_memory_gates_passed'])

    def test_unchanged_role_increase_is_diagnostic_not_suppressed(self):
        m = M.metrics(self.rows)
        self.assertTrue(all(r['timing_and_memory_gates_passed'] for r in m))
        self.assertTrue(all(not r['diagnostic_original_all5_zero_increase_gates']['pg_kernel_peak_median_no_increase'] for r in m))

    def test_first_final_draft_and_whole_metrics_remain_strict(self):
        for name in M.TIMES:
            rows = copy.deepcopy(self.rows)
            for r in rows:
                if r['side'] == 'candidate': r['timings_seconds'][name] = 81 if name == M.TIMES[0] else 1000
            with self.subTest(name=name): self.assertTrue(all(not s['timing_and_memory_gates_passed'] for s in M.metrics(rows)))

    def test_old_v4_renderer_is_unchanged(self):
        p = Path(__file__).with_name('render-formal-report.py')
        self.assertEqual(sha(p.read_bytes()), 'fdbf3e7f11d72b4e14ffab172971ee1c0723fef32efab1201fabf85fdf10eb48')
        self.assertEqual(sha(Path(__file__).with_name('render-v5-report.py').read_bytes()),
                         'f8d1a9185af23dbd9d3b72df28c4af9a3a967944584bd1ed9ee1542a0681e35a')

    def test_matched_ratio_median_is_not_ratio_of_unmatched_clock_medians(self):
        rows = copy.deepcopy(self.rows)
        ratios = [.7, .8, .9, .6, .7, .8]
        for row in rows:
            baseline = 100 if row['pair'] % 2 else 1
            row['timings_seconds'][M.TIMES[0]] = baseline * (ratios[row['pair'] - 1] if row['side'] == 'candidate' else 1)
        self.assertEqual(M.metrics(rows)[0]['timing_ratio_dispersion'][M.TIMES[0]]['median'], .75)

    def test_valid_full24_with_failed_primed_goal_emits_all_adverse_rows_and_no_readiness(self):
        for index, row in enumerate(self.rows, 1):
            if row['side'] != 'candidate' or row['stratum'] != M.STRATA[1]: continue
            row['actual_kernel_peak_bytes']['parent'] = 101 << 20
            record = self.records[index - 1]; backend = self.read(record['backend'])
            backend['resources_before_stop']['parent']['memory_peak_bytes'] = 101 << 20
            backend['parent_after_container_stop']['memory_peak_bytes'] = 101 << 20
            backend_sha = self.write(record['backend'], backend); record['backend_sha256'] = backend_sha
            path = self.qdir / f'SAMPLE-{index:02}.json'; proof = self.read(path); proof['backend_receipt_sha256'] = backend_sha
            self.summaries[index - 1]['qualification_receipt_sha256'] = self.write(path, proof)
            self.summaries[index - 1]['backend_receipt_sha256'] = backend_sha
        self.refresh_top(); report = M.audit(self.args)
        self.assertFalse(report['performance_claim_ready']); self.assertEqual(len(report['all24_rows']), 24)
        self.assertTrue(report['strata'][0]['timing_and_memory_gates_passed'])
        self.assertFalse(report['strata'][1]['timing_and_memory_gates_passed'])

    def test_raw_sdk_callback_clock_cannot_be_replaced_by_an_earlier_phase(self):
        backend = self.read(self.records[0]['backend']); receipt = copy.deepcopy(self.samples[0]['public'])
        receipt[M.TIMES[0]] -= 1
        with self.assertRaisesRegex(M.Refused, 'sdk_callback_or_phase_clock_changed'):
            M.backend_scope(self.manifest, backend, receipt, 'baseline', M.STRATA[0], self.reg_sha)

    def test_sample_rss_cannot_substitute_an_actual_kernel_peak(self):
        backend = self.read(self.records[0]['backend'])
        backend['resources_before_stop']['roles']['client'] = {'sampled_rss_peak_bytes': 8 << 20}
        with self.assertRaises(M.Refused):
            M.backend_scope(self.manifest, backend, self.samples[0]['public'], 'baseline', M.STRATA[0], self.reg_sha)

    def test_metadata_changed_at_final_boundary_is_refused(self):
        evidence = M.Evidence(); path = self.cohort / 'PUBLIC-TIMING-RESULT.json'; evidence.doc(path)
        data = path.read_bytes(); path.write_bytes(data.replace(b'false', b'true ', 1))
        with self.assertRaisesRegex(M.Refused, 'evidence_changed_before_report'): evidence.finish()

    def test_actual_coordinator_identity_caps_and_final_cost_are_required(self):
        actual_path = self.coordinator_dir / 'ACTUAL-COORDINATOR.json'; original = self.read(actual_path)
        for mode in ('cgroup', 'caps', 'fsize'):
            value = copy.deepcopy(original)
            if mode == 'cgroup': value['cgroup'] = '/sys/fs/cgroup/cutmyfirstdraft' + 'a' * 32 + '.slice'
            if mode == 'caps': value['actual_kernel']['memory.max'] = str(256 << 20)
            if mode == 'fsize': value['actual_fsize_limits'] = [-1, -1]
            self.write(actual_path, value)
            with self.subTest(mode=mode), self.assertRaises(M.Refused): M.audit(self.args)
        self.write(actual_path, original)
        path = self.coordinator_dir / 'COORDINATOR-FINAL-COST.json'; final = self.read(path)
        final['actual_kernel']['memory.peak'] = '0'; self.write(path, final)
        with self.assertRaises(M.Refused): M.audit(self.args)

    def test_public_binding_and_claimed_controller_summary_are_not_trusted(self):
        original = copy.deepcopy(self.timing)
        self.timing['bindings']['source_sha256'] = 'e' * 64
        self.refresh_top()
        with self.assertRaisesRegex(M.Refused, 'public_coordinator_or_gate_binding_changed'): M.audit(self.args)
        self.timing = copy.deepcopy(original)
        self.timing['paired_comparisons'][0]['kernel_peak_median_matched_ratios']['parent'] = .5
        # Rebind all metadata digests while retaining the forged producer summary.
        digest = self.write(self.cohort / 'PUBLIC-TIMING-RESULT.json', self.timing)
        self.quality['timing_result_sha256'] = digest
        binding = self.read(self.qdir / 'INPUT-BINDING.json'); binding['timing_result_sha256'] = digest
        self.write(self.qdir / 'INPUT-BINDING.json', binding); self.update_quality()
        with self.assertRaisesRegex(M.Refused, 'controller_comparison_disagrees'): M.audit(self.args)

    def test_role_diagnostic_does_not_include_a_failed_timing_gate(self):
        rows = copy.deepcopy(self.rows)
        for row in rows:
            row['actual_kernel_peak_bytes'] = {n: 8 << 20 for n in M.ROLES}
            if row['side'] == 'candidate': row['timings_seconds'][M.TIMES[0]] *= 3
        result = M.metrics(rows)[0]
        self.assertTrue(result['diagnostic_original_all5_zero_increase_passed'])
        self.assertFalse(result['diagnostic_original_full_timing_and_memory_passed'])
        self.assertFalse(result['timing_and_memory_gates_passed'])

    def test_host_change_between_otherwise_valid_samples_is_not_pooled(self):
        for name in ('host_oom_before', 'host_oom_after'):
            self.records[2][name]['oom_kill'] = 1
        self.refresh_top()
        with self.assertRaisesRegex(M.Refused, 'host_boot_or_oom_changed_between_samples'): M.audit(self.args)


if __name__ == '__main__':
    unittest.main(verbosity=2)
