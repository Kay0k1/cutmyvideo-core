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
    require(release['permissions'] == {'contents': 'read', 'actions': 'read'}, 'Builders require read-only permissions')
    require(jobs['checks']['with']['ref'] == COMMIT and jobs['checks']['needs'] == 'resolve',
            'Source CI must use the single immutable resolver output')
    for name in ('packages', 'native', 'archive-recovery', 'native-gate', 'trust', 'attest', 'draft'):
        checkouts = [step for step in jobs[name]['steps'] if step.get('uses', '').startswith('actions/checkout@')]
        require(len(checkouts) == 1 and checkouts[0]['with']['ref'] == COMMIT,
                'Every consumer must check out the immutable source')
    gate = jobs['native-gate']
    require(gate['if'] == 'always()' and set(gate['needs']) == {'resolve', 'checks', 'packages', 'native', 'archive-recovery'},
            'The required gate must observe every prerequisite even after failure')
    require(set(jobs['draft']['needs']) == {'resolve', 'packages', 'native-gate', 'trust', 'attest'}
            and jobs['draft']['if'] == "github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')",
            'Publication must be gated and restricted to tags')
    require(set(jobs['trust']['needs']) == {'resolve', 'packages', 'native-gate'}
            and set(jobs['attest']['needs']) == {'resolve', 'packages', 'native-gate', 'trust'}
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
    require(len(signing) == 2 and all(s['uses'] == 'actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6'
            and 'subject-checksums' in s['with'] and 'subject-path' not in s['with'] and s['with']['create-storage-record'] is False for s in signing),
            'Signing requires pinned official action and explicit verified subject hashes')
    require(signing[0]['with']['subject-checksums'].endswith('/release-trust/ATTESTATION-SUBJECTS.sha256')
            and signing[1]['with']['subject-checksums'].endswith('/release-bundle/SHA256SUMS')
            and signing[1]['with']['sbom-path'].endswith('/release-trust/SBOM.spdx.json'), 'Wrong provenance or SPDX signing inputs')
    for name in ('trust', 'attest', 'draft'):
        runs = '\n'.join(s.get('run', '') for s in jobs[name]['steps'])
        require('go build' not in runs and 'release.sh' not in runs, 'Trust/signing/publication must never rebuild')
        require('scripts/release-fetch.py' in runs and '--expected-artifact-sha256' in runs, 'Trust must re-fetch exactly sealed bytes')
    attest_runs = '\n'.join(s.get('run', '') for s in jobs['attest']['steps'])
    require('prepare-signing' in attest_runs and 'finalize' in attest_runs and 'git fetch --force --no-tags' in attest_runs,
            'Signatures require release-only source/trust checks and actual signature verification')
    draft_runs = '\n'.join(s.get('run', '') for s in jobs['draft']['steps'])
    require('fetch-attested' in draft_runs and 'verify-attested' in draft_runs and '/release-bundle/*' not in draft_runs
            and 'bundle+sidecars' in draft_runs, 'Publisher must reverify signatures and upload an exact asset list')
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


if __name__ == '__main__':
    unittest.main()
