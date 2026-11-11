"""Controls for the archive-workflow scope: releases keep everything, PRs skip only what they cannot affect."""

import importlib.util
from pathlib import Path
import unittest

SCRIPT = Path(__file__).with_name('release-scope.py')
spec = importlib.util.spec_from_file_location('release_scope', SCRIPT)
release_scope = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release_scope)
scope = release_scope.scope
FULL = {'checks': 'full', 'archives': True, 'vulnerabilities': 'blocking'}


class ReleaseScopeControls(unittest.TestCase):
    def test_tags_nightly_manual_and_unknown_events_run_everything(self):
        for event in ('push', 'schedule', 'workflow_dispatch', 'merge_group', 'unknown'):
            with self.subTest(event=event):
                self.assertEqual(scope(event, None), FULL)
                self.assertEqual(scope(event, ['README.md']), FULL)

    def test_pull_request_without_a_provider_merge_runs_everything(self):
        self.assertEqual(scope('pull_request', None), FULL)

    def test_empty_pull_request_diff_keeps_archives_and_blocking_vulnerabilities(self):
        self.assertEqual(scope('pull_request', []), {'checks': 'pr', 'archives': True, 'vulnerabilities': 'blocking'})

    def test_documentation_only_pull_request_checks_documentation(self):
        for paths in (['README.md'], ['docs/self-hosting.md', 'CHANGELOG.md'], ['.github/ISSUE_TEMPLATE/bug.md']):
            with self.subTest(paths=paths):
                self.assertEqual(scope('pull_request', paths),
                                 {'checks': 'docs', 'archives': False, 'vulnerabilities': 'report'})

    def test_documentation_that_ships_or_is_code_is_not_documentation_only(self):
        for path in ('THIRD_PARTY_NOTICES.md', 'docs/release-artifact.txt', 'sdk/python/README.md',
                     'docs/api.yaml', 'internal/app/README.md', 'api/openapi.yaml'):
            with self.subTest(path=path):
                self.assertNotEqual(scope('pull_request', ['README.md', path])['checks'], 'docs')

    def test_source_change_runs_pull_request_matrices_without_archives(self):
        self.assertEqual(scope('pull_request', ['internal/app/http.go', 'internal/app/http_test.go']),
                         {'checks': 'pr', 'archives': False, 'vulnerabilities': 'report'})

    def test_release_inputs_run_archives(self):
        for path in ('go.mod', 'go.sum', 'Makefile', 'cmd/cutmy/main.go', 'sdk/typescript/src/index.ts',
                     'sdk/python/README.md', '.github/workflows/ci.yml', '.github/workflows/self-hosting.yml',
                     'scripts/release.sh', 'scripts/release-gate.py', 'scripts/release_manifest.py',
                     'scripts/package-release.py', 'scripts/check-sdks.py', 'scripts/update-notices.py',
                     'scripts/recovery-acceptance.py', 'scripts/testdata/release-process-helper.go',
                     'THIRD_PARTY_NOTICES.md', 'LICENSE', 'docs/release-artifact.txt'):
            with self.subTest(path=path):
                self.assertTrue(scope('pull_request', ['internal/app/http.go', path])['archives'])

    def test_dependency_or_toolchain_change_blocks_on_vulnerabilities(self):
        for path in ('go.mod', 'go.sum'):
            with self.subTest(path=path):
                self.assertEqual(scope('pull_request', ['internal/app/http.go', path])['vulnerabilities'], 'blocking')
        self.assertEqual(scope('pull_request', ['internal/app/http.go'])['vulnerabilities'], 'report')


if __name__ == '__main__':
    unittest.main()
