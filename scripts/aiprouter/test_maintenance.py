import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('maintenance', Path(__file__).with_name('maintenance.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class MaintenanceTest(unittest.TestCase):
    def test_restore_orders_invalidation_before_any_restart(self):
        with tempfile.NamedTemporaryFile() as archive, patch.object(m, 'run') as run:
            m.offline('restore', archive.name)
        calls = [call.args[0] for call in run.call_args_list]
        self.assertEqual(calls[0], ['stop', 'aiprouter'])
        self.assertIn('--single-transaction', calls[2])
        self.assertEqual(calls[-1][-1], 'restore-invalidate')
        self.assertFalse(any('start' in call or 'up' in call for call in calls))

    def test_failed_restore_never_starts_or_invalidates_partial_restore(self):
        with tempfile.NamedTemporaryFile() as archive, patch.object(m, 'run', side_effect=[None, None, subprocess.CalledProcessError(1, 'pg_restore')]) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                m.offline('restore', archive.name)
        self.assertEqual(run.call_count, 3)

    def test_backup_excludes_temp_and_does_not_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'backup.dump'
            with patch.object(m, 'run') as run:
                m.backup(path)
            self.assertIn('--exclude-schema=aiprouter_temp', run.call_args.args[0])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(ValueError):
                m.backup(path)

    def test_failed_backup_is_not_published(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(m, 'run', side_effect=subprocess.CalledProcessError(1, 'pg_dump')):
            path = Path(directory) / 'backup.dump'
            with self.assertRaises(subprocess.CalledProcessError):
                m.backup(path)
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_rotation_never_starts_server(self):
        with patch.object(m, 'run') as run:
            m.offline('rotate')
        self.assertEqual(run.call_args_list[0].args[0], ['stop', 'aiprouter'])
        self.assertEqual(run.call_args_list[-1].args[0][-1], 'reencrypt')


if __name__ == '__main__':
    unittest.main()
