"""Controls for the CI race shards: together they run every internal/app test exactly once."""

import importlib.util
from pathlib import Path
import re
import unittest

SCRIPT = Path(__file__).with_name('race-shard.py')
spec = importlib.util.spec_from_file_location('race_shard', SCRIPT)
race_shard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(race_shard)
ROOT = Path(__file__).resolve().parents[1]


class RaceShardControls(unittest.TestCase):
    def test_app_shards_partition_every_test(self):
        names = [f'Test{index:03d}' for index in range(101)] + ['ExampleX', 'FuzzY', 'Test050']
        for count in (1, 2, 3, 4):
            with self.subTest(count=count):
                shards = [race_shard.shard_names(names, shard, count) for shard in range(1, count + 1)]
                flat = [name for shard in shards for name in shard]
                self.assertEqual(sorted(flat), sorted(set(names)))
                self.assertEqual(len(flat), len(set(flat)))
                self.assertLessEqual(max(map(len, shards)) - min(map(len, shards)), 1)

    def test_out_of_range_or_empty_shards_fail(self):
        for shard in (0, 4):
            with self.subTest(shard=shard), self.assertRaises(ValueError):
                race_shard.shard_names(['TestA'], shard, 3)
        with self.assertRaises(ValueError):
            race_shard.selector([])

    def test_selector_matches_only_exact_top_level_names(self):
        pattern = race_shard.selector(['TestA', 'TestA_B'])
        self.assertIsNotNone(re.fullmatch(pattern, 'TestA'))
        self.assertIsNotNone(re.fullmatch(pattern, 'TestA_B'))
        self.assertIsNone(re.fullmatch(pattern, 'TestAB'))
        self.assertIsNone(re.fullmatch(pattern, 'TestA_BC'))

    def test_workflow_runs_every_shard(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text(encoding='utf-8')
        race = workflow.split('\n  race:\n', 1)[1].split('\n  vulnerabilities:\n', 1)[0]
        shards = re.search(r'shard: \[([0-9, ]+)\]', race).group(1)
        app_shards = int(re.search(r'--app-shards (\d+)', race).group(1))
        self.assertEqual([int(value) for value in shards.split(',')], list(range(app_shards + 1)))
        self.assertIn('fail-fast: false', race)
        self.assertNotIn('\n    if:', race)


if __name__ == '__main__':
    unittest.main()
