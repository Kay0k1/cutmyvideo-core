"""Synthetic negative controls for explicit006 acceptance; no host operations."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT=Path(__file__).resolve().parent

def module(name,file):
    spec=importlib.util.spec_from_file_location(name,ROOT/file)
    value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value

cleanup=module('cleanup_controls','source-media-cleanup-transition.py')
existing=module('cleanup_source_fixture','current_installation_acceptance_test.py')
base=existing.base
proof=base.transition_module()

class CleanupFixture(existing.SyntheticCurrentReceipt):
    def __init__(self,directory):
        super().__init__(directory)
        report=self.report;report.update(mode=cleanup.MODE,schema_version=4)
        value=report.pop('current_installation');admission=value['admission']
        admission.pop('schema_sources_unchanged');admission['schema_transition']=[5,6]
        self.value=value
        for name in (cleanup.SQL_NAME,cleanup.SHAPE_NAME):
            self.sources['current']['files'][name]=existing.blob((ROOT.parent/name).read_bytes())
        now=report['images']['current']
        now['tree']=existing.current.source_tree(self.sources['current']['files'])
        self.sources['current']['tree']=now['tree']
        report['checkout'].update(tree=now['tree'],head_tree=now['tree'])
        report['images']['restored']['tree']=now['tree']
        admission.update(checkout=deepcopy(report['checkout']),candidate_tree=now['tree'],
            candidate_application_files=len(existing.current.application(self.sources['current'])),
            candidate_application_sha256=proof.digest(proof.canonical(existing.current.application(self.sources['current']))))
        value['current_source']=self.write('current',self.sources['current'])
        now['source_sha256']=value['current_source']['sha256']
        report['images']['restored']['source_sha256']=now['source_sha256']
        report['source_media_cleanup_transition']=value
        value.pop('baseline')
        value['migrations']=[dict(position=i+1,version=v,checksum=d) for i,(v,_,d) in enumerate(proof.MIGRATIONS)]+[dict(position=6,version=cleanup.MIGRATION,checksum=cleanup.SQL_SHA256)]
        value.update(manifests={},catalogs={},guard_exit=1,guard_code='schema_incompatible',reverse_method='fresh_restore_coordinated005_backup',compatible_recovery_image_id=now['image_id'])
        shapes={}
        for position in (5,6):
            name=f'internal/app/migrations/{position:03d}_schema.json'
            raw=(ROOT.parent/name).read_bytes();shapes[position]=json.loads(raw)
            value['manifests'][str(position)]=self.raw('manifest'+str(position),raw)
        for phase,position in (('old005',5),('current006',6),('restored006',6),('rollback005',5)):
            value['catalogs'][phase]=self.raw(phase,dict(position=position,shape=shapes[position],context=admission,postgres_version_num=170011,
                versions=sorted(m['version'] for m in value['migrations'][:position]),migrations=value['migrations'][:position]))
        before={'sources':[dict(id='source',owner='owner',path='/data/source',duration_ms=10000)],
                'jobs':[dict(id='job',source_id='source',status='succeeded')],
                'artifacts':[dict(id='output',job_id='job',path='/data/output')],
                'storage_files':[dict(path='/data/source',size_bytes=10)],
                'storage_counters':[dict(id=1,stored_bytes=10,reserved_bytes=0)]}
        after=deepcopy(before);after['sources'][0]['media_removed']=False
        value['originals_before']=self.raw('originals-before',before)
        value['originals_after']=self.raw('originals-after',after)
        for key in ('guard_before','guard_after','restore_before','restore_after'):
            value[key]=self.raw(key,after)
        value['rollback_originals']=self.raw('rollback',before)
        value['cleanup']=self.raw('cleanup',dict(source_id='source',source={'media_removed':True},
            job={'id':'job','status':'succeeded','request':{'source_id':'source'}},
            decoded={'sha256':'a'*64},download_sha256='a'*64,foreign_status=404,removed_status=410,
            retained_job_id='job',physical_original_removed=True))
        checks=report['groups']['identity']['checks'];checks.pop('current_application_sources_bound')
        checks['cleanup_candidate_sources_bound']={'ok':True,'evidence':{'schema_transition':[5,6]}}
        report['groups']['source_media_cleanup_upgrade']={'status':'pass','checks':{name:{'ok':True} for name in cleanup.CHECKS}}

    def raw(self,name,value):
        data=value if isinstance(value,bytes) else proof.canonical(value)
        name='cleanup-'+name+'.json';(self.directory/name).write_bytes(data)
        return {'path':name,'bytes':len(data),'sha256':proof.digest(data)}

    def change_raw(self,key,change):
        value=json.loads(proof.artifact(self.directory,self.value[key]))
        change(value);self.value[key]=self.raw(key+'-mutated',value)

class CleanupReceiptControls(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.fixture=CleanupFixture(self.temp.name)

    def test_explicit006_mode_accepts_complete_bound_sources_and_real_evidence_shape(self):
        self.fixture.validate()

    def test_missing_extra_group_or_legacy_mode_cannot_claim006_acceptance(self):
        for fault in ('missing','mode','version','partial'):
            with self.subTest(fault=fault):
                fixture=CleanupFixture(self.temp.name)
                if fault=='missing':fixture.report['groups'].pop('source_media_cleanup_upgrade')
                elif fault=='mode':fixture.report['mode']='current-installation'
                elif fault=='version':fixture.report['schema_version']=3
                else:fixture.report['groups']['source_media_cleanup_upgrade']['checks'].pop(cleanup.CHECKS[-1])
                with self.assertRaises(RuntimeError):fixture.validate()

    def test_original_media_and_existing_records_cannot_change_during_upgrade(self):
        faults=[lambda v:v['sources'][0].update(media_removed=True),lambda v:v['jobs'][0].update(status='failed'),
                lambda v:v['artifacts'].clear(),lambda v:v['storage_counters'][0].update(stored_bytes=0)]
        for change in faults:
            with self.subTest(change=change):
                fixture=CleanupFixture(self.temp.name);fixture.change_raw('originals_after',change)
                with self.assertRaises(RuntimeError):fixture.validate()

    def test_actual_guard_and_restore_raw_records_must_match(self):
        for key in ('guard_after','restore_after','rollback_originals'):
            with self.subTest(key=key):
                fixture=CleanupFixture(self.temp.name);fixture.change_raw(key,lambda v:v['jobs'][0].update(status='failed'))
                with self.assertRaises(RuntimeError):fixture.validate()

    def test_cleanup_requires_owner_refusal_physical_removal_and_retained_download(self):
        for change in (lambda v:v.update(foreign_status=200),lambda v:v.update(removed_status=404),
                       lambda v:v.update(physical_original_removed=False),lambda v:v.update(download_sha256='b'*64),
                       lambda v:v['source'].update(media_removed=False),lambda v:v.update(retained_job_id='other')):
            with self.subTest(change=change):
                fixture=CleanupFixture(self.temp.name);fixture.change_raw('cleanup',change)
                with self.assertRaises(RuntimeError):fixture.validate()

    def test_catalog_position_and_history_are_exact(self):
        for fault in ('shape','history','position'):
            with self.subTest(fault=fault):
                fixture=CleanupFixture(self.temp.name)
                catalog=json.loads(proof.artifact(fixture.directory,fixture.value['catalogs']['current006']))
                if fault=='shape':catalog['shape']['Tables']['sources']['Columns']['media_removed']['Default']='true'
                elif fault=='history':catalog['migrations'][-1]['checksum']='0'*64
                else:catalog['position']=5
                fixture.value['catalogs']['current006']=fixture.raw('bad-catalog',catalog)
                with self.assertRaises(RuntimeError):fixture.validate()

    def test_wrong_recovery_method_or_image_is_rejected(self):
        for key,value in (('reverse_method','drop_columns_and_markers'),('guard_exit',0),('compatible_recovery_image_id','sha256:'+'0'*64)):
            with self.subTest(key=key):
                fixture=CleanupFixture(self.temp.name);fixture.value[key]=value
                with self.assertRaises(RuntimeError):fixture.validate()

    def test_source_bound006_catalog_manifest_cannot_be_changed(self):
        receipt=self.fixture.value['manifests']['6']
        (self.fixture.directory/receipt['path']).write_bytes(b'{}')
        with self.assertRaises(RuntimeError):self.fixture.validate()

class CleanupAdmissionControls(unittest.TestCase):
    def test_explicit_mode_uses005_predecessor_and_refuses_unrecognized_schema(self):
        args=base.parse_args(['--mode',cleanup.MODE,'--revision','1'*40,'--output','/synthetic-output'])
        self.assertEqual(args.upgrade_from,cleanup.BASELINE)
        old=existing.current.application({'files':existing.baseline_files()})
        candidate=deepcopy(old)
        for name in (cleanup.SQL_NAME,cleanup.SHAPE_NAME):candidate[name]=existing.blob((ROOT.parent/name).read_bytes())
        cleanup.validate_schema_inventory(old,candidate)
        for name in ('internal/app/migrations/007_future.sql','internal/app/migrations/006_unknown.sql'):
            invalid=deepcopy(candidate);invalid[name]=existing.blob(b'SELECT 1;\n')
            with self.assertRaises(RuntimeError):cleanup.validate_schema_inventory(old,invalid)
        invalid=deepcopy(candidate);invalid['internal/app/migrations/005_schema.json']['sha256']='0'*64
        with self.assertRaises(RuntimeError):cleanup.validate_schema_inventory(old,invalid)

    def test_wrong_mode_never_initializes_parent_or_host(self):
        init=Mock();fake=SimpleNamespace(Acceptance=type('FakeBase',(),{'__init__':init}),transition_module=lambda:proof,current_installation_module=lambda:existing.current)
        value=cleanup.acceptance_class(fake)
        for mode in (None,'current-installation','same-schema',proof.MODE):
            with self.assertRaises(RuntimeError):value(SimpleNamespace(mode=mode))
        init.assert_not_called()


class OriginalDeletionObservationControls(unittest.TestCase):
    def test_psql_json_scalar_preserves_charge_and_physical_file_gate(self):
        for charged,present,expected in ((True,False,True),(True,True,False),(False,False,False),(False,True,False)):
            with self.subTest(charged=charged,present=present):
                def sql(query):
                    self.assertTrue(query.startswith('SELECT to_jsonb(NOT EXISTS('))
                    self.assertIn("path='/data/sources/owner''s-source.media'",query)
                    return json.dumps(charged).encode()+b'\n'
                self.assertIs(cleanup.original_media_deleted(sql,"/data/sources/owner's-source.media",lambda path:present),expected)

    def test_bare_sql_boolean_is_not_mistaken_for_json_or_completion(self):
        for raw in (b't\n',b'f\n',b'null\n',b'1\n'):
            with self.subTest(raw=raw),self.assertRaises((ValueError,RuntimeError)):
                cleanup.original_media_deleted(lambda query:raw,'/data/sources/source.media',lambda path:False)
