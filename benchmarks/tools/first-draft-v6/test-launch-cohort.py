"""Pure local launch controls. Never execute systemd/subprocess/network."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location('cohort_launcher', HERE / 'launch-cohort.py')
M = importlib.util.module_from_spec(SPEC); SPEC.loader.exec_module(M)


class Controls(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='cutmy-launcher-offline-')
        self.root = Path(self.tmp.name)
        (self.root / 'proofs').mkdir(mode=0o700); (self.root / 'inputs').mkdir(mode=0o700)
        self.local_file = self.root / 'inputs/launch-cohort.py'
        self.local_file.write_bytes((HERE / 'launch-cohort.py').read_bytes()); self.local_file.chmod(0o600)
        self.patches = [patch.object(M,'ROOT',self.root),patch.object(M,'__file__',str(self.local_file)),
                        patch.object(subprocess,'Popen',side_effect=AssertionError('process forbidden'))]
        for item in self.patches: item.start()
        (self.root / '.first-draft-cohort-window.lock').touch(mode=0o600)
        self.controller = self.root / 'inputs/controller-cohort-v6.py'
        self.controller.write_text('synthetic controller'); self.controller.chmod(0o600)
        self.nonce = 'a' * 32
        self.manifest = {'protocol_version':6,'samples':24,'pairs_per_stratum':6,
            'config':{'cohort_id':'pure','window_id':'pure-window','coordinator_launcher_path':str(self.local_file),
                      'coordinator_launcher_sha256':M.digest(self.local_file)},
            'coordinator_instrumentation':M.policy(M.digest(self.local_file)),
            'frozen_inputs':{str(self.local_file):M.digest(self.local_file),str(self.controller):M.digest(self.controller)}}
        self.registration = self.root / 'registration.json'; self.write(self.registration,self.manifest)
        self.request = {'kind':'cutmy_first_draft_detached_coordinator','nonce':self.nonce,
            'registration_path':str(self.registration),'registration_sha256':M.digest(self.registration),
            'controller_path':str(self.controller),'controller_sha256':M.digest(self.controller),
            'window':'pure-window','python_path':'/usr/bin/python3'}
        self.request_path = self.root / 'request.json'; self.write(self.request_path,self.request)
        self.argv = []

    def tearDown(self):
        for item in reversed(self.patches): item.stop()
        self.tmp.cleanup()

    def write(self,path,value):
        path.write_text(json.dumps(value)); path.chmod(0o600)

    def fields(self):
        return {'LoadState':'loaded','Description':M.description(self.request),'ActiveState':'active','SubState':'running',
            'ControlGroup':'/system.slice/'+M.unit_name(self.request),'MainPID':'123','ExecMainStatus':'0','Result':'success',
            'MemoryMax':str(128<<20),'MemorySwapMax':'0','TasksMax':'64','CPUQuotaPerSecUSec':'100ms',
            'RuntimeMaxUSec':'4h','RemainAfterExit':'yes','LimitFSIZE':str(8<<20),'CPUUsageNSec':'100','MemoryPeak':'1024'}

    def command(self,argv,timeout=20,check=True):
        self.argv.append(argv)
        if argv[0] == '/usr/bin/systemd-run': return SimpleNamespace(returncode=0,stdout=b'',stderr=b'')
        if '--value' in argv: return SimpleNamespace(returncode=0,stdout=b'not-found\n',stderr=b'')
        return SimpleNamespace(returncode=0,stdout=('\n'.join(k+'='+v for k,v in self.fields().items())+'\n').encode(),stderr=b'')

    def test_positive_detached_launch_policy_private_logs_and_actual_intent(self):
        with patch.object(M,'command',side_effect=self.command): result=M.launch(self.request_path)
        self.assertTrue(result['launched'])
        self.assertNotIn(self.nonce,json.dumps(result))
        directory=self.root/'proofs'/('coordinator-'+self.nonce)
        self.assertEqual((directory/'coordinator.log').stat().st_mode & 0o777,0o600)
        self.assertEqual((directory/'coordinator.log').stat().st_size,0)
        self.assertTrue((directory/'LAUNCH-ACCEPTED.json').is_file())
        run=next(row for row in self.argv if row[0]=='/usr/bin/systemd-run')
        for expected in ('--property=MemoryMax=128M','--property=CPUQuota=10%','--property=TasksMax=64',
                         '--property=RemainAfterExit=yes','--property=RuntimeMaxSec=14400','--property=LimitFSIZE=8388608'):
            self.assertIn(expected,run)
        self.assertTrue(any(row.startswith('--property=StandardOutput=append:') for row in run))
        self.assertTrue(any(row.startswith('--setenv=CUTMY_COORDINATOR_INTENT=') for row in run))

    def test_duplicate_request_cannot_take_over_or_retry(self):
        with patch.object(M,'command',side_effect=self.command):
            M.launch(self.request_path)
            with self.assertRaisesRegex(M.Refused,'new_run_once_coordinator_required'): M.launch(self.request_path)
        self.assertEqual(sum(row[0]=='/usr/bin/systemd-run' for row in self.argv),1)

    def test_lost_start_reply_preserves_intent_and_refuses_retry(self):
        def broken(argv,*args,**kwargs):
            if argv[0]=='/usr/bin/systemd-run': raise subprocess.TimeoutExpired('owned',20)
            return self.command(argv,*args,**kwargs)
        with patch.object(M,'command',side_effect=broken):
            with self.assertRaises(subprocess.TimeoutExpired): M.launch(self.request_path)
            with self.assertRaisesRegex(M.Refused,'new_run_once_coordinator_required'): M.launch(self.request_path)
        self.assertTrue((self.root/'proofs/COORDINATOR-pure-LAUNCH.json').exists())

    def test_existing_cohort_and_busy_window_are_refused(self):
        (self.root/'proofs/cohort-pure').mkdir()
        with patch.object(M,'command',side_effect=AssertionError('must refuse first')):
            with self.assertRaisesRegex(M.Refused,'new_run_once_coordinator_required'): M.launch(self.request_path)
        (self.root/'proofs/cohort-pure').rmdir()
        with (self.root/'.first-draft-cohort-window.lock').open('rb') as lock:
            M.fcntl.flock(lock,M.fcntl.LOCK_EX|M.fcntl.LOCK_NB)
            with self.assertRaisesRegex(M.Refused,'timing_or_quality_window_still_active'): M.launch(self.request_path)

    def test_metadata_cannot_retarget_controller_or_registration(self):
        self.controller.write_text('changed controller')
        with self.assertRaisesRegex(M.Refused,'exact_frozen_controller_or_registration_required'): M.validated(self.request_path)

    def test_direct_protocol_or_policy_relabel_is_refused(self):
        for field in ('protocol_version','coordinator_instrumentation'):
            manifest=copy.deepcopy(self.manifest)
            if field=='protocol_version': manifest[field]=5
            else: manifest[field]['cpu_quota_percent']=11
            self.write(self.registration,manifest)
            request=copy.deepcopy(self.request);request['registration_sha256']=M.digest(self.registration)
            self.write(self.request_path,request)
            with self.subTest(field=field),self.assertRaisesRegex(M.Refused,'prospective_detached_instrumentation_not_registered'):
                M.validated(self.request_path)

    def test_foreign_unit_and_limits_fail_closed(self):
        for field,value in (('Description','foreign'),('ControlGroup','/cutmyfirstdraftbad.slice/a.service'),
                            ('MemoryMax','999'),('CPUQuotaPerSecUSec','200ms')):
            values=self.fields();values[field]=value
            with patch.object(M,'command',return_value=SimpleNamespace(stdout=('\n'.join(k+'='+v for k,v in values.items())).encode())):
                with self.subTest(field=field),self.assertRaises(M.Refused): M.unit(self.request)


if __name__=='__main__': unittest.main(verbosity=2)
