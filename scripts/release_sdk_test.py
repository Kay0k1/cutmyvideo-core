"""SDK publication controls use synthetic bytes, not real signing evidence."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

SPEC = importlib.util.spec_from_file_location('release_sdk_tested', Path(__file__).with_name('release-sdk.py'))
s = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(s)


class SDKReleaseControls(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.evidence = self.root / 'evidence'
        self.evidence.mkdir()
        (self.evidence / 'python-dist').mkdir()
        self.repo = self.root / 'source'
        (self.repo / 'sdk/typescript').mkdir(parents=True)
        (self.repo / 'sdk/typescript/package.json').write_text('{"version":"1.4.0"}')
        self.source = {'commit': 'a'*40, 'tree': 'b'*40, 'version': 'v1.4.0', 'mode': 'release', 'publishable': True,
                       'files': {'sdk/input.py': {'sha256': 'c'*64}, s.PROJECT_CONTRACTS: {'sha256': 'e'*64}}}
        self.run = {'repository': s.REPOSITORY, 'run_id': 123, 'run_attempt': 2}
        self.names = s.package_names('1.4.0')
        self.receipt = {'installed_consumers': True, 'required_tests': sorted(s.required_tests()),
                        'source_inputs_sha256': {'sdk/input.py': 'c'*64, s.PROJECT_CONTRACTS: 'e'*64}}
        for kind, name in self.names.items():
            path = self.evidence / ('python-dist' if kind != 'npm' else '') / name
            raw = (kind+'-tested-package').encode()
            path.write_bytes(raw)
            self.receipt[{'wheel':'python_wheel_sha256','sdist':'python_sdist_sha256','npm':'npm_tarball_sha256'}[kind]] = s.m.digest(raw)
        (self.evidence / 'SDK-CHECKS.json').write_bytes(s.m.canonical(self.receipt))
        self.directory = self.root / 'sealed'

    def seal(self):
        return s.seal(self.directory, self.evidence, self.source, self.repo)

    def rewrite(self, value):
        (self.directory / s.MANIFEST).write_bytes(s.m.canonical(value))

    def test_exact_tested_package_bytes_and_source_identity(self):
        value = self.seal()
        again, subjects = s.load_manifest(self.directory, self.source)
        self.assertEqual(value, again)
        self.assertEqual(set(subjects), set(self.names.values()) | {s.MANIFEST})
        for kind, name in self.names.items():
            origin = self.evidence / ('python-dist' if kind != 'npm' else '') / name
            self.assertEqual(origin.read_bytes(), (self.directory / name).read_bytes())

    def test_refuses_uninstalled_incomplete_or_different_source_receipt(self):
        for key, bad in [('installed_consumers', False), ('required_tests', []),
                         ('source_inputs_sha256', {'sdk/input.py':'d'*64}), ('python_wheel_sha256','d'*64)]:
            with self.subTest(key=key):
                receipt = deepcopy(self.receipt); receipt[key] = bad
                (self.evidence/'SDK-CHECKS.json').write_bytes(s.m.canonical(receipt))
                with self.assertRaises(ValueError): self.seal()

    def test_refuses_receipt_without_the_resolved_shared_contract_examples(self):
        for digest in (None, 'f' * 64):
            with self.subTest(digest=digest):
                receipt = deepcopy(self.receipt)
                if digest is None:
                    receipt['source_inputs_sha256'].pop(s.PROJECT_CONTRACTS)
                else:
                    receipt['source_inputs_sha256'][s.PROJECT_CONTRACTS] = digest
                (self.evidence / 'SDK-CHECKS.json').write_bytes(s.m.canonical(receipt))
                with self.assertRaisesRegex(ValueError, 'shared project contract examples'):
                    self.seal()

    def test_refuses_changed_package_or_noncanonical_metadata(self):
        self.seal()
        path = self.directory/self.names['npm']
        raw = path.read_bytes();path.write_bytes(raw+b'x')
        with self.assertRaises(ValueError): s.load_manifest(self.directory,self.source)
        path.write_bytes(raw)
        path=self.directory/s.MANIFEST
        path.write_bytes(path.read_bytes()+b' ')
        with self.assertRaises(ValueError): s.load_manifest(self.directory,self.source)

    def test_refuses_unknown_duplicate_mixed_version_and_incomplete_manifest(self):
        good=self.seal()
        mutations=[]
        bad=deepcopy(good);bad['source_commit']='d'*40;mutations.append(bad)
        bad=deepcopy(good);bad['package_version']='1.4.1';mutations.append(bad)
        bad=deepcopy(good);bad['packages'][1]=bad['packages'][0];mutations.append(bad)
        bad=deepcopy(good);bad['acceptance']['required_tests']=[];mutations.append(bad)
        bad=deepcopy(good);bad['unknown']=True;mutations.append(bad)
        for bad in mutations:
            with self.subTest(bad=bad):
                self.rewrite(bad)
                with self.assertRaises(ValueError): s.load_manifest(self.directory,self.source)

    def test_fixed_artifact_members_and_no_traversal(self):
        self.seal()
        archive=self.root/'artifact.zip'
        with zipfile.ZipFile(archive,'w') as out:
            for path in self.directory.iterdir():out.writestr(path.name,path.read_bytes())
        destination=self.root/'downloaded'
        s.extract_sdk(archive,destination,self.source)
        self.assertEqual({p.name for p in destination.iterdir()},set(self.names.values())|{s.MANIFEST})
        with zipfile.ZipFile(archive,'w') as out:
            for path in self.directory.iterdir():out.writestr('../'+path.name,path.read_bytes())
        with self.assertRaises(ValueError):s.extract_sdk(archive,self.root/'unsafe',self.source)

    def fake_verified_statement(self, subjects):
        ref='refs/tags/'+self.source['version']
        repository='https://github.com/'+s.REPOSITORY
        invocation=repository+'/actions/runs/'+str(self.run['run_id'])+'/attempts/'+str(self.run['run_attempt'])
        return [{'verificationResult':{'signature':{'certificate':{'runInvocationURI':invocation}},'statement':{
            'subject':[{'name':name,'digest':{'sha256':digest}} for name,digest in subjects.items()],
            'predicateType':s.PREDICATE,
            'predicate':{'buildDefinition':{
                'buildType':'https://actions.github.io/buildtypes/workflow/v1',
                'externalParameters':{'workflow':{'ref':ref,'repository':repository,'path':'.github/workflows/release.yml'}},
                'resolvedDependencies':[{'uri':'git+'+repository+'@'+ref,'digest':{'gitCommit':self.source['commit']}}],
                'internalParameters':{'github':{'event_name':'push','runner_environment':'github-hosted'}}},
                'runDetails':{'builder':{'id':'https://github.com/'+s.WORKFLOW+'@'+ref},'metadata':{'invocationId':invocation}}}
        }}}]

    def test_signature_policy_requires_every_exact_subject_and_tagged_workflow(self):
        self.seal()
        (self.directory/s.SIGNATURE).write_text('{"synthetic":"not a real signature"}')
        _,subjects=s.load_manifest(self.directory,self.source)
        good=self.fake_verified_statement(subjects)
        commands=[]
        def command(argv):commands.append(argv);return json.dumps(good).encode()
        s.verify_signed(self.directory,self.source,expected_run=self.run,command=command)
        self.assertEqual(len(commands),4)
        for argv in commands:
            self.assertIn('--deny-self-hosted-runners',argv)
            self.assertEqual(argv[argv.index('--source-digest')+1],self.source['commit'])
            self.assertEqual(argv[argv.index('--source-ref')+1],'refs/tags/v1.4.0')
        missing=deepcopy(good);missing[0]['verificationResult']['statement']['subject'].pop()
        wrong=deepcopy(good);wrong[0]['verificationResult']['statement']['predicate']['buildDefinition']['internalParameters']['github']['event_name']='pull_request'
        for bad in [missing,wrong]:
            with self.assertRaises(ValueError):s.verify_signed(self.directory,self.source,expected_run=self.run,command=lambda _:json.dumps(bad).encode())

    def test_mixed_run_attempt_certificate_and_predicate_are_refused(self):
        self.seal()
        (self.directory/s.SIGNATURE).write_text('{"synthetic":"not a real signature"}')
        _,subjects=s.load_manifest(self.directory,self.source)
        good=self.fake_verified_statement(subjects)
        other='https://github.com/'+s.REPOSITORY+'/actions/runs/124/attempts/2'
        attempt='https://github.com/'+s.REPOSITORY+'/actions/runs/123/attempts/1'
        cases=[]
        for invocation in (other,attempt):
            for certificate,predicate in ((True,False),(False,True),(True,True)):
                bad=deepcopy(good)
                if certificate:bad[0]['verificationResult']['signature']['certificate']['runInvocationURI']=invocation
                if predicate:bad[0]['verificationResult']['statement']['predicate']['runDetails']['metadata']['invocationId']=invocation
                cases.append(bad)
        missing=deepcopy(good);missing[0]['verificationResult']['signature']['certificate'].clear();cases.append(missing)
        wrong_builder=deepcopy(good);wrong_builder[0]['verificationResult']['statement']['predicate']['runDetails']['builder']['id']='https://github.com/other/workflow@refs/tags/v1.4.0';cases.append(wrong_builder)
        for bad in cases:
            with self.subTest(bad=bad),self.assertRaises(ValueError):
                s.verify_signed(self.directory,self.source,expected_run=self.run,command=lambda _:json.dumps(bad).encode())
        for run in ({},dict(self.run,run_attempt=0),dict(self.run,run_id=True),dict(self.run,repository='other/repo')):
            with self.assertRaises(ValueError):
                s.verify_signed(self.directory,self.source,expected_run=run,command=lambda _:json.dumps(good).encode())

    def test_old_releases_keep_thirteen_assets_new_sdk_release_has_eighteen(self):
        spec=importlib.util.spec_from_file_location('consumer_for_sdk',Path(__file__).with_name('release-consumer.py'))
        consumer=importlib.util.module_from_spec(spec);spec.loader.exec_module(consumer)
        self.assertEqual(len(consumer.asset_names('v1.3.0')),13)
        self.assertEqual(len(consumer.asset_names('v1.4.0')),18)
        self.assertEqual(s.release_names('v1.4.0'),set(self.names.values())|{s.MANIFEST,s.SIGNATURE})


if __name__=='__main__':unittest.main()
