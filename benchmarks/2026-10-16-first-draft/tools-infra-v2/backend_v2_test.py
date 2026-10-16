#!/usr/bin/env python3
"""Offline safety controls only; no actual Docker/systemd/backend launched."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('backend_v2', Path(__file__).with_name('backend-v2.py'))
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)


class OfflineControls(unittest.TestCase):
    def setUp(self):
        guard=patch.object(subprocess, 'run', side_effect=AssertionError('real process forbidden'))
        guard.start();self.addCleanup(guard.stop)
        guard=patch.object(subprocess, 'Popen', side_effect=AssertionError('real process forbidden'))
        guard.start();self.addCleanup(guard.stop)

    def state(self):
        identity='a'*32
        data={'identity':identity,'names':b.names(identity),'client_launched':True,
              'status':'ready','limits':b.LIMITS,'journal':[]}
        data['client_before']=self.client_unit(data,MainPID='1234',SubState='running')
        return data

    def client_unit(self,data,**changes):
        row={'Id':data['names']['client'],'LoadState':'loaded',
             'Description':f'{b.LABEL}={b.DATE} sample={data["identity"]} role=client',
             'InvocationID':'b'*32,'ExecMainPID':'1234','MainPID':'0',
             'ControlGroup':'/'+data['names']['slice']+'/'+data['names']['client'],
             'ActiveState':'active','SubState':'exited','ExecMainCode':'1','ExecMainStatus':'0',
             'Result':'success','MemoryPeak':'108687360','CPUUsageNSec':'123'}
        row.update(changes);return row

    def absent_unit(self,data,**changes):
        row={'Id':data['names']['client'],'LoadState':'not-found','MainPID':'0','ExecMainPID':'0',
             'ActiveState':'inactive','SubState':'dead','ControlGroup':'','InvocationID':''}
        row.update(changes);return row

    def missing_group(self,raw,parent=None,exists=True):
        if not exists:return Path('/synthetic/client-cgroup')
        raise b.Refused('owned_cgroup_missing')

    def failed_state(self):
        data=self.state();data['client_before']=self.client_unit(data,MainPID='1234',SubState='running')
        data['status']='collecting_failed'
        data['journal']=[{'event':'cohort_failed_before_bounded_collect_stop'}]
        return data

    def test_flat_units_do_not_create_production_ancestor(self):
        n = b.names('a' * 32)
        self.assertEqual(n['slice'], 'cutmyfirstdraft' + 'a' * 32 + '.slice')
        self.assertEqual(n['client'], 'cutmyfirstdraftclient' + 'a' * 32 + '.service')
        self.assertNotIn('-', n['slice'])
        self.assertNotIn('-', n['client'])

    def test_fixed_env_preserves_ordinary_admission(self):
        self.assertEqual(b.APP_ENV['MUTATIONS_PER_MINUTE'], '20')
        self.assertEqual(b.APP_ENV['WORKER_CONCURRENCY'], '1')
        self.assertEqual(b.APP_ENV['WORKER_PREPARATION_CONCURRENCY'], '0')
        self.assertEqual(b.APP_ENV['FFMPEG_THREADS'], '2')
        self.assertEqual(b.APP_ENV['FFMPEG_PATH'], '/usr/bin/ffmpeg')
        self.assertNotIn('POSTGRES_PASSWORD', b.safe_environment())
        self.assertNotIn('DATABASE_URL', b.safe_environment())

    def test_task_launch_overrides_inherited_compose_labels(self):
        args = b.label_args('a' * 32, 'worker')
        self.assertIn('com.docker.compose.project=cutmy-firstdraft-bench', args)
        self.assertIn('com.docker.compose.service=worker', args)

    def test_inherited_production_label_requires_full_retained_task_binding(self):
        identity = 'e' * 32
        expected = b.names(identity)
        sample = b.TASK_ROOT / 'samples' / 'pilot-baseline-fresh-01'
        data = {'kind': 'owned_first_draft_backend', 'identity': identity, 'names': expected,
                'sample_dir': str(sample), 'container_ids': {'worker': 'owned-id'}, 'core_image_id': 'owned-image'}
        value = {'id': 'owned-id', 'name': '/' + expected['containers']['worker'], 'image': 'owned-image',
                 'labels': {**b.labels(identity, 'worker'), 'com.docker.compose.project': 'cutmy',
                            'com.docker.compose.service': 'api'},
                 'cgroup_parent': expected['slice'], 'memory': 768 << 20, 'nano_cpus': 1000000000,
                 'pids_limit': 128, 'networks': {expected['network']: {}},
                 'mounts': [{'Type': 'bind', 'Source': str(sample / 'media'), 'Destination': '/data'}]}
        self.assertTrue(b.task_matches_retained_state(value, data))
        for key,replacement in [('id','other-id'),('image','other-image'),('name','/cutmy-api-1'),
                                ('cgroup_parent','system.slice'),('memory',1024),
                                ('networks',{'production': {}}),
                                ('mounts',[{'Type':'bind','Source':'/opt/cutmy/shared/media','Destination':'/data'}])]:
            changed = {**value, key: replacement}
            self.assertFalse(b.task_matches_retained_state(changed, data), key)

    def test_retained_task_is_excluded_but_mislabeled_non_task_is_not(self):
        identity = 'f' * 32
        expected = b.names(identity)
        sample = b.TASK_ROOT / 'samples' / 'pilot-baseline-fresh-01'
        task = {'id': 'owned-id', 'name': '/' + expected['containers']['api'], 'image': 'owned-image',
                'labels': {**b.labels(identity, 'api'), 'com.docker.compose.project': 'cutmy',
                           'com.docker.compose.service': 'api'},
                'cgroup_parent': expected['slice'], 'memory': 512 << 20, 'nano_cpus': 500000000,
                'pids_limit': 128, 'networks': {expected['network']: {}},
                'mounts': [{'Type': 'bind', 'Source': str(sample / 'media'), 'Destination': '/data'}]}
        state = {'kind': 'owned_first_draft_backend', 'identity': identity, 'names': expected,
                 'sample_dir': str(sample), 'container_ids': {'api': 'owned-id'}, 'core_image_id': 'owned-image'}
        def row(identifier):
            if identifier == 'task':return task
            return {'id': identifier, 'image': identifier, 'labels': {'com.docker.compose.service': identifier},
                    'state': {'Running': True, 'OOMKilled': False, 'Health': {'Status': 'healthy'},
                              'StartedAt': 'unchanged'}, 'mounts': []}
        roles = sorted(b.PRODUCTION_ROLES)
        with patch.object(b, 'load', return_value=state), \
             patch.object(b, 'docker', return_value=subprocess.CompletedProcess([],0,' '.join(roles+['task']).encode(),b'')), \
             patch.object(b, 'container', side_effect=row):
            self.assertEqual(set(b.production_snapshot()['roles']), b.PRODUCTION_ROLES)
        with patch.object(b, 'load', return_value={**state,'core_image_id':'wrong-image'}):
            self.assertFalse(b.retained_task_container(task))

    def test_no_actual_peak_counter_is_a_refusal(self):
        with tempfile.TemporaryDirectory(prefix='offline-cg-') as directory:
            p = Path(directory)
            (p / 'memory.current').write_text('123')
            with self.assertRaisesRegex(b.Refused, '^actual_kernel_peak_required$'):
                b.counters(p)
            (p / 'memory.peak').write_text('456')
            self.assertEqual(b.counters(p)['memory_peak_bytes'], 456)

    def test_container_wrong_owner_stops_before_mutation(self):
        identity = 'b' * 32
        data = {'identity': identity, 'names': b.names(identity)}
        value = {'labels': b.labels('c' * 32, 'worker')}
        with patch.object(b, 'container', return_value=value), patch.object(b, 'docker') as docker:
            with self.assertRaisesRegex(b.Refused, '^container_ownership_mismatch$'):
                b.owned_container(data, 'worker')
            docker.assert_not_called()

    def test_network_rejects_an_unowned_attached_member(self):
        identity = 'd' * 32
        data = {'identity': identity, 'names': b.names(identity)}
        value = {'id': 'x', 'labels': b.labels(identity, 'network'), 'internal': False,
                 'containers': {'other': {'Name': 'cutmy-api-1'}}}
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, json.dumps(value).encode(), b'')):
            with self.assertRaisesRegex(b.Refused, '^unowned_network_member$'):
                b.owned_network(data)

    def test_missing_published_port_is_a_fixed_refusal(self):
        with self.assertRaisesRegex(b.Refused, '^api_port_not_published$'):
            b.published_loopback_port({'8080/tcp': None}, 56845)
        with self.assertRaisesRegex(b.Refused, '^loopback_only_api_required$'):
            b.published_loopback_port({'8080/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '56845'}]}, 56845)
        with self.assertRaisesRegex(b.Refused, '^api_port_changed$'):
            b.published_loopback_port({'8080/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '56846'}]}, 56845)
        self.assertEqual(b.published_loopback_port({'8080/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '56845'}]}, 56845), '56845')

    def test_production_requires_five_distinct_healthy_roles(self):
        def row(identifier):
            return {'id': identifier, 'image': identifier, 'labels': {'com.docker.compose.service': identifier},
                    'state': {'Running': True, 'OOMKilled': False, 'Health': {'Status': 'healthy'},
                              'StartedAt': 'unchanged'}, 'mounts': []}
        roles = sorted(b.PRODUCTION_ROLES)
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, ' '.join(roles).encode(), b'')), \
             patch.object(b, 'container', side_effect=row):
            self.assertEqual(set(b.production_snapshot()['roles']), b.PRODUCTION_ROLES)
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, b'api worker', b'')), \
             patch.object(b, 'container', side_effect=row):
            with self.assertRaisesRegex(b.Refused, '^production_not_five_healthy$'):
                b.production_snapshot()

    def test_changed_production_identity_is_rejected(self):
        with patch.object(b, 'production_snapshot', return_value={'roles': {'api': {'id': 'new'}}}):
            with self.assertRaisesRegex(b.Refused, '^production_baseline_changed$'):
                b.assert_production({'roles': {'api': {'id': 'old'}}})

    def test_production_mount_identity_survives_private_json_roundtrip(self):
        def row(identifier):
            return {'id': identifier, 'image': identifier, 'labels': {'com.docker.compose.service': identifier},
                    'state': {'Running': True, 'OOMKilled': False, 'Health': {'Status': 'healthy'},
                              'StartedAt': 'unchanged'},
                    'mounts': [{'Type': 'volume', 'Name': 'untouched', 'Source': '/never-modified', 'Destination': '/data'}]}
        roles = sorted(b.PRODUCTION_ROLES)
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, ' '.join(roles).encode(), b'')), \
             patch.object(b, 'container', side_effect=row):
            before = b.production_snapshot()
            stored = json.loads(json.dumps(before))
            self.assertEqual(b.assert_production(stored)['roles'], stored['roles'])

    def test_raw_subprocess_error_never_becomes_public_exception(self):
        secret = b'postgres://user:never-print-this@host/db'
        with patch.object(b.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, b'', secret)):
            with self.assertRaisesRegex(b.Refused, '^example_failed$') as raised:
                b.run(['false'], 'example')
            self.assertNotIn('never-print-this', str(raised.exception))


    def test_successful_exited_client_uses_retained_peak_for_empty_or_stale_path(self):
        for group in ('','/owned/stale-client'):
            data=self.state();row=self.client_unit(data,ControlGroup=group)
            with self.subTest(group=group),patch.object(b,'unit',return_value=row) as reads,patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'counters') as counters:
                result=b.client_resources(data,Path('/synthetic/parent'))
                self.assertEqual(result['kernel_memory_peak_bytes'],108687360)
                self.assertIn('retained kernel MemoryPeak',result['scope'])
                self.assertEqual(reads.call_count,4);counters.assert_not_called()

    def test_counter_disappearance_requires_fresh_terminal_metadata(self):
        data=self.state();rows=[self.client_unit(data,MemoryPeak='1') for _ in range(3)]
        rows.append(self.client_unit(data,MemoryPeak='108687360'))
        with patch.object(b,'unit',side_effect=rows) as reads,patch.object(b,'cgroup_path',return_value=Path('/synthetic/group')),patch.object(b,'counters',side_effect=FileNotFoundError):
            result=b.client_resources(data,Path('/synthetic/parent'))
        self.assertEqual(result['kernel_memory_peak_bytes'],108687360)
        self.assertEqual(reads.call_count,4)

    def test_live_counter_preferred_and_transient_counter_race_can_recover(self):
        for race in (False,True):
            data=self.state();current=self.client_unit(data)
            kernel={'memory_peak_bytes':123,'memory.peak':'123','memory.current':'0','memory.events':'oom 0',
                    'cpu.max':'25000 100000','memory.max':str(256<<20),'memory.swap.max':'0','pids.max':'64'}
            values=[FileNotFoundError(),kernel] if race else [kernel]
            with self.subTest(race=race),patch.object(b,'unit',return_value=current) as reads,patch.object(b,'cgroup_path',return_value=Path('/synthetic/group')),patch.object(b,'counters',side_effect=values):
                result=b.client_resources(data,Path('/synthetic/parent'))
                self.assertEqual(result['kernel'],kernel)
                self.assertNotIn('kernel_memory_peak_bytes',result)
                self.assertEqual(reads.call_count,2 if race else 1)

    def test_running_missing_group_remains_fatal_and_exit_race_is_rechecked(self):
        data=self.state();running=self.client_unit(data,MainPID='1234',SubState='running')
        with patch.object(b,'unit',return_value=running) as reads,patch.object(b,'cgroup_path',side_effect=self.missing_group):
            with self.assertRaisesRegex(b.Refused,'^owned_cgroup_missing$'):b.owned_unit(data,'client')
            self.assertEqual(reads.call_count,2)
        with patch.object(b,'unit',side_effect=[running,self.client_unit(data)]) as reads,patch.object(b,'cgroup_path',side_effect=self.missing_group):
            self.assertTrue(b.successful_client(b.owned_unit(data,'client')))
            self.assertEqual(reads.call_count,2)

    def test_failed_cancelled_or_nonzero_client_cannot_supply_retained_measurement(self):
        for changes in ({'Result':'timeout'},{'ExecMainStatus':'1'},{'ExecMainCode':'2'},
                        {'ActiveState':'failed','SubState':'failed'}, {'MainPID':'1234','SubState':'running'}):
            data=self.state();row=self.client_unit(data,**changes)
            with self.subTest(changes=changes),patch.object(b,'unit',return_value=row),patch.object(b,'cgroup_path',side_effect=self.missing_group):
                with self.assertRaises(b.Refused):b.client_resources(data,Path('/synthetic/parent'))

    def test_no_zero_unknown_or_malformed_retained_peak(self):
        for peak in ('0','[not set]','','-1','nan'):
            data=self.state();row=self.client_unit(data,MemoryPeak=peak)
            with self.subTest(peak=peak),patch.object(b,'unit',return_value=row),patch.object(b,'cgroup_path',side_effect=self.missing_group):
                with self.assertRaisesRegex(b.Refused,'^actual_client_peak_unavailable$'):b.client_resources(data,Path('/synthetic/parent'))

    def test_bad_counter_permission_foreign_group_or_symlink_never_select_fallback(self):
        for error in (b.Refused('invalid_kernel_memory_counter'),PermissionError(),b.Refused('symlink_cgroup_refused')):
            data=self.state();row=self.client_unit(data)
            with self.subTest(error=type(error).__name__),patch.object(b,'unit',return_value=row) as reads,patch.object(b,'cgroup_path',return_value=Path('/synthetic/group')),patch.object(b,'counters',side_effect=error):
                with self.assertRaises(type(error)):b.client_resources(data,Path('/synthetic/parent'))
                self.assertEqual(reads.call_count,1)
        with patch.object(b,'unit',return_value=self.client_unit(self.state())),patch.object(b,'cgroup_path',side_effect=b.Refused('cgroup_outside_owned_slice')):
            with self.assertRaisesRegex(b.Refused,'^cgroup_outside_owned_slice$'):b.client_resources(self.state(),Path('/synthetic/parent'))

    def test_loaded_identity_invocation_or_pid_change_is_not_recoverable(self):
        for key,value in [('Id','other.service'),('LoadState','not-found'),('Description','foreign'),
                          ('InvocationID','c'*32),('ExecMainPID','9999')]:
            data=self.state();data['client_before']=self.client_unit(data)
            changed=self.client_unit(data,**{key:value})
            with self.subTest(key=key),patch.object(b,'unit',return_value=changed),patch.object(b,'cgroup_path',side_effect=self.missing_group):
                with self.assertRaises(b.Refused):b.client_resources(data,Path('/synthetic/parent'))

    def test_anchor_and_parent_missing_groups_still_refuse(self):
        data=self.state();anchor={'Id':data['names']['anchor'],'LoadState':'loaded','ControlGroup':'/missing',
            'Description':f'{b.LABEL}={b.DATE} sample={data["identity"]} role=anchor'}
        with patch.object(b,'unit',return_value=anchor),patch.object(b,'cgroup_path',side_effect=self.missing_group):
            with self.assertRaisesRegex(b.Refused,'^owned_cgroup_missing$'):b.owned_unit(data,'anchor')
        with patch.object(b,'cgroup_path',side_effect=b.Refused('owned_cgroup_missing')),patch.object(b,'owned_unit') as units:
            with self.assertRaisesRegex(b.Refused,'^owned_cgroup_missing$'):b.snapshot(data)
            units.assert_not_called()

    def test_exact_stopped_client_absence_skips_only_redundant_failed_cleanup(self):
        data=self.failed_state()
        with patch.object(b,'unit',return_value=self.absent_unit(data)),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'run') as run:
            b.stop_client_for_cleanup(data);run.assert_not_called()
        self.assertTrue(data['failed_cleanup_client_absence_verified']['failed_sample_only_not_measurement'])
        self.assertNotIn('client_finished',data)

    def test_absence_needs_failed_marker_and_prior_positive_identity(self):
        for change in ('ready','marker','before','wrong_owner','wrong_invocation','prior_zero_pid'):
            data=self.failed_state()
            if change=='ready':data['status']='ready'
            elif change=='marker':data['journal']=[]
            elif change=='before':del data['client_before']
            elif change=='wrong_owner':data['client_before']['Description']='foreign'
            elif change=='wrong_invocation':data['client_before']['InvocationID']='0'*32
            else:data['client_before']['ExecMainPID']='0'
            with self.subTest(change=change),patch.object(b,'unit',return_value=self.absent_unit(data)),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'run') as run:
                with self.assertRaises(b.Refused):b.stop_client_for_cleanup(data)
                run.assert_not_called()

    def test_ambiguous_not_found_unit_never_skips_stop(self):
        for key,value in [('Id','other.service'),('MainPID','123'),('ExecMainPID','123'),('ActiveState','active'),
                          ('SubState','running'),('ControlGroup','/foreign'),('InvocationID','b'*32)]:
            data=self.failed_state();current=self.absent_unit(data,**{key:value})
            with self.subTest(key=key),patch.object(b,'unit',return_value=current),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'run') as run:
                with self.assertRaisesRegex(b.Refused,'^absent_client_not_verified$'):b.stop_client_for_cleanup(data)
                run.assert_not_called()

    def test_loaded_terminal_cleanup_still_stops_exact_unit_and_running_refuses(self):
        for running in (False,True):
            data=self.failed_state();current=self.client_unit(data,MainPID='1234' if running else '0',SubState='running' if running else 'exited')
            with self.subTest(running=running),patch.object(b,'unit',return_value=current),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'run') as run:
                if running:
                    with self.assertRaisesRegex(b.Refused,'^client_still_running_preserve$'):b.stop_client_for_cleanup(data)
                    run.assert_not_called()
                else:
                    b.stop_client_for_cleanup(data)
                    run.assert_called_once_with(['systemctl','stop',data['names']['client']],'stop_client_unit')

    def test_failed_cleanup_integration_preserves_failure_and_stops_anchor_slice(self):
        data=self.failed_state();record=[]
        with patch.object(b,'unit',return_value=self.absent_unit(data)),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'owned_container',return_value=None),patch.object(b,'owned_network',return_value=None),patch.object(b,'owned_unit') as owned,patch.object(b,'counters',return_value={'memory_peak_bytes':321}),patch.object(Path,'exists',return_value=False),patch.object(b,'run',side_effect=lambda argv,step:record.append((argv,step))):
            b.clean_owned(data)
        self.assertEqual([step for _,step in record],['stop_anchor','stop_owned_slice'])
        owned.assert_called_once_with(data,'anchor')
        self.assertEqual(data['status'],'collecting_failed')
        self.assertTrue(data['resources_removed']['slice'])
        self.assertNotIn('post_client_resources',data)

    def test_unit_missing_response_is_only_opt_in_and_not_arbitrary_failure(self):
        data=self.state();raw='\n'.join(k+'='+v for k,v in self.absent_unit(data).items()).encode()
        with patch.object(b,'run',return_value=subprocess.CompletedProcess([],1,raw,b'')):
            self.assertEqual(b.unit(data['names']['client'],allow_missing=True)['LoadState'],'not-found')
            with self.assertRaisesRegex(b.Refused,'^inspect_unit_failed$'):b.unit(data['names']['client'])
        with patch.object(b,'run',return_value=subprocess.CompletedProcess([],1,b'LoadState=error',b'')):
            with self.assertRaisesRegex(b.Refused,'^inspect_unit_failed$'):b.unit(data['names']['client'],allow_missing=True)


    def test_failed_collect_receipt_cannot_become_valid_workload_or_metric(self):
        data=self.failed_state();directory=b.TASK_ROOT/'samples'/'synthetic-failed-sample'
        data.update(sample_dir=str(directory),sample_id='synthetic-failed-sample',side='baseline',
                    production_before={'roles':{}},application_config=b.APP_ENV,protocol_sha256='c'*64,
                    core_image_id='synthetic-core',pg_image_id='synthetic-pg')
        saved=[]
        def cleanup(value):
            value['resources_removed']={k:True for k in ('containers','network','anchor','slice')}
            value['parent_after_container_stop']={'memory_peak_bytes':321}
            value['failed_cleanup_client_absence_verified']={'failed_sample_only_not_measurement':True}
        with patch.object(b,'own_state',return_value=data),patch.object(b,'contained',side_effect=lambda path:Path(path)),patch.object(Path,'exists',return_value=False),patch.object(b,'logs',return_value={}),patch.object(b,'assert_production',return_value=data['production_before']),patch.object(b,'save',side_effect=lambda path,value,**kwargs:saved.append(json.loads(json.dumps(value)))),patch.object(b,'save_state'),patch.object(b,'clean_owned',side_effect=cleanup),patch.object(b,'snapshot') as snapshot,patch.object(b,'database_counts') as counts:
            result=b.collect_stop(type('Args',(),{'state':directory/'backend-state.json','output':directory/'failed-sample-backend-result.json'})())
        snapshot.assert_not_called();counts.assert_not_called()
        self.assertTrue(result['ok'])
        final=saved[-1]
        self.assertTrue(final['failed_sample_cleanup_only'])
        self.assertFalse(final['full30_workload_verified'])
        self.assertNotIn('resources_before_stop',final)
        self.assertTrue(data['failed_sample_cleanup_only'])


    def test_partial_live_counters_after_gc_cannot_return_incomplete_kernel_receipt(self):
        for missing in ('memory.events','cpu.max','memory.max','memory.swap.max','pids.max'):
            data=self.state();row=self.client_unit(data)
            kernel={'memory_peak_bytes':108687360,'memory.peak':'108687360','memory.current':'0',
                    'memory.events':'oom 0','cpu.max':'25000 100000','memory.max':str(256<<20),
                    'memory.swap.max':'0','pids.max':'64'}
            kernel[missing]=None
            with self.subTest(missing=missing),patch.object(b,'unit',return_value=row) as reads,patch.object(b,'cgroup_path',return_value=Path('/synthetic/group')),patch.object(b,'counters',return_value=kernel),patch.object(Path,'exists',return_value=False):
                result=b.client_resources(data,Path('/synthetic/parent'))
                self.assertEqual(result['kernel_memory_peak_bytes'],108687360)
                self.assertNotIn('kernel',result)
                self.assertEqual(reads.call_count,4)


    def test_missing_required_file_in_existing_cgroup_is_never_retained_fallback(self):
        for error in (b.Refused('actual_kernel_peak_required'),b.Refused('actual_kernel_current_required'),
                      b.Refused('actual_client_counter_disappeared'),FileNotFoundError()):
            data=self.state();row=self.client_unit(data)
            with self.subTest(error=type(error).__name__),patch.object(b,'unit',return_value=row) as reads,patch.object(b,'cgroup_path',return_value=Path('/synthetic/group')),patch.object(b,'counters',side_effect=error),patch.object(Path,'exists',return_value=True):
                with self.assertRaisesRegex(b.Refused,'^incomplete_existing_client_cgroup$'):b.client_resources(data,Path('/synthetic/parent'))
                self.assertEqual(reads.call_count,4)


    def test_cleared_loaded_inactive_client_skips_only_failed_redundant_stop(self):
        data=self.failed_state()
        current=self.absent_unit(data,LoadState='loaded',Description=data['client_before']['Description'])
        with patch.object(b,'unit',return_value=current),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'run') as run:
            b.stop_client_for_cleanup(data);run.assert_not_called()
        self.assertEqual(data['failed_cleanup_client_absence_verified']['load_state'],'loaded')
        for key,value in [('Description','foreign'),('MainPID','9876'),('ExecMainPID','9876'),
                          ('InvocationID','c'*32),('ControlGroup','/foreign'),('ActiveState','active')]:
            data=self.failed_state();changed={**current,key:value}
            with self.subTest(key=key),patch.object(b,'unit',return_value=changed),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'run') as run:
                with self.assertRaises(b.Refused):b.stop_client_for_cleanup(data)
                run.assert_not_called()
        data=self.state();data['client_before']=self.client_unit(data)
        with patch.object(b,'unit',return_value=current),patch.object(b,'cgroup_path',side_effect=self.missing_group),patch.object(b,'run') as run:
            with self.assertRaisesRegex(b.Refused,'^failed_cleanup_marker_required$'):b.stop_client_for_cleanup(data)
            run.assert_not_called()


    def test_retained_measurement_requires_prior_saved_execution_identity(self):
        data=self.state();del data['client_before'];row=self.client_unit(data)
        with patch.object(b,'unit',return_value=row),patch.object(b,'cgroup_path',side_effect=self.missing_group):
            with self.assertRaisesRegex(b.Refused,'^retained_client_identity_required$'):b.client_resources(data,Path('/synthetic/parent'))


if __name__ == '__main__':
    unittest.main()
