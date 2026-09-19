import hashlib
import importlib.util
import io
import os
from pathlib import Path
import tempfile
import unittest

MODULE = Path(__file__).resolve().parents[1] / 'files/usr/local/lib/harness_files.py'
spec = importlib.util.spec_from_file_location('harness_files', MODULE)
files = importlib.util.module_from_spec(spec)
spec.loader.exec_module(files)


class GuestFilesystemTests(unittest.TestCase):
    def test_atomic_replace_range_read_and_native_sync(self):
        with tempfile.TemporaryDirectory() as root:
            descriptor = files.open_root(root)
            try:
                payload = b'synthetic file content'
                digest = hashlib.sha256(payload).hexdigest()
                result = files.replace(descriptor, 'nested/file', io.BytesIO(payload), len(payload), digest, 'operation')
                self.assertEqual(result['sha256'], digest)
                output = io.BytesIO()
                files.read_range(descriptor, 'nested/file', output, offset=3, length=7)
                self.assertEqual(output.getvalue(), payload[3:10])
                self.assertEqual(files.native_state_barrier(Path(root) / 'nested/file', root), result)
            finally:
                os.close(descriptor)

    def test_parent_symlink_swap_cannot_redirect_write(self):
        with tempfile.TemporaryDirectory() as root, tempfile.TemporaryDirectory() as outside:
            (Path(root) / 'parent').mkdir()
            (Path(outside) / 'file').write_bytes(b'outside')
            descriptor = files.open_root(root)
            def swap(stage):
                if stage == 'parent-open':
                    (Path(root) / 'parent').rename(Path(root) / 'pinned')
                    (Path(root) / 'parent').symlink_to(outside)
            try:
                files.replace(descriptor, 'parent/file', io.BytesIO(b'new'), 3, hashlib.sha256(b'new').hexdigest(), 'operation', swap)
                self.assertEqual((Path(outside) / 'file').read_bytes(), b'outside')
                self.assertEqual((Path(root) / 'pinned/file').read_bytes(), b'new')
            finally:
                os.close(descriptor)

    def test_failed_validation_preserves_target_and_after_rename_is_uncertain(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'file'
            path.write_bytes(b'old')
            descriptor = files.open_root(root)
            try:
                with self.assertRaises(ValueError):
                    files.replace(descriptor, 'file', io.BytesIO(b'wrong'), 5, hashlib.sha256(b'other').hexdigest(), 'operation')
                self.assertEqual(path.read_bytes(), b'old')
                self.assertEqual([entry.name for entry in Path(root).iterdir()], ['file'])
                def fail(stage):
                    if stage == 'renamed':
                        raise OSError('injected parent sync failure')
                with self.assertRaises(files.UncertainMutation):
                    files.replace(descriptor, 'file', io.BytesIO(b'new'), 3, hashlib.sha256(b'new').hexdigest(), 'operation', fail)
                self.assertEqual(path.read_bytes(), b'new')
            finally:
                os.close(descriptor)

    def test_rejects_symlinks_special_files_and_traversal(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)
            (path / 'regular').write_bytes(b'synthetic')
            (path / 'link').symlink_to(path / 'regular')
            os.mkfifo(path / 'fifo')
            descriptor = files.open_root(root)
            try:
                (path / 'parent-link').symlink_to(path)
                with self.assertRaises((ValueError, OSError)):
                    files.open_root(path / 'parent-link')
                for name in ('../escape', 'link', 'fifo'):
                    with self.subTest(name=name), self.assertRaises((ValueError, OSError)):
                        files.read_range(descriptor, name, io.BytesIO())
            finally:
                os.close(descriptor)


if __name__ == '__main__':
    unittest.main()
