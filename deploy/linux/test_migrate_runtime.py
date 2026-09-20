"""Run with python3 -B deploy/linux/test_migrate_runtime.py."""
import pathlib
import runpy
import tempfile
import unittest

migration = runpy.run_path(str(pathlib.Path(__file__).with_name("migrate-runtime.py")))

class MigrationTests(unittest.TestCase):
    def test_hash_verified_repeat_and_conflict(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source, target = root / "source", root / "target"
            source.mkdir()
            (source / "attachment").write_bytes(b"original runtime bytes")
            self.assertEqual(migration["merge"](source, target), 1)
            self.assertEqual(migration["merge"](source, target), 1)
            self.assertEqual(migration["manifest"](source), migration["manifest"](target))
            (target / "attachment").write_bytes(b"conflicting destination")
            with self.assertRaises(RuntimeError):
                migration["merge"](source, target)
            self.assertEqual((source / "attachment").read_bytes(), b"original runtime bytes")
            self.assertEqual((target / "attachment").read_bytes(), b"conflicting destination")

    def test_symlink_not_followed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source = root / "source"
            source.mkdir()
            (root / "outside").write_bytes(b"outside")
            (source / "escape").symlink_to(root / "outside")
            with self.assertRaises(RuntimeError):
                migration["merge"](source, root / "target")

if __name__ == "__main__":
    unittest.main()
