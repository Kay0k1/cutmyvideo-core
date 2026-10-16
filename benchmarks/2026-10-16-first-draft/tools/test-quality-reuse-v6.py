"""Synthetic offline controls. All child-process creation is forbidden."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location('quality_reuse_v6', HERE / 'quality-reuse-v6.py')
M = importlib.util.module_from_spec(SPEC); SPEC.loader.exec_module(M)


class Controls(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='cutmy-quality-reuse-offline-')
        self.root = Path(self.tmp.name)
        self.root_patch = patch.object(M, 'ROOT', self.root); self.root_patch.start()
        self.no_process = patch.object(subprocess, 'Popen', side_effect=AssertionError('native process forbidden'))
        self.no_process.start()
        self.clip = {'id': 'clip-01', 'ranges': [{'start_ms': 1000, 'end_ms': 7000, 'label': 'one'},
                                                {'start_ms': 8000, 'end_ms': 11000, 'label': 'two'}], 'options': {}}
        self.plan = {'version': 1, 'options': {'format': 'mp4', 'quality': 'best', 'cut_mode': 'accurate'},
                     'clips': [self.clip]}
        self.output = {'clip_id': 'clip-01', 'rendition': 'final', 'sha256': 'a' * 64, 'size_bytes': 8,
                       'segments': [{'index': 0, 'label': 'one', 'source_start_ms': 1000, 'source_end_ms': 7000,
                                     'output_start_ms': 0, 'output_end_ms': 6000},
                                    {'index': 1, 'label': 'two', 'source_start_ms': 8000, 'source_end_ms': 11000,
                                     'output_start_ms': 6000, 'output_end_ms': 9000}],
                       'track_manifest': [{'index': 0, 'type': 'video', 'included': True, 'codec': 'h264'},
                                          {'index': 1, 'type': 'audio', 'included': True, 'codec': 'aac'}]}
        self.env = M.environment({name: {'executable_sha256': digest, 'version_stdout_sha256': 'd' * 64}
                                  for name, digest in M.TOOL_SHA.items()})

    def tearDown(self):
        self.no_process.stop(); self.root_patch.stop(); self.tmp.cleanup()

    def save(self, path, value):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value), encoding='utf-8'); path.chmod(0o600)

    def test_positive_manifest(self):
        intent, tracks = M.check_manifest(self.output, self.clip, self.plan, 'final')
        self.assertEqual(intent['ranges'][0]['start_ms'], 1000)
        self.assertEqual(len(tracks), 2)

    def test_real_sdk_output_has_only_report_level_rendition(self):
        self.output.pop('rendition')
        intent, _ = M.check_manifest(self.output, self.clip, self.plan, 'final')
        self.assertEqual(intent['rendition'], 'final')
        self.output['rendition'] = 'draft'
        with self.assertRaisesRegex(M.Refused, 'output_clip_scope'):
            M.check_manifest(self.output, self.clip, self.plan, 'final')

    def test_current_maps_checked_even_with_identical_encoded_digest(self):
        for field, value in [('source_start_ms', 1001), ('output_start_ms', 1), ('index', 1), ('label', 'changed')]:
            with self.subTest(field=field):
                output = copy.deepcopy(self.output); output['segments'][0][field] = value
                with self.assertRaises(M.Refused):
                    M.check_manifest(output, self.clip, self.plan, 'final')

    def test_track_selection_scope_and_unknown_fields(self):
        output = copy.deepcopy(self.output)
        output['track_manifest'][1]['index'] = 2
        with self.assertRaisesRegex(M.Refused, 'qualified_track_manifest_changed'):
            M.check_manifest(output, self.clip, self.plan, 'final', self.output['track_manifest'])
        output['track_manifest'][1]['cookie'] = 'SECRET'
        with self.assertRaises(M.Refused):
            M.check_manifest(output, self.clip, self.plan, 'final')

    def test_explicit_audio_choice_verified_against_manifest(self):
        self.clip['options']['audio_track'] = 3
        with self.assertRaisesRegex(M.Refused, 'explicit_audio_manifest'):
            M.check_manifest(self.output, self.clip, self.plan, 'final')

    def test_semantic_key_changes_for_order_crop_audio_rendition(self):
        initial = M.source_key(self.output, M.semantics(self.plan, self.clip, 'final'), self.env)
        for mode in ('order', 'crop', 'audio', 'draft'):
            clip = copy.deepcopy(self.clip); rendition = 'final'
            if mode == 'order': clip['ranges'].reverse()
            if mode == 'crop': clip['options']['crop'] = {'x': .25, 'y': 0, 'width': .5, 'height': 1}
            if mode == 'audio': clip['options']['audio_track'] = 1
            if mode == 'draft': rendition = 'draft'
            with self.subTest(mode=mode):
                self.assertNotEqual(initial, M.source_key(self.output, M.semantics(self.plan, clip, rendition), self.env))

    def test_cosmetic_labels_excluded_from_content_semantics_but_maps_checked(self):
        a = M.semantics(self.plan, self.clip, 'final')
        self.clip['label'] = 'new label'; self.clip['ranges'][0]['label'] = 'renamed'
        self.assertEqual(a, M.semantics(self.plan, self.clip, 'final'))
        with self.assertRaises(M.Refused):
            M.check_manifest(self.output, self.clip, self.plan, 'final')

    def test_empty_per_clip_string_inherits_real_base(self):
        self.plan['options']['quality'] = '1080p'; self.clip['options']['quality'] = ''
        self.assertEqual(M.semantics(self.plan, self.clip, 'final')['effective_options']['quality'], '1080p')

    def test_decode_key_binds_size_runtime_tools_and_helpers(self):
        initial = M.decode_key(self.output, self.env)
        variants = []
        env = copy.deepcopy(self.env); env['baseline_image_id'] = 'changed'; variants.append((self.output, env))
        env = copy.deepcopy(self.env); env['tool_binding']['ffmpeg']['version_stdout_sha256'] = 'e' * 64
        variants.append((self.output, env))
        env = copy.deepcopy(self.env); env['original_helpers']['full-decode.py'] = 'e' * 64
        variants.append((self.output, env))
        output = copy.deepcopy(self.output); output['size_bytes'] += 1; variants.append((output, self.env))
        for output, env in variants:
            self.assertNotEqual(initial, M.decode_key(output, env))

    def test_fresh_hash_detects_same_length_byte_change(self):
        path = self.root / 'encoded.mp4'; path.write_bytes(b'abcdefgh')
        first = M.hash_file(path)
        path.write_bytes(b'abcdefgX')
        second = M.hash_file(path)
        self.assertEqual(first['size_bytes'], second['size_bytes'])
        self.assertNotEqual(first['sha256'], second['sha256'])
        # Filesystems may expose identical timestamps for same-tick rewrites;
        # the mandatory new whole-file read, not metadata, detects the change.

    def test_symlink_hardlink_and_oversize_media_refused(self):
        path = self.root / 'encoded.mp4'; path.write_bytes(b'abcdefgh')
        other = self.root / 'other.mp4'; other.symlink_to(path)
        with self.assertRaises(M.Refused): M.hash_file(other)
        other.unlink(); os.link(path, other)
        with self.assertRaises(M.Refused): M.hash_file(path)
        other.unlink()
        with self.assertRaises(M.Refused): M.hash_file(path, 7)

    def test_unknown_row_selects_whole_sample_without_partial_outputs(self):
        intent, tracks = M.check_manifest(self.output, self.clip, self.plan, 'final')
        row = {'output': self.output, 'intent': intent, 'tracks': tracks}
        certificate = {'decode_key': M.decode_key(self.output, self.env), 'source_key': M.source_key(self.output, intent, self.env),
                       'intent': intent, 'tracks': tracks, 'environment': self.env}
        unknown = copy.deepcopy(row); unknown['output']['sha256'] = 'b' * 64
        sample = {'rows': [row, unknown]}
        self.assertIsNone(M.reusable(sample, {certificate['source_key']: certificate}, self.env))
        self.assertEqual(list(self.root.iterdir()), [])

    def test_certificate_conflict_cannot_be_overwritten(self):
        certificate = {'source_key': 's', 'decode_key': 'd', 'intent': {}, 'tracks': [], 'fingerprint': {'f': 1}, 'environment': {}}
        cache = {}; M.insert_certificates(cache, [certificate], 'prior')
        changed = copy.deepcopy(certificate); changed['fingerprint']['f'] = 2
        with self.assertRaises(M.Refused): M.insert_certificates(cache, [changed], 'current')
        self.assertEqual(cache['s']['fingerprint']['f'], 1)

    def test_original_v4_never_accepted_by_new_runner(self):
        with patch.object(M, 'digest', return_value=M.OLD_REGISTRATION), \
             patch.object(M, 'read', side_effect=AssertionError('must reject first')):
            with self.assertRaisesRegex(M.Refused, 'original_v4_cohort_cannot_use_reuse'):
                M.prospective_registration(self.root / 'old.json')

    def test_relabelled_v4_registration_still_cannot_use_reuse(self):
        with patch.object(M, 'digest', return_value='f' * 64), \
             patch.object(M, 'read', return_value={'protocol_version': 4, 'prospective_quality': {'mode': 'content_addressed_v1'}}):
            with self.assertRaisesRegex(M.Refused, 'new_prospective_v6_registration_required'):
                M.prospective_registration(self.root / 'relabeled.json')

    def test_fingerprint_structure_hash_and_complete_counts(self):
        row = {name: 'a' * 64 if name.endswith('_sha256') else 1 for name in M.FP_FIELDS}
        row['structure'] = {'streams': [{'codec_type': 'video'}]}
        row['media_structure_sha256'] = M.key(row['structure'])
        self.assertEqual(M.fingerprint(row)['structure'], row['structure'])
        row['structure']['streams'][0]['codec_type'] = 'audio'
        with self.assertRaisesRegex(M.Refused, 'native_structure_hash'): M.fingerprint(row)

    def test_unqualified_historical_oracle_never_seeded(self):
        config = self.root / 'config.json'
        self.save(config, {'cohort_id': 'future', 'prior_inputs': ['private-input']})
        with patch.object(M, 'freeze_helpers'), \
             patch.object(M, 'load_sample', return_value={'directory': self.root}), \
             patch.object(M, 'digest', return_value='f' * 64), \
             patch.object(M, 'original_proof', side_effect=AssertionError('untrusted proof')):
            with self.assertRaisesRegex(M.Refused, 'only_genuine_previously_qualified_pilot_allowed'):
                M.freeze_inventory(config, self.root / 'inventory.json')

    def sample_fixture(self):
        sample = self.root / 'samples/example'; run = sample / 'sdkrun'; files = run / 'collection/files'
        files.mkdir(parents=True)
        clips = []
        for i in range(15):
            clip = copy.deepcopy(self.clip); clip['id'] = f'clip-{i:02}'
            clips.append(clip)
        plan = {**self.plan, 'clips': clips}
        self.save(self.root / 'inputs/requests.json', {'initial': {'plan': plan}})
        input_path = sample / 'client-input.json'
        self.save(input_path, {'run_dir': str(run), 'role': 'baseline', 'server_commit': 'a' * 40,
                               'cache_stratum': 'ready-fresh-process'})
        public = {'complete': True, 'controller_input_sha256': M.digest(input_path),
                  'harness_sha256': M.HELPERS['run-python.py'], 'source_sha256': M.SOURCE_SHA,
                  'fixture_sha256': M.FIXTURE_SHA, 'draft_then_final_unique_jobs': 30,
                  'owner_history_observed_jobs': 30, 'role': 'baseline',
                  'expected_server_commit_from_controller': 'a' * 40, 'cache_stratum': 'ready-fresh-process'}
        requests = {}
        for stage in ('draft', 'final'):
            outputs = []
            for i, clip in enumerate(clips):
                output = copy.deepcopy(self.output); output.update(clip_id=clip['id'], rendition=stage,
                    job_id=f'{stage}-job-{i}', artifact_id=f'{stage}-artifact-{i}')
                path = files / f'{stage}-{i}.mp4'; path.write_bytes(f'{stage}-{i}'.encode())
                hashed = M.hash_file(path); output.update(path=str(path), sha256=hashed['sha256'], size_bytes=hashed['size_bytes'])
                outputs.append(output)
            requests[stage] = {'plan': plan, 'rendition': stage, 'source_id': 'same-source'}
            self.save(run / (stage + '-private-receipt.json'), {'complete': True, 'rendition': stage,
                      'source_id': 'same-source', 'outputs': outputs})
            public[stage] = {'outputs': [{'sha256': row['sha256'], 'size_bytes': row['size_bytes']} for row in outputs]}
        self.save(run / 'requests.json', requests); self.save(run / 'public-receipt.json', public)
        return input_path, run

    def test_all_thirty_files_fresh_hash_and_manifest_checked(self):
        input_path, _ = self.sample_fixture()
        with patch.object(M, 'hash_file', wraps=M.hash_file) as hashed:
            sample = M.load_sample(input_path)
        media_hashes = [call for call in hashed.call_args_list if str(call.args[0]).endswith('.mp4')]
        self.assertEqual(len(sample['rows']), 30)
        self.assertEqual(len(media_hashes), 30)
        M.unchanged_sample(sample)

    def test_real_driver_default_fresh_stratum_in_prior_input(self):
        input_path, run = self.sample_fixture()
        config = M.read(input_path); config.pop('cache_stratum'); self.save(input_path, config)
        public = M.read(run / 'public-receipt.json'); public['controller_input_sha256'] = M.digest(input_path)
        self.save(run / 'public-receipt.json', public)
        self.assertEqual(len(M.load_sample(input_path)['rows']), 30)

    def test_identical_bytes_do_not_hide_cross_source_or_changed_manifest(self):
        input_path, run = self.sample_fixture()
        report_path = run / 'final-private-receipt.json'
        report = M.read(report_path); report['source_id'] = 'OTHER-SOURCE'; self.save(report_path, report)
        with self.assertRaisesRegex(M.Refused, 'current_request_or_source_scope'): M.load_sample(input_path)
        report['source_id'] = 'same-source'; report['outputs'][0]['segments'][0]['source_start_ms'] += 40
        self.save(report_path, report)
        with self.assertRaisesRegex(M.Refused, 'ordered_source_timeline'): M.load_sample(input_path)

    def test_post_verification_mutation_refused(self):
        input_path, run = self.sample_fixture(); sample = M.load_sample(input_path)
        path = Path(sample['rows'][0]['output']['path']); path.write_bytes(b'MUTATED')
        with self.assertRaisesRegex(M.Refused, 'sample_media_changed_after_verification'):
            M.unchanged_sample(sample)

    def test_final_rehash_rejects_even_metadata_equal_byte_tamper(self):
        input_path, _ = self.sample_fixture(); sample = M.load_sample(input_path)
        original_hash = M.hash_file
        target = sample['rows'][0]['output']['path']
        def changed(path, *args):
            result = original_hash(path, *args)
            if str(path) == target:
                result['sha256'] = '0' * 64
            return result
        with patch.object(M, 'hash_file', side_effect=changed), \
             self.assertRaisesRegex(M.Refused, 'sample_media_bytes_changed_after_verification'):
            M.unchanged_sample(sample, rehash=True)

    def test_real_shaped_closed24_schedule_counts_positive_and_failed_job_negative(self):
        manifest = {'samples': 24, 'pairs_per_stratum': 6, 'strata': list(M.STRATA), 'config': {'cohort_id': 'next'}}
        records, rows = [], []
        for stratum in M.STRATA:
            for pair in range(1, 7):
                for side in (('baseline', 'candidate') if pair % 2 else ('candidate', 'baseline')):
                    token = 'fresh' if stratum == M.STRATA[0] else 'primed'
                    name = f'next-{token}-p{pair:02}-{side}'
                    directory = self.root / 'samples' / name
                    row = {'side': side, 'stratum': stratum, 'pair': pair}; rows.append(row)
                    records.append({'sample_id': name, 'record': row,
                                    'receipt': str(directory / 'sdkrun/public-receipt.json'), 'receipt_sha256': 'a' * 64,
                                    'backend': str(directory / 'backend-result.json'), 'backend_sha256': 'a' * 64})
        timing = {'completed_runs': 24, 'rows': rows, 'status': 'all24_timings_collected_content_pending',
                  'registration_sha256': 'b' * 64}
        backend = {'ok': True, 'status': 'collected_and_stopped', 'full30_workload_verified': True,
                   'resources_removed': {name: True for name in ('containers', 'network', 'anchor', 'slice')},
                   'database_counts': {'jobs': 30, 'draft_jobs': 15, 'final_jobs': 15, 'artifacts': 30,
                                       'render_attempts': 30, 'succeeded_jobs': 30}}
        with patch.object(M, 'safe_path', side_effect=lambda raw, *args: Path(raw)), \
             patch.object(M, 'digest', return_value='a' * 64), patch.object(M, 'read', return_value=backend):
            M.schedule(manifest, records, timing, 'b' * 64)
            backend['database_counts']['succeeded_jobs'] = 29
            with self.assertRaisesRegex(M.Refused, 'stopped_exact_thirty_backend_required'):
                M.schedule(manifest, records, timing, 'b' * 64)

    def test_complete_original_native_certificate_chain_positive_and_mutation(self):
        input_path, run = self.sample_fixture(); sample = M.load_sample(input_path)
        outputs = []
        for row in sample['rows']:
            output = row['output']
            fp = {name: 'a' * 64 if name.endswith('_sha256') else 1 for name in M.FP_FIELDS}
            fp['structure'] = {'streams': [{'codec_type': 'video', 'width': 1280, 'height': 720}, {'codec_type': 'audio'}]}
            fp['media_structure_sha256'] = M.key(fp['structure'])
            outputs.append({**fp, 'path': output['path'], 'clip_id': output['clip_id'], 'rendition': row['rendition'],
                            'encoded_file_sha256': output['sha256'], 'encoded_size_bytes': output['size_bytes']})
        private = {'complete': True, 'tool_binding': self.env['tool_binding'], 'source_sha256': M.SOURCE_SHA,
                   'fixture_sha256': M.FIXTURE_SHA, 'timed_receipt_sha256': sample['timed_sha256'], 'outputs': outputs}
        self.save(run / 'full-decoded-fingerprints.json', private)
        oracle = {'passed': True, 'timed_receipt_sha256': sample['timed_sha256'], 'private_fingerprints_sha256':
                  M.digest(run / 'full-decoded-fingerprints.json'), 'source_sha256': M.SOURCE_SHA, 'fixture_sha256': M.FIXTURE_SHA,
                  'full_video_audio_decoded_outputs': 30, 'tool_binding': self.env['tool_binding'],
                  'verifier_sha256': M.HELPERS['verify-content.py'], 'full_decode_helper_sha256': M.HELPERS['full-decode.py'],
                  'frozen_comparator_sha256': M.HELPERS['content-oracle.py'], 'frame_mae_limit': 12.0,
                  'audio_correlation_minimum': .75, 'source_reference_times_come_from_frozen_caller_ranges': True,
                  'sdk_timing_completed_before_decode': True,
                  'decoded_content_aggregate_sha256': M.key([{'rendition': row['rendition'], 'ordinal': index % 15,
                      **{name: row[name] for name in M.FP_FIELDS}} for index, row in enumerate(outputs)]),
                  'stages': [{'rendition': stage, 'clips': 15, 'segments': 30, 'frame_samples': 60,
                              'max_frame_mae': 1.0, 'audio_samples': 30, 'silent_segments': 0, 'min_audio_correlation': .99}
                             for stage in ('draft', 'final')]}
        self.save(run / 'content-oracle-receipt.json', oracle)
        certificates, _, _ = M.original_proof(sample, M.digest(run / 'content-oracle-receipt.json'))
        self.assertEqual(len(certificates), 30)
        self.assertEqual(certificates[0]['intent']['ranges'][0]['start_ms'], 1000)
        oracle['decoded_content_aggregate_sha256'] = '0' * 64
        self.save(run / 'content-oracle-receipt.json', oracle)
        with self.assertRaisesRegex(M.Refused, 'native_decoded_aggregate_changed'): M.original_proof(sample)


if __name__ == '__main__':
    unittest.main(verbosity=2)
