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
    for name in ('packages', 'native', 'archive-recovery', 'native-gate', 'draft'):
        checkouts = [step for step in jobs[name]['steps'] if step.get('uses', '').startswith('actions/checkout@')]
        require(len(checkouts) == 1 and checkouts[0]['with']['ref'] == COMMIT,
                'Every consumer must check out the immutable source')
    gate = jobs['native-gate']
    require(gate['if'] == 'always()' and set(gate['needs']) == {'resolve', 'checks', 'packages', 'native', 'archive-recovery'},
            'The required gate must observe every prerequisite even after failure')
    require(set(jobs['draft']['needs']) == {'resolve', 'packages', 'native-gate'}
            and jobs['draft']['if'] == "github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')",
            'Publication must be gated and restricted to tags')
    for name, job in jobs.items():
        require(job.get('permissions', {}).get('contents', 'read') == ('write' if name == 'draft' else 'read'),
                'Only the tag publisher may write repository contents')
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


if __name__ == '__main__':
    unittest.main()
