"""Protocol safety checks; these tests do not execute media workloads."""
import importlib.util
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location('core_performance', Path(__file__).with_name('core-performance.py'))
perf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(perf)


class ProtocolTests(unittest.TestCase):
    def test_finite_workload_covers_independent_dimensions(self):
        scenarios = perf.scenarios()
        self.assertEqual(len(scenarios), 65)
        self.assertEqual(len({scenario['id'] for scenario in scenarios}), 65)
        local = [scenario for scenario in scenarios if scenario['fixture'] == 'long' and scenario['kind'] == 'cli']
        expected = {(count, profile, mode) for count in (1, 2, 10, 20, 32)
                    for profile in perf.PROFILES for mode in ('separate', 'joined')}
        actual = {(scenario['count'], next(profile for profile, options in perf.PROFILES.items()
                                           if options == scenario['options']), scenario['output_mode'])
                  for scenario in local}
        self.assertEqual(actual, expected)
        full = next(scenario for scenario in local if scenario['count'] == 32)
        bounds = [(item['start_ms'], item['end_ms']) for item in full['ranges']]
        self.assertLess(len(set(bounds)), len(bounds), 'Repeated ranges must be retained, not deduplicated')
        self.assertTrue(any(a[0] < b[0] < a[1] for a, b in zip(bounds, bounds[1:])), 'Overlap coverage')
        self.assertTrue(any(a[0] > b[0] for a, b in zip(bounds, bounds[1:])), 'Backward order coverage')
        self.assertGreater(max(start for start, _ in bounds), 3_000_000, 'Distant seek coverage')

    def test_every_range_fits_declared_fixture_duration(self):
        duration = {'long': 3600_000, 'complex': 36000, 'hls': 12000,
                    'multi': 12000, 'vfr': 12000, 'rotated': 12000}
        for scenario in perf.scenarios():
            self.assertEqual(len(scenario['ranges']), scenario['count'])
            for item in scenario['ranges']:
                self.assertGreater(item['end_ms'], item['start_ms'])
                self.assertLessEqual(item['end_ms'], duration[scenario['fixture']])

    def test_hls_network_warmth_and_straggler_are_fixed(self):
        hls = [scenario for scenario in perf.scenarios() if scenario['kind'] == 'hls']
        self.assertEqual(len(hls), 8)
        self.assertEqual(sum(scenario['warmup'] for scenario in hls), 2)
        self.assertEqual(sum(scenario['network']['straggler_delay_ms'] > 0 for scenario in hls), 2)
        self.assertTrue(all(scenario['network']['base_delay_ms'] == 15 for scenario in hls))

    def test_semantic_comparison_rejects_bounds_and_stream_mismatch(self):
        common = {'bytes': 100, 'file_sha256': 'container-a', 'actual_start_ms': 12,
                  'streams': [{'codec_name': 'h264'}], 'video_decoded_sha256': 'decoded'}
        equivalent = {**common, 'bytes': 101, 'file_sha256': 'container-b'}
        perf.compare_fingerprints([common], [equivalent])
        for changed in ({'actual_start_ms': 13}, {'video_decoded_sha256': 'wrong'}, {'streams': []}):
            with self.assertRaises(RuntimeError):
                perf.compare_fingerprints([common], [{**equivalent, **changed}])
        a = {**common, 'packet_payload_sha256_by_stream': {'0': 'packet-a'}}
        b = {**equivalent, 'packet_payload_sha256_by_stream': {'0': 'packet-b'}}
        perf.compare_fingerprints([a], [b])
        with self.assertRaises(RuntimeError):
            perf.compare_fingerprints([a], [b], copy_mode=True)

    def test_independent_packet_clocks_preserve_unknown_values_units_and_av_offsets(self):
        source = {'streams': [{'index': 0, 'time_base': '1/90000'}, {'index': 1, 'time_base': '1/48000'}],
                  'packets': [{'stream_index': 0, 'pts': 3000, 'dts': 0, 'duration': 3000, 'data_hash': 'video'},
                              {'stream_index': 1, 'pts': -1024, 'dts': -1024, 'duration': 1024, 'data_hash': 'audio'},
                              {'stream_index': 0, 'pts': 0, 'dts': 3000, 'duration': 3000, 'data_hash': 'video2'},
                              {'stream_index': 1, 'duration': 1024, 'data_hash': 'audio2'}]}
        baseline = perf.packet_fingerprints(source)
        for mutate in ('pts', 'dts', 'duration', 'unknown', 'time_base'):
            changed = json.loads(json.dumps(source))
            if mutate == 'time_base':
                changed['streams'][1]['time_base'] = '1/44100'
            elif mutate == 'unknown':
                changed['packets'][-1]['pts'] = 0
            else:
                changed['packets'][1][mutate] += 1
            candidate = perf.packet_fingerprints(changed)
            self.assertEqual(baseline['packet_payload_sha256_by_stream'], candidate['packet_payload_sha256_by_stream'])
            for copy_mode in (False, True):
                with self.assertRaisesRegex(RuntimeError, 'packet_clock_sha256_by_stream'):
                    perf.compare_fingerprints([baseline], [candidate], copy_mode=copy_mode)
        reversed_dts = json.loads(json.dumps(source))
        reversed_dts['packets'][2]['dts'] = -1
        with self.assertRaisesRegex(RuntimeError, 'non-monotonic'):
            perf.packet_fingerprints(reversed_dts)

    def test_disk_reserve_stops_before_new_media(self):
        with patch.object(perf.shutil, 'disk_usage') as usage:
            usage.return_value.free = perf.RESERVE_BYTES + 100
            perf.ensure_space(Path('/tmp'), 100)
            with self.assertRaises(RuntimeError):
                perf.ensure_space(Path('/tmp'), 101)

    def test_exclusive_manifest_write_preserves_previous_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'frozen.json'
            perf.write_json(path, {'baseline': 'original'}, exclusive=True)
            digest = perf.sha256(path)
            with self.assertRaises(FileExistsError):
                perf.write_json(path, {'baseline': 'replacement'}, exclusive=True)
            self.assertEqual(perf.sha256(path), digest)

    def test_build_catalog_fences_embedded_schema_and_module_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            migration = root / 'internal/app/migrations'
            migration.mkdir(parents=True)
            files = ['main.go', 'go.mod', 'go.sum', 'internal/app/migrations/007.sql',
                     'internal/app/migrations/007_schema.json']
            for filename in files:
                (root / filename).write_text(filename)
            (root / 'private.env').write_text('not a build input')
            before = perf.build_source_catalog(root)
            self.assertEqual(set(before), set(files))
            for filename in files:
                target = root / filename
                old = target.read_text()
                target.write_text(old + 'changed')
                self.assertNotEqual(before, perf.build_source_catalog(root), filename)
                target.write_text(old)

    def test_protocol_pairs_preserve_all_registered_cases_and_key_minimums(self):
        cases = perf.scenarios()
        counts = perf.protocol_pair_counts(cases)
        self.assertEqual(len(counts), 65)
        self.assertEqual(sum(value == 5 for value in counts.values()), 26)
        self.assertEqual(sum(counts.values()) * 2, 494)
        for case in cases:
            self.assertEqual(counts[case['id']], 5 if case['final_key'] else 3)

    def test_pair_supplement_rejects_undeclared_or_reduced_key_counts(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'pairs.json'
            cases = perf.scenarios()
            frozen = {'_sha256': 'frozen-digest', 'scenarios': cases}
            declared = {'manifest_sha256': 'frozen-digest',
                        'scenario_pair_counts': perf.protocol_pair_counts(cases)}
            perf.write_json(path, declared)
            path.with_suffix('.sha256').write_text(perf.sha256(path))
            args = SimpleNamespace(protocol_pairs=True, pairs=None, pair_supplement=path, final_keys=False)
            self.assertEqual(perf.selected_pair_counts(args, cases, frozen), declared['scenario_pair_counts'])
            declared['scenario_pair_counts'][next(case['id'] for case in cases if case['final_key'])] = 3
            perf.write_json(path, declared)
            path.with_suffix('.sha256').write_text(perf.sha256(path))
            with self.assertRaises(RuntimeError):
                perf.selected_pair_counts(args, cases, frozen)
            args.pairs = 3
            with self.assertRaises(RuntimeError):
                perf.selected_pair_counts(args, cases, frozen)

    @unittest.skipUnless(sys.platform == 'linux' and hasattr(os, 'wait4'), 'Linux-only measurement observer')
    def test_timeout_kills_term_ignoring_parent_and_separate_child_group(self):
        with tempfile.TemporaryDirectory() as directory:
            case = Path(directory)
            (case / 'media').mkdir()
            child_pid = case / 'child.pid'
            child = ('import os,signal,time; from pathlib import Path; '
                     'signal.signal(signal.SIGTERM, signal.SIG_IGN); '
                     f'Path({str(child_pid)!r}).write_text(str(os.getpid())); time.sleep(60)')
            parent = ('import signal,subprocess,sys,time; '
                      'signal.signal(signal.SIGTERM, signal.SIG_IGN); '
                      f'subprocess.Popen([sys.executable,"-c",{child!r}], start_new_session=True); '
                      'time.sleep(60)')
            started = time.monotonic()
            try:
                with self.assertRaisesRegex(RuntimeError, 'owned groups terminated'):
                    perf.measured_process([sys.executable, '-c', parent], os.environ.copy(), case,
                                          timeout_seconds=.3, termination_grace_seconds=.1)
                self.assertLess(time.monotonic() - started, 3)
                receipt = json.loads((case / 'timeout.json').read_text())
                self.assertTrue(receipt['timed_out'])
                self.assertTrue(any(event['signal'] == signal.SIGKILL for event in receipt['signals']))
                self.assertTrue(child_pid.exists(), 'child must have started to exercise its separate group')
                pid = int(child_pid.read_text())
                path = Path('/proc') / str(pid) / 'stat'
                until = time.monotonic() + .5
                while path.exists():
                    try:
                        state = path.read_text().rsplit(')', 1)[1].split()[0]
                    except FileNotFoundError:
                        break
                    if state == 'Z':
                        break
                    if time.monotonic() >= until:
                        self.fail('separate child group survived the timed-out parent')
                    time.sleep(.01)
            finally:
                # A failing regression must not leave the test's producer.
                if child_pid.exists():
                    try:
                        os.killpg(int(child_pid.read_text()), signal.SIGKILL)
                    except ProcessLookupError:
                        pass


if __name__ == '__main__':
    unittest.main()
