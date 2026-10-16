"""Negative controls for source, permission and attempt boundaries of the DAG."""

from copy import deepcopy
from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
COMMIT = '${{ needs.resolve.outputs.commit }}'
ATTEMPT = '${{ github.run_attempt }}'
RUN = '${{ github.run_id }}'


def require(condition, message):
    if not condition:
        raise ValueError(message)


def audit(release, source_ci):
    jobs = release['jobs']
    # PyYAML's YAML 1.1 loader treats the unquoted Actions key `on` as True.
    source_events = source_ci.get('on', source_ci.get(True))
    release_events = release.get('on', release.get(True))
    require('pull_request' not in source_events and 'workflow_call' in source_events
            and 'pull_request' in release_events, 'PR source checks must run exactly once through the archive workflow')
    require(jobs['checks']['uses'] == './.github/workflows/ci.yml', 'Archive admission requires the complete source workflow')
    require({'sdk', 'sdk-http', 'artifact-policy', 'native', 'bookworm-media', 'test'} <= set(source_ci['jobs']),
            'All source, SDK, production-media and artifact checks remain required')
    targets = {'ubuntu-24.04', 'ubuntu-24.04-arm', 'macos-15', 'macos-15-intel', 'windows-2025'}
    for name in ('sdk', 'native'):
        strategy = source_ci['jobs'][name]['strategy']
        require(set(strategy['matrix']['runner']) == targets and len(strategy['matrix']['runner']) == len(targets)
                and strategy['fail-fast'] is False and 'if' not in source_ci['jobs'][name],
                'Both five-platform matrices must run completely')
    require(release['concurrency']['cancel-in-progress'] == "${{ github.event_name == 'pull_request' }}",
            'Superseded PRs must cancel without interrupting tag/manual verification')
    require(source_ci['concurrency']['cancel-in-progress'] == "${{ github.event_name == 'push' && github.ref == 'refs/heads/main' }}"
            and source_ci['concurrency']['group'].startswith('core-checks-')
            and release['concurrency']['group'].startswith('cli-archives-'),
            'Main checks may cancel stale pushes without cancelling their reusable caller')
    require(release['permissions'] == {'contents': 'read', 'actions': 'read'}, 'Builders require read-only permissions')
    require(jobs['checks']['with']['ref'] == COMMIT and jobs['checks']['needs'] == 'resolve',
            'Source CI must use the single immutable resolver output')
    for name in ('packages', 'sdk-packages', 'native', 'archive-recovery', 'native-gate', 'trust', 'attest', 'draft'):
        checkouts = [step for step in jobs[name]['steps'] if step.get('uses', '').startswith('actions/checkout@')]
        require(len(checkouts) == 1 and checkouts[0]['with']['ref'] == COMMIT,
                'Every consumer must check out the immutable source')
    gate = jobs['native-gate']
    require(gate['if'] == 'always()' and set(gate['needs']) == {'resolve', 'checks', 'packages', 'native', 'archive-recovery'},
            'The required gate must observe every prerequisite even after failure')
    require(set(jobs['draft']['needs']) == {'resolve', 'packages', 'native-gate', 'trust', 'attest', 'sdk-packages'}
            and jobs['draft']['if'] == "github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')",
            'Publication must be gated and restricted to tags')
    require(set(jobs['trust']['needs']) == {'resolve', 'packages', 'native-gate'}
            and set(jobs['attest']['needs']) == {'resolve', 'packages', 'native-gate', 'trust', 'sdk-packages'}
            and jobs['attest']['if'] == jobs['draft']['if'], 'Trust and tag signing must follow the complete archive gate')
    require(jobs['attest']['permissions'] == {'contents': 'read', 'actions': 'read', 'id-token': 'write', 'attestations': 'write'},
            'Only the isolated tag signer may receive OIDC and attestation permissions')
    for name, job in jobs.items():
        require(job.get('permissions', {}).get('contents', 'read') == ('write' if name == 'draft' else 'read'),
                'Only the tag publisher may write repository contents')
        for permission in ('id-token', 'attestations'):
            require(job.get('permissions', {}).get(permission, 'none') == ('write' if name == 'attest' else 'none'),
                    'Builder/candidate/publisher must not inherit signing permissions')
    signing = [s for s in jobs['attest']['steps'] if s.get('uses', '').startswith('actions/attest@')]
    require(len(signing) == 3 and all(s['uses'] == 'actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6'
            and 'subject-checksums' in s['with'] and 'subject-path' not in s['with'] and s['with']['create-storage-record'] is False for s in signing),
            'Signing requires pinned official action and explicit verified subject hashes')
    require(signing[0]['with']['subject-checksums'].endswith('/release-trust/ATTESTATION-SUBJECTS.sha256')
            and signing[1]['with']['subject-checksums'].endswith('/release-bundle/SHA256SUMS')
            and signing[1]['with']['sbom-path'].endswith('/release-trust/SBOM.spdx.json')
            and signing[2]['with']['subject-checksums'].endswith('/sdk-subjects.sha256'), 'Wrong provenance or SPDX signing inputs')
    for name in ('trust', 'attest', 'draft'):
        runs = '\n'.join(s.get('run', '') for s in jobs[name]['steps'])
        require('go build' not in runs and 'release.sh' not in runs, 'Trust/signing/publication must never rebuild')
        require('scripts/release-fetch.py' in runs and '--expected-artifact-sha256' in runs, 'Trust must re-fetch exactly sealed bytes')
    attest_runs = '\n'.join(s.get('run', '') for s in jobs['attest']['steps'])
    require('prepare-signing' in attest_runs and 'finalize' in attest_runs and 'git fetch --force --no-tags' in attest_runs,
            'Signatures require release-only source/trust checks and actual signature verification')
    draft_runs = '\n'.join(s.get('run', '') for s in jobs['draft']['steps'])
    require('fetch-attested' in draft_runs and 'verify-attested' in draft_runs and '/release-bundle/*' not in draft_runs
            and 'bundle+sidecars+sdk' in draft_runs, 'Publisher must reverify signatures and upload an exact asset list')
    sdk_runs = '\n'.join(s.get('run', '') for s in jobs['sdk-packages']['steps'])
    require(jobs['sdk-packages']['needs'] == 'resolve' and 'if' not in jobs['sdk-packages']
            and 'scripts/release-sdk.py build' in sdk_runs
            and '--source "$RUNNER_TEMP/resolved-source/SOURCE.json"' in sdk_runs
            and '--expected-source-sha256 "$SOURCE_SHA256"' in sdk_runs,
            'SDK packages must build/test from the same immutable source on candidates and releases')
    for name, artifact_source in (('attest', 'sdk-packages'), ('draft', 'attest')):
        job=jobs[name]
        output='artifact-id' if name == 'attest' else 'sdk-artifact-id'
        digest='artifact-sha256' if name == 'attest' else 'sdk-artifact-sha256'
        require(job['env']['SDK_ARTIFACT_ID'] == '${{ needs.'+artifact_source+'.outputs.'+output+' }}'
                and job['env']['SDK_ARTIFACT_SHA256'] == '${{ needs.'+artifact_source+'.outputs.'+digest+' }}'
                and job['env']['SDK_MANIFEST_SHA256'] == '${{ needs.sdk-packages.outputs.manifest-sha256 }}',
                'SDK artifact and manifest identities must follow the exact packaging/signing outputs')
        runs='\n'.join(s.get('run','') for s in job['steps'])
        require('scripts/release-sdk.py fetch' in runs and '--artifact-id "$SDK_ARTIFACT_ID"' in runs
                and '--expected-artifact-sha256 "$SDK_ARTIFACT_SHA256"' in runs,
                'SDK consumers must fetch only the sealed artifact identity')
        verification=[line for line in runs.splitlines() if 'scripts/release-sdk.py verify-signed ' in line]
        require(len(verification)==1 and '--expected-manifest-sha256 "$SDK_MANIFEST_SHA256"' in verification[0]
                and '--run-id "$GITHUB_RUN_ID"' in verification[0] and '--run-attempt "$GITHUB_RUN_ATTEMPT"' in verification[0],
                'SDK signatures must match the current source, manifest, run and attempt')
        expected_name='sdk-bundle' if name == 'attest' else 'sdk-attested'
        require('--artifact-name "'+expected_name+'-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"' in runs
                and ('fetch --signed' in runs if name == 'draft' else 'scripts/release-sdk.py subjects' in runs),
                'SDK signing/publication must use the correct attempt-scoped package stage')
    native_runs = '\n'.join(step.get('run', '') for step in jobs['native']['steps'])
    require('go build' in native_runs and './scripts/testdata/release-process-helper.go' in native_runs
            and './cmd/cutmy' not in native_runs and './...' not in native_runs,
            'Native archive jobs may build only the separate test helper')
    for workflow in (release, source_ci):
        for job in workflow['jobs'].values():
            for step in job.get('steps', []):
                if step.get('uses', '').startswith('actions/upload-artifact@'):
                    name = step['with']['name']
                    require(RUN in name and ATTEMPT in name, 'Evidence uploads must be run/attempt scoped')
    for step in gate['steps']:
        if step.get('uses', '').startswith('actions/download-artifact@'):
            selector = step['with'].get('name', step['with'].get('pattern'))
            require(selector is None or (RUN in selector and ATTEMPT in selector),
                    'Aggregate downloads must select only the current attempt')


class WorkflowControls(unittest.TestCase):
    def setUp(self):
        self.release = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_bytes())
        self.source_ci = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_bytes())

    def test_current_source_archive_and_permission_graph(self):
        audit(self.release, self.source_ci)

    def test_duplicate_or_missing_pr_source_checks_are_rejected(self):
        for mutation in ('duplicate', 'missing_caller', 'wrong_callee'):
            release, source = deepcopy(self.release), deepcopy(self.source_ci)
            source_events = source.get('on', source.get(True))
            release_events = release.get('on', release.get(True))
            if mutation == 'duplicate': source_events['pull_request'] = None
            elif mutation == 'missing_caller': release_events.pop('pull_request')
            else: release['jobs']['checks']['uses'] = './.github/workflows/partial.yml'
            with self.assertRaises(ValueError): audit(release, source)

    def test_reduced_source_coverage_is_rejected(self):
        for mutation in ('missing_sdk_http', 'missing_platform', 'fail_fast', 'skipped_matrix'):
            source = deepcopy(self.source_ci)
            if mutation == 'missing_sdk_http': source['jobs'].pop('sdk-http')
            elif mutation == 'missing_platform': source['jobs']['native']['strategy']['matrix']['runner'].pop()
            elif mutation == 'fail_fast': source['jobs']['sdk']['strategy']['fail-fast'] = True
            else: source['jobs']['native']['if'] = 'false'
            with self.assertRaises(ValueError): audit(self.release, source)

    def test_tag_or_manual_cancellation_and_shared_caller_group_are_rejected(self):
        for mutation in ('tag', 'callee_group'):
            release, source = deepcopy(self.release), deepcopy(self.source_ci)
            if mutation == 'tag': release['concurrency']['cancel-in-progress'] = True
            else: source['concurrency']['group'] = release['concurrency']['group']
            with self.assertRaises(ValueError): audit(release, source)

    def test_missing_source_native_or_recovery_prerequisite(self):
        for job in ('checks', 'native', 'archive-recovery'):
            with self.subTest(job=job):
                workflow = deepcopy(self.release)
                workflow['jobs']['native-gate']['needs'].remove(job)
                with self.assertRaises(ValueError):
                    audit(workflow, self.source_ci)

    def test_mutable_consumer_or_source_check_ref(self):
        for job in ('checks', 'packages', 'native', 'archive-recovery', 'draft'):
            with self.subTest(job=job):
                workflow = deepcopy(self.release)
                if job == 'checks':
                    workflow['jobs'][job]['with']['ref'] = '${{ github.ref }}'
                else:
                    workflow['jobs'][job]['steps'][0]['with']['ref'] = '${{ github.ref }}'
                with self.assertRaises(ValueError):
                    audit(workflow, self.source_ci)

    def test_pr_publication_or_writable_builder(self):
        for mutation in ('publisher', 'builder'):
            with self.subTest(mutation=mutation):
                workflow = deepcopy(self.release)
                if mutation == 'publisher':
                    workflow['jobs']['draft']['if'] = "github.event_name == 'pull_request'"
                else:
                    workflow['jobs']['packages']['permissions'] = {'contents': 'write'}
                with self.assertRaises(ValueError):
                    audit(workflow, self.source_ci)

    def test_source_rebuild_in_native_job_is_rejected(self):
        workflow = deepcopy(self.release)
        workflow['jobs']['native']['steps'].append({'run': 'go build -o replacement ./cmd/cutmy'})
        with self.assertRaises(ValueError):
            audit(workflow, self.source_ci)

    def test_stale_upload_or_aggregate_selector_is_rejected(self):
        for mutation in ('upload', 'download', 'source_ci'):
            with self.subTest(mutation=mutation):
                workflow, source_ci = deepcopy(self.release), deepcopy(self.source_ci)
                job = workflow['jobs']['native'] if mutation == 'upload' else workflow['jobs']['native-gate']
                if mutation == 'source_ci':
                    job = source_ci['jobs']['native']
                action = 'actions/download-artifact@' if mutation == 'download' else 'actions/upload-artifact@'
                step = next(step for step in job['steps'] if step.get('uses', '').startswith(action)
                            and ('name' in step.get('with', {}) or 'pattern' in step.get('with', {})))
                step['with']['pattern' if 'pattern' in step['with'] else 'name'] = 'old-attempt'
                with self.assertRaises(ValueError):
                    audit(workflow, source_ci)

    def test_candidate_signing_permission_or_missing_trust_gate(self):
        mutations = [('trust', 'permissions', {'id-token': 'write'}),
                     ('draft', 'permissions', {'contents': 'write', 'id-token': 'write'}),
                     ('attest', 'if', "github.event_name == 'pull_request'"),
                     ('draft', 'needs', ['resolve', 'packages', 'native-gate'])]
        for job, key, value in mutations:
            workflow = deepcopy(self.release); workflow['jobs'][job][key] = value
            with self.assertRaises(ValueError): audit(workflow, self.source_ci)

    def test_unpinned_auto_discovered_or_wrong_sbom_signing_subjects(self):
        for mutation in ('unpinned', 'automatic', 'wrong_sbom'):
            workflow = deepcopy(self.release)
            signing = [s for s in workflow['jobs']['attest']['steps'] if s.get('uses', '').startswith('actions/attest@')]
            if mutation == 'unpinned': signing[0]['uses'] = 'actions/attest@v4'
            elif mutation == 'automatic': signing[0]['with'].pop('subject-checksums')
            else: signing[1]['with']['sbom-path'] = '/unrelated.json'
            with self.assertRaises(ValueError): audit(workflow, self.source_ci)

    def test_sdk_packages_signatures_and_run_binding_are_mandatory(self):
        for mutation in ('missing_draft_gate','missing_signing_gate','mutable_source','skip_candidate',
                         'wrong_build_source','missing_sdk_signing','wrong_subjects','wrong_artifact',
                         'wrong_manifest','wrong_attempt_name','missing_signed_fetch','missing_run',
                         'missing_attempt','missing_manifest_verification','missing_published_sdk'):
            with self.subTest(mutation=mutation):
                workflow=deepcopy(self.release);jobs=workflow['jobs']
                if mutation=='missing_draft_gate':jobs['draft']['needs'].remove('sdk-packages')
                elif mutation=='missing_signing_gate':jobs['attest']['needs'].remove('sdk-packages')
                elif mutation=='mutable_source':jobs['sdk-packages']['steps'][0]['with']['ref']='main'
                elif mutation=='skip_candidate':jobs['sdk-packages']['if']="github.event_name == 'push'"
                elif mutation=='wrong_build_source':
                    step=next(x for x in jobs['sdk-packages']['steps'] if 'scripts/release-sdk.py build' in x.get('run',''));step['run']=step['run'].replace('--expected-source-sha256 "$SOURCE_SHA256"','--expected-source-sha256 stale')
                elif mutation in ('missing_sdk_signing','wrong_subjects'):
                    signing=next(x for x in jobs['attest']['steps'] if x.get('id')=='sdk-provenance')
                    if mutation=='missing_sdk_signing':jobs['attest']['steps'].remove(signing)
                    else:signing['with']['subject-checksums']='/unrelated.sha256'
                elif mutation=='wrong_artifact':jobs['draft']['env']['SDK_ARTIFACT_ID']='${{ needs.sdk-packages.outputs.artifact-id }}'
                elif mutation=='wrong_manifest':jobs['draft']['env']['SDK_MANIFEST_SHA256']='stale'
                else:
                    for step in jobs['draft']['steps']:
                        value=step.get('run','')
                        if mutation=='wrong_attempt_name':value=value.replace('sdk-attested-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT','sdk-attested-old')
                        elif mutation=='missing_signed_fetch':value=value.replace('fetch --signed','fetch')
                        elif mutation=='missing_run' and 'scripts/release-sdk.py verify-signed ' in value:value=value.replace('--run-id "$GITHUB_RUN_ID"','')
                        elif mutation=='missing_attempt' and 'scripts/release-sdk.py verify-signed ' in value:value=value.replace('--run-attempt "$GITHUB_RUN_ATTEMPT"','')
                        elif mutation=='missing_manifest_verification' and 'scripts/release-sdk.py verify-signed ' in value:value=value.replace('--expected-manifest-sha256 "$SDK_MANIFEST_SHA256"','')
                        elif mutation=='missing_published_sdk':value=value.replace('bundle+sidecars+sdk','bundle+sidecars')
                        step['run']=value
                with self.assertRaises(ValueError):audit(workflow,self.source_ci)


if __name__ == '__main__':
    unittest.main()
