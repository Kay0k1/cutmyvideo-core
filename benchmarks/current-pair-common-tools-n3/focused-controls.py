#!/usr/bin/env python3
"""Exactly the two new pure controls; child-process and network audit denied."""
import importlib.util
import json
from pathlib import Path
import sys
import unittest


def deny_runtime(event, arguments):
    if event.startswith(('subprocess.', 'socket.', 'urllib.')) or event in (
            'os.system', 'os.fork', 'os.forkpty', 'os.posix_spawn', 'os.exec'):
        raise RuntimeError('Focused pure control denied process/network audit: ' + event)


sys.addaudithook(deny_runtime)
path = Path(__file__).resolve().with_name('media_capacity_test.py')
spec = importlib.util.spec_from_file_location('current_pair_pure_controls', path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
names = (
    'test_old_generator_with_distinct_common_runtime_is_retained_and_reported',
    'test_generation_sha_substitution_or_missing_identity_is_rejected',
)
suite = unittest.TestSuite(module.FixtureAndAudioControls(name) for name in names)
result = unittest.TextTestRunner(verbosity=2).run(suite)
print(json.dumps({'scope': 'exactly two new pure controls', 'tests': list(names),
                  'tests_run': result.testsRun, 'ok': result.wasSuccessful(),
                  'process_and_network_audit': 'denied'}))
raise SystemExit(0 if result.wasSuccessful() and result.testsRun == 2 else 1)
