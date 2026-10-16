#!/usr/bin/env python3
"""Pure controls only; forbid subprocess, Docker, media and actual timings."""
import importlib.util
import copy
import hashlib
from pathlib import Path
import subprocess
import os
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('cohort_v6', Path(__file__).with_name('controller-cohort-v6.py'))
c = importlib.util.module_from_spec(spec); spec.loader.exec_module(c)


def rows():
    return [{'side': side, 'stratum': stratum, 'pair': pair,
             'timings_seconds': {k: 100 * (.7 if side == 'candidate' and k == c.TIMES[0] else 1) for k in c.TIMES},
             'actual_kernel_peak_bytes': {k: 98 if side == 'candidate' and k == 'parent' else 100 for k in c.ROLES}}
            for stratum in c.STRATA for pair in range(1, 7) for side in ('baseline','candidate')]


class Controls(unittest.TestCase):
    def setUp(self):
        self.no_process = patch.object(subprocess, 'Popen', side_effect=AssertionError('process forbidden'))
        self.no_process.start()

    def tearDown(self):
        self.no_process.stop()

    def test_six_pairs_per_stratum_pass_strict_targets(self):
        reports = c.comparisons(rows())
        self.assertEqual(len(reports),2)
        self.assertTrue(all(r['timing_and_memory_gates_passed'] for r in reports))
        self.assertTrue(all(r['complete_pairs']==6 for r in reports))

    def test_missing_pair_is_never_accepted(self):
        value=rows()[:-1]
        self.assertFalse(c.comparisons(value)[1]['timing_and_memory_gates_passed'])

    def test_one_percent_final_regression_is_a_failure(self):
        value=rows()
        for row in value:
            if row['side']=='candidate':row['timings_seconds'][c.TIMES[2]]=101
        self.assertTrue(all(not r['timing_and_memory_gates_passed'] for r in c.comparisons(value)))

    def test_one_percent_role_peak_regression_is_explicit_diagnostic(self):
        value=rows()
        for row in value:
            if row['side']=='candidate':row['actual_kernel_peak_bytes']['pg']=101
        reports = c.comparisons(value)
        self.assertTrue(all(r['timing_and_memory_gates_passed'] for r in reports))
        self.assertTrue(all(not r['original_all_five_scope_zero_increase_gates_passed'] for r in reports))

    def test_role_peak_outlier_is_not_hidden_in_original_diagnostics(self):
        value=rows()
        value[1]['actual_kernel_peak_bytes']['client']=101
        report=c.comparisons(value)[0]
        self.assertEqual(report['kernel_peak_median_matched_ratios']['client'],1)
        self.assertFalse(report['maximum_peak_no_increase']['client'])
        self.assertFalse(report['original_all_five_scope_zero_increase_gates_passed'])
        self.assertTrue(report['timing_and_memory_gates_passed'])

    def test_faster_first_file_cannot_hide_full_draft_regression(self):
        value=rows()
        for row in value:
            if row['side']=='candidate':row['timings_seconds'][c.TIMES[1]]=100.0001
        self.assertTrue(all(not r['timing_and_memory_gates_passed'] for r in c.comparisons(value)))

    def test_less_than_twenty_percent_improvement_fails(self):
        value=rows()
        for row in value:
            if row['side']=='candidate':row['timings_seconds'][c.TIMES[0]]=80.01
        self.assertTrue(all(not r['timing_and_memory_gates_passed'] for r in c.comparisons(value)))

    def test_kernel_peak_is_required_not_rss(self):
        with self.assertRaisesRegex(c.Refused,'actual_role_peak_missing'):
            c.memory_peak({'sampled_rss_peak_bytes':12345})

    def test_oom_and_max_pressure_are_refused(self):
        for key in ('max','oom','oom_kill'):
            events={k:0 for k in ('max','oom','oom_kill')};events[key]=1
            with self.assertRaisesRegex(c.Refused,'role_memory_pressure_failure'):
                c.memory_peak({'kernel':{'memory_peak_bytes':100,'memory.events':'\n'.join(k+' '+str(v) for k,v in events.items())}})

    def test_public_bindings_exclude_private_paths_and_auth(self):
        binding={'version':{'commit':'a'*40,'arch':'amd64','os':'linux','go_version':'go1.27.2'},
                 'tool_sha256':{p:'b'*64 for p in ('/usr/bin/ffmpeg','/usr/bin/ffprobe','/usr/local/bin/cutmy')}}
        m={'memory_protocol_decision':{'sha256':'1'*64,'path':'/root/private/decision'},
           'metric_gates':{},'diagnostic_gates':{},'coordinator_instrumentation':{},
           'config':{'candidate_commit':'a'*40},'images':{s:{'id':'sha256:'+'c'*64,'binding':binding} for s in ('baseline','candidate')},
           'frozen_inputs':{'/root/private/backend.py':'d'*64},'limits':{},'application_config':{'DATABASE_URL':'SECRET','DATA_DIR':'/root/private','FFMPEG_PATH':'/root/private/tool','MAX_RANGES':'32'}}
        value=c.public_bindings(m)
        self.assertNotIn('SECRET',str(value));self.assertNotIn('/root/private',str(value))
        self.assertEqual(value['application_numeric_config'],{'MAX_RANGES':'32'})

    def test_host_oom_and_boot_change_are_refused(self):
        before={'boot_id':'same','oom_kill':1}
        c.check_host_oom_unchanged(before,{'boot_id':'same','oom_kill':1})
        with self.assertRaisesRegex(c.Refused,'host_oom_during_sample'):
            c.check_host_oom_unchanged(before,{'boot_id':'same','oom_kill':2})
        with self.assertRaisesRegex(c.Refused,'host_rebooted_during_sample'):
            c.check_host_oom_unchanged(before,{'boot_id':'new','oom_kill':1})

    def test_quality_helper_substitution_fails_frozen_guard(self):
        helper='/root/.cache/cutmy-first-draft-20261016/inputs/full-decode.py'
        with patch.object(c,'sha',return_value='new'):
            with self.assertRaisesRegex(c.Refused,'cohort_input_changed'):
                c.preconditions({'frozen_inputs':{helper:'old'}},None)

    def fresh(self):
        value = []
        for pair in range(1, 7):
            for side in (('baseline', 'candidate') if pair % 2 else ('candidate', 'baseline')):
                value.append({'pair': pair, 'side': side, 'stratum': c.STRATA[0],
                              'timings_seconds': {name: 70 if side == 'candidate' and name == c.TIMES[0] else 100
                                                  for name in c.TIMES},
                              'actual_kernel_peak_bytes': {name: 98 if side == 'candidate' and name == 'parent' else 100 for name in c.ROLES},
                              'all30_database_counts_passed': True, 'owned_cleanup_passed': True,
                              'production_unchanged': True})
        return value

    def test_futility_never_at_less_than_six_pairs(self):
        value = self.fresh()
        value[1]['actual_kernel_peak_bytes']['worker'] = 1000
        for length in (2, 6, 10, 11):
            with self.subTest(length=length), self.assertRaises(c.Refused):
                c.fresh_futility(value[:length])

    def test_futility_rejects_invalid_cleanup_order_or_replacement(self):
        for mode in ('cleanup', 'order', 'duplicate', 'primed'):
            value = self.fresh()
            if mode == 'cleanup': value[-1]['owned_cleanup_passed'] = False
            if mode == 'order': value[0], value[1] = value[1], value[0]
            if mode == 'duplicate': value[-1] = copy.deepcopy(value[0])
            if mode == 'primed': value[-1]['stratum'] = c.STRATA[1]
            with self.subTest(mode=mode), self.assertRaises(c.Refused):
                c.fresh_futility(value)

    def test_parent_memory_max_guard_stops_but_role_guards_remain_diagnostic(self):
        for role in c.ROLES:
            value = self.fresh()
            value[1]['actual_kernel_peak_bytes'][role] = 101  # Median stays1; max must fail.
            with self.subTest(role=role):
                decision = c.fresh_futility(value)
                self.assertEqual(decision['stop_before_primed'], role == 'parent')
                self.assertFalse(decision['fresh_registered_v6_gate_results']['maximum_peak_no_increase'][role])
                self.assertFalse(decision['performance_claim_ready'])

    def test_passed_fresh_never_means_early_acceptance(self):
        result = c.fresh_futility(self.fresh())
        self.assertFalse(result['stop_before_primed'])
        self.assertFalse(result['full_cohort_complete'])
        self.assertFalse(result['early_positive_acceptance'])
        self.assertFalse(result['performance_claim_ready'])

    def test_original_v4_source_remains_immutable(self):
        original = Path(__file__).with_name('controller-cohort.py')
        self.assertEqual(hashlib.sha256(original.read_bytes()).hexdigest(), c.ORIGINAL_V4_SHA)
        self.assertEqual(hashlib.sha256(Path(__file__).with_name('controller-cohort-v5.py').read_bytes()).hexdigest(), c.ORIGINAL_V5_SHA)
        self.assertEqual(hashlib.sha256(Path(__file__).with_name('quality-reuse.py').read_bytes()).hexdigest(), c.ORIGINAL_V5_QUALITY_SHA)

    def test_parent_requires_positive_two_percent_reduction_in_both_strata(self):
        for parent in (99, 100, 101):
            value = rows()
            for row in value:
                if row['side'] == 'candidate': row['actual_kernel_peak_bytes']['parent'] = parent
            self.assertTrue(all(not item['timing_and_memory_gates_passed'] for item in c.comparisons(value)))
        self.assertTrue(all(item['timing_and_memory_gates_passed'] for item in c.comparisons(rows())))

    def test_any_original_timing_failure_still_causes_fresh_futility(self):
        for name in c.TIMES:
            value = self.fresh()
            for row in value:
                if row['side'] == 'candidate': row['timings_seconds'][name] = 81 if name == c.TIMES[0] else 100.01
            with self.subTest(name=name): self.assertTrue(c.fresh_futility(value)['stop_before_primed'])

    def test_candidate_binding_accepts_explicit_new_commit_and_rejects_mismatch(self):
        version = {'commit': 'b' * 40, 'go_version': 'go1.27.2', 'arch': 'amd64', 'os': 'linux'}
        value = {'version': version, 'image_id': 'sha256:' + 'd' * 64,
                 'tool_sha256': {name: 'e' * 64 for name in ('/usr/bin/ffmpeg', '/usr/bin/ffprobe', '/usr/local/bin/cutmy')}}
        c.validate_binding(value, 'b' * 40, value['image_id'])
        with self.assertRaises(c.Refused): c.validate_binding(value, 'c' * 40, value['image_id'])

    def synthetic_run(self, fail):
        with tempfile.TemporaryDirectory(prefix='cutmy-cohort-v5-offline-') as raw:
            root = Path(raw); (root / 'proofs').mkdir(); (root / 'samples').mkdir()
            helper = str(root / 'inputs/backend.py'); driver = str(root / 'inputs/run-python.py')
            binding = {'version': {'commit': c.BASELINE_COMMIT, 'go_version': 'go1.27.2', 'arch': 'amd64', 'os': 'linux'},
                       'image_id': 'sha256:' + 'a' * 64,
                       'tool_sha256': {name: 'e' * 64 for name in ('/usr/bin/ffmpeg', '/usr/bin/ffprobe', '/usr/local/bin/cutmy')}}
            candidate = copy.deepcopy(binding); candidate['version']['commit'] = 'b' * 40
            candidate['image_id'] = 'sha256:' + 'b' * 64
            config = {'cohort_id': 'pure-v5', 'window_id': 'pure', 'candidate_commit': 'b' * 40,
                      'backend_path': helper, 'sdk_driver_path': driver, 'client_timeout_seconds': 900,
                      'pg_image_id': 'sha256:' + 'c' * 64, 'quality_inventory_path': str(root / 'inventory.json')}
            quality = {'mode': 'content_addressed_v1'}
            manifest = {'protocol_version': 6, 'samples': 24, 'pairs_per_stratum': 6, 'strata': list(c.STRATA),
                        'registered_at_utc': '2026-10-16T12:00:00+00:00', 'config': config,
                        'prospective_quality': quality,
                        'memory_protocol_decision': {'sha256': c.MEMORY_DECISION_SHA, 'decided_at_utc': '2026-10-16T11:36:03+00:00'},
                        'metric_gates': {}, 'diagnostic_gates': {}, 'coordinator_instrumentation': {},
                        'negative_only_futility': {'predeclared': True, 'after_valid_stopped_fresh_pairs': 6,
                             'after_completed_runs': 12, 'only_if_any_registered_v6_fresh_acceptance_gate_fails': True,
                             'diagnostic_role_failures_trigger_futility': False,
                             'no_primed_sample_starts_on_negative': True, 'positive_early_acceptance': False,
                             'resume_replacement_pooling_allowed': False},
                        'frozen_inputs': {str(Path(c.__file__)): 'f' * 64,
                             str(root / 'inputs/controller-cohort.py'): c.ORIGINAL_V4_SHA, helper: 'f' * 64, driver: 'f' * 64},
                        'images': {'baseline': {'reference': 'base', 'id': binding['image_id'], 'commit': c.BASELINE_COMMIT, 'binding': binding},
                                   'candidate': {'reference': 'candidate', 'id': candidate['image_id'], 'commit': 'b' * 40, 'binding': candidate}},
                        'limits': {}, 'application_config': {}, 'production_before_registration': {}}
            count = {'starts': 0}
            def start(args):
                value = c.load(args.config); sample = root / 'samples' / value['sample_id']; sample.mkdir()
                c.save(sample / 'backend-state.json', {'base_url': 'http://127.0.0.1:1234', 'status': 'ready'})
                count['starts'] += 1
            def client(args):
                spec = c.load(args.client); data = c.load(Path(spec['input'])); run = Path(data['run_dir']); run.mkdir()
                c.save(run / 'public-receipt.json', {'expected_server_commit_from_controller': data['server_commit'],
                       'controller_input_sha256': 'f' * 64, 'harness_sha256': 'f' * 64})
            def stop(args):
                sample = args.state.parent; data = c.load(sample / 'client-input.json')
                c.save(args.output, {'core_image_id': manifest['images'][data['role']]['id'],
                       'limits': {}, 'application_config': {}, 'status': 'collected_and_stopped'})
            def record(receipt, resources, side, stratum, pair):
                value = {'pair': pair, 'side': side, 'stratum': stratum,
                         'timings_seconds': {name: 70 if side == 'candidate' and name == c.TIMES[0] else 100 for name in c.TIMES},
                         'actual_kernel_peak_bytes': {name: 98 if side == 'candidate' and name == 'parent' else 100 for name in c.ROLES},
                         'all30_database_counts_passed': True, 'owned_cleanup_passed': True, 'production_unchanged': True}
                if fail and side == 'candidate': value['actual_kernel_peak_bytes']['parent'] = 99
                return value
            module = SimpleNamespace(PG_IMAGE='pinned', Refused=RuntimeError, start=start, client=client, collect_stop=stop)
            def sha(path, *args):
                return c.ORIGINAL_V4_SHA if Path(path) == root / 'inputs/controller-cohort.py' else 'f' * 64
            with patch.object(c, 'ROOT', root), patch.object(c, 'sha', side_effect=sha), \
                 patch.object(c, 'backend', return_value=module), patch.object(c, 'preconditions'), \
                 patch.object(c, 'prospective_quality', return_value=quality), patch.object(c, 'sample_record', side_effect=record), \
                 patch.object(c, 'memory_protocol_decision', return_value=manifest['memory_protocol_decision']), \
                 patch.object(c, 'coordinator_instrumentation', return_value={}), \
                 patch.object(c, 'verify_runtime_coordinator', return_value={'receipt_sha256':'f'*64,
                      'launch_intent_sha256':'e'*64,'launcher_sha256':'d'*64}), \
                 patch.object(c, 'coordinator_final_cost', return_value='c'*64), \
                 patch.object(c, 'host_oom_observation', return_value={'boot_id': 'same', 'oom_kill': 0}), \
                 patch('builtins.print'):
                registration = root / 'registration.json'; c.save(registration, manifest)
                c.save(root / 'inventory.json', {'frozen_at_utc': '2026-10-16T11:00:00+00:00'})
                result = c.run_cohort(SimpleNamespace(registration=registration, window='pure'))
                directory = root / 'proofs/cohort-pure-v5'
                return result, count['starts'], sorted(path.name for path in directory.iterdir())

    def test_real_controller_branch_stops_at12_never_starts13_or_public24(self):
        result, starts, files = self.synthetic_run(True)
        self.assertEqual(starts, 12)
        self.assertEqual(result['status'], 'negative_futility_incomplete_cohort')
        self.assertIn('NEGATIVE-FRESH-FUTILITY.json', files)
        self.assertIn('NEGATIVE-INCOMPLETE-TIMING-RESULT.json', files)
        self.assertNotIn('PUBLIC-TIMING-RESULT.json', files)
        self.assertNotIn('FAILED.json', files)

    def test_real_controller_branch_continues_all24_after_passing_fresh(self):
        result, starts, files = self.synthetic_run(False)
        self.assertEqual(starts, 24)
        self.assertEqual(result['completed_runs'], 24)
        self.assertFalse(result['performance_claim_ready'])
        self.assertIn('PUBLIC-TIMING-RESULT.json', files)
        self.assertNotIn('NEGATIVE-FRESH-FUTILITY.json', files)

    def test_direct_ssh_run_is_refused_before_any_sample(self):
        with patch.dict(os.environ, {'CUTMY_COORDINATOR_INTENT': ''}):
            with self.assertRaisesRegex(c.Refused, 'detached_coordinator_intent_required'):
                c.verify_runtime_coordinator({}, SimpleNamespace(), None)

    def test_runtime_guard_rejects_foreign_unit_before_kernel_snapshot(self):
        nonce = 'a' * 32; reg = c.ROOT / 'registration.json'
        intent_path = c.ROOT / 'proofs/COORDINATOR-example-LAUNCH.json'
        intent = {'kind': 'immutable_owned_coordinator_launch_intent', 'nonce':nonce,
                  'unit_name':'cutmyfirstdraftcoordinator'+nonce+'.service', 'cohort_id':'example',
                  'window':'win','registration_path':str(reg),'registration_sha256':'f'*64,
                  'controller_path':str(Path(c.__file__)),'controller_sha256':'f'*64,
                  'launcher_sha256':'e'*64,'policy':{}}
        manifest = {'config':{'cohort_id':'example','coordinator_launcher_sha256':'e'*64},'coordinator_instrumentation':{}}
        with patch.dict(os.environ, {'CUTMY_COORDINATOR_INTENT':str(intent_path),'INVOCATION_ID':'b'*32}), \
             patch.object(c,'safe_path',side_effect=lambda raw:Path(raw)),patch.object(c,'load',return_value=intent), \
             patch.object(c,'sha',return_value='f'*64),patch.object(Path,'read_text',return_value='0::/user.slice/foreign.service\n'):
            with self.assertRaisesRegex(c.Refused, 'coordinator_must_run_in_owned_outside_unit'):
                c.verify_runtime_coordinator(manifest, SimpleNamespace(registration=reg,window='win'),None)

    def runtime_fixture(self, changed=None):
        nonce='a'*32; reg=c.ROOT/'registration.json'; name='cutmyfirstdraftcoordinator'+nonce+'.service'
        intent_path=c.ROOT/'proofs/COORDINATOR-example-LAUNCH.json'
        description='cutmy.first-draft=20261016 coordinator='+nonce+' registration='+'f'*64
        intent={'kind':'immutable_owned_coordinator_launch_intent','nonce':nonce,'unit_name':name,'cohort_id':'example',
                'window':'win','registration_path':str(reg),'registration_sha256':'f'*64,
                'controller_path':str(Path(c.__file__)),'controller_sha256':'f'*64,
                'launcher_sha256':'e'*64,'policy':{},'description':description}
        manifest={'config':{'cohort_id':'example','coordinator_launcher_sha256':'e'*64},'coordinator_instrumentation':{}}
        kernel={'memory.max':str(128<<20),'memory.swap.max':'0','cpu.max':'10000 100000','pids.max':'64',
                'memory.peak':'1000000','memory.current':'900000','cpu.stat':'usage_usec 1000'}
        kernel.update(changed or {})
        module=SimpleNamespace(run=lambda *args,**kwargs:SimpleNamespace(stdout=(description+'\n').encode()))
        def read_text(path,*args,**kwargs):
            return '0::/system.slice/'+name+'\n' if str(path)=='/proc/self/cgroup' else kernel[path.name]
        with patch.dict(os.environ,{'CUTMY_COORDINATOR_INTENT':str(intent_path),'INVOCATION_ID':'b'*32}), \
             patch.object(c,'safe_path',side_effect=lambda raw:Path(raw)),patch.object(c,'load',return_value=intent), \
             patch.object(c,'sha',return_value='f'*64),patch.object(Path,'read_text',read_text), \
             patch.object(Path,'is_dir',return_value=True),patch.object(Path,'is_symlink',return_value=False), \
             patch.object(Path,'lstat',return_value=SimpleNamespace(st_uid=os.geteuid(),st_mode=stat.S_IFDIR|0o700)), \
             patch.object(c.resource,'getrlimit',return_value=(8<<20,8<<20)),patch.object(c,'save') as saved:
            value=c.verify_runtime_coordinator(manifest,SimpleNamespace(registration=reg,window='win'),module)
            self.assertTrue(saved.call_args.args[1]['actual_description_verified'])
            return value

    def test_positive_actual_coordinator_runtime_receipt(self):
        self.assertEqual(self.runtime_fixture()['receipt_sha256'],'f'*64)

    def test_actual_coordinator_limits_cannot_be_only_declarations(self):
        for name,value in (('memory.max','999'),('memory.swap.max','1'),('cpu.max','20000 100000'),('pids.max','65')):
            with self.subTest(name=name),self.assertRaisesRegex(c.Refused,'actual_coordinator_limits_changed'):
                self.runtime_fixture({name:value})


if __name__=='__main__':unittest.main()
