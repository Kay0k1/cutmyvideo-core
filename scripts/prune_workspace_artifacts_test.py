import importlib.util
import tempfile
import time
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('prune', Path(__file__).with_name('prune-workspace-artifacts.py'))
prune = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prune)


class PruneTests(unittest.TestCase):
    def test_selects_only_large_generated_files_and_skips_symlinks_and_backups(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / 'reports'
            root.mkdir()
            for name in ['clip.mp4', 'summary.json', 'notes.md', 'small.mp4', 'backups/clip.mp4']:
                path = root / name
                path.parent.mkdir(exist_ok=True)
                path.write_bytes(b'x' * (1 if name in {'small.mp4', 'summary.json', 'notes.md'} else 1024 * 1024))
            (root / 'link.mp4').symlink_to(root / 'clip.mp4')
            selected = [path.relative_to(root).as_posix() for path, _ in prune.candidates(root, time.time() + 1)]
            self.assertEqual(selected, ['clip.mp4'])
            self.assertEqual(list(prune.candidates(root, 0)), [])
