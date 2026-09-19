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
    def test_directory_inventory_pages_reject_symlink_traversal(self):
        with tempfile.TemporaryDirectory() as root:
            descriptor = files.open_root(root)
            try:
                nested = Path(root) / 'nested'
                nested.mkdir()
                for number in range(40):
                    (nested / f'file-{number:02d}').write_text('content')
                (Path(root) / 'alias').symlink_to(nested, target_is_directory=True)
                first = files.list_directory(descriptor, 'nested')
                second = files.list_directory(descriptor, 'nested', first['next'])
                self.assertEqual(len(first['entries']), 32)
                self.assertEqual(len(second['entries']), 8)
                self.assertEqual(second['next'], '')
                self.assertEqual(files.list_directory(descriptor, '')['entries'][0]['type'], 'symlink')
                with self.assertRaises(ValueError):
                    files.list_directory(descriptor, 'alias')
            finally:
                os.close(descriptor)

    def test_replacement_cleanup_keeps_other_operations_and_target(self):
        with tempfile.TemporaryDirectory() as root:
            descriptor = files.open_root(root)
            try:
                for name in ('target', '.harness-upload-current-partial', '.harness-upload-other-partial'):
                    (Path(root) / name).write_text(name)
                files.cleanup_replacement(descriptor, 'target', 'current')
                self.assertFalse((Path(root) / '.harness-upload-current-partial').exists())
                self.assertTrue((Path(root) / '.harness-upload-other-partial').exists())
                self.assertEqual((Path(root) / 'target').read_text(), 'target')
            finally:
                os.close(descriptor)

    def test_native_restore_preserves_interrupted_bytes_and_retries(self):
        with tempfile.TemporaryDirectory() as root:
            descriptor = files.open_root(root)
            try:
                original = b'{"type":"session","id":"native"}\n{"message":"completed"}\n'
                selected = Path(root) / 'native.jsonl'
                selected.write_bytes(original)
                snapshot = '.harness-completed/state.jsonl'
                saved = files.snapshot_native(descriptor, 'native.jsonl', snapshot, 'native')
                interrupted = original + b'{"message":"interrupted"}\n'
                selected.write_bytes(interrupted)
                for _ in range(2):
                    files.restore_native(descriptor, 'native.jsonl', snapshot, 'native', len(original), saved['sha256'], 'operation')
                    self.assertEqual(selected.read_bytes(), original)
                    self.assertEqual((Path(root) / '.harness-completed/interrupted-operation.jsonl').read_bytes(), interrupted)
                selected.write_bytes(interrupted)
                (Path(root) / snapshot).chmod(0o600)
                (Path(root) / snapshot).write_bytes(original.replace(b'completed', b'tampered!'))
                with self.assertRaises(ValueError):
                    files.restore_native(descriptor, 'native.jsonl', snapshot, 'native', len(original), saved['sha256'], 'operation')
                self.assertEqual(selected.read_bytes(), interrupted)
            finally:
                os.close(descriptor)

    def test_completed_native_snapshot_survives_source_rewrite(self):
        with tempfile.TemporaryDirectory() as root:
            descriptor = files.open_root(root)
            try:
                body = b'{"type":"session","id":"native"}\n{"message":"completed"}\n'
                source = Path(root) / 'native.jsonl'
                source.write_bytes(body)
                target = '.harness-completed/state.jsonl'
                result = files.snapshot_native(descriptor, 'native.jsonl', target, 'native')
                self.assertEqual(result['sha256'], hashlib.sha256(body).hexdigest())
                self.assertEqual(files.snapshot_native(descriptor, 'native.jsonl', target, 'native'), result)
                self.assertNotEqual(source.stat().st_ino, (Path(root) / target).stat().st_ino)
                source.write_bytes(b'{"type":"session","id":"native"}\n{"message":"interrupted"}\n')
                self.assertEqual((Path(root) / target).read_bytes(), body)
                with self.assertRaises(ValueError):
                    files.snapshot_native(descriptor, 'native.jsonl', target, 'native')
                with self.assertRaises(ValueError):
                    files.snapshot_native(descriptor, 'native.jsonl', '.harness-completed/other.jsonl', 'foreign-id')
            finally:
                os.close(descriptor)

    def test_snapshot_publish_failure_retries_without_replacement(self):
        with tempfile.TemporaryDirectory() as root:
            descriptor = files.open_root(root)
            try:
                body = b'{"type":"session","id":"native"}\n'
                (Path(root) / 'native.jsonl').write_bytes(body)
                def fail(stage):
                    if stage == 'snapshot-published':
                        raise OSError('injected parent sync interruption')
                target = '.harness-completed/state.jsonl'
                with self.assertRaises(OSError):
                    files.snapshot_native(descriptor, 'native.jsonl', target, 'native', hook=fail)
                inode = (Path(root) / target).stat().st_ino
                files.snapshot_native(descriptor, 'native.jsonl', target, 'native')
                self.assertEqual((Path(root) / target).stat().st_ino, inode)
                (Path(root) / target).unlink()
                (Path(root) / target).symlink_to(Path(root) / 'native.jsonl')
                with self.assertRaises(ValueError):
                    files.snapshot_native(descriptor, 'native.jsonl', target, 'native')
            finally:
                os.close(descriptor)

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

class NativeIdentityBarrierTests(unittest.TestCase):
    def test_native_sync_checks_session_header_identity(self):
        with tempfile.TemporaryDirectory() as root:
            (Path(root) / 'native.jsonl').write_text('{"type":"session","id":"native-id"}\n')
            descriptor = files.open_root(root)
            try:
                self.assertEqual(files.file_digest(descriptor, 'native.jsonl', sync=True, native_id='native-id')['size'], 36)
                with self.assertRaises(ValueError):
                    files.file_digest(descriptor, 'native.jsonl', sync=True, native_id='other-id')
            finally:
                os.close(descriptor)


if __name__ == '__main__':
    unittest.main()
