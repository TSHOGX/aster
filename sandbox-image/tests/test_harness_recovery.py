import hashlib
import io
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'files/usr/local/lib'))
from harness_recovery import RecoveryJournal
from harness_stream import Decoder, Journal, JournalGap


def envelope(number, line='historical'):
    return {'message_id': str(number), 'session_id': 'session', 'generation_id': 'old',
            'type': 'emit_output', 'turn_id': 4,
            'payload': {'output_sequence': number, 'payload': {'line': line}}}


class RecoveryTests(unittest.TestCase):
    def test_refuses_live_producer_and_preserves_original_wal_and_ack(self):
        with tempfile.TemporaryDirectory() as root:
            producer = Journal(root, 'old')
            producer.append(envelope(1))
            producer.append(envelope(2, 'x' * (256 << 10)))
            producer.acknowledge(1)
            with self.assertRaises(BlockingIOError):
                RecoveryJournal(root, 'old')
            # Simulate process death leaving committed WAL, without checkpoint.
            producer.producer.close()
            before = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (Path(root) / 'old').iterdir() if p.is_file()}
            reader = RecoveryJournal(root, 'old')
            try:
                output = io.BytesIO()
                reader.synchronize({'kind': 'sync', 'execution': 'old', 'command_id': 'sync', 'after': 1}, output)
                frames = Decoder().feed(output.getvalue())
                self.assertEqual([f['kind'] for f in frames], ['state', 'record'])
                self.assertEqual((frames[0]['after'], frames[0]['head']), (1, 2))
                self.assertEqual(frames[1]['envelope']['payload']['payload']['line'], 'x' * (256 << 10))
                with self.assertRaises(ValueError):
                    reader.synchronize({'kind': 'command', 'execution': 'old', 'command_id': 'claim'}, output)
                with self.assertRaises(ValueError):
                    reader.synchronize({'kind': 'ack', 'execution': 'old', 'after': 2}, output)
                with self.assertRaises(JournalGap):
                    list(reader.replay(0))
                with self.assertRaises(BlockingIOError):
                    Journal(root, 'old')
            finally:
                reader.close()
            after = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (Path(root) / 'old').iterdir() if p.is_file()}
            self.assertEqual(before, after)
            self.assertEqual(producer.positions(), (2, 1))
            producer.db.close()

    def test_missing_or_symlinked_partition_is_not_created_or_followed(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaises(FileNotFoundError):
                RecoveryJournal(root, 'missing')
            self.assertFalse((Path(root) / 'missing').exists())
            producer = Journal(root, 'old')
            producer.close()
            (Path(root) / 'alias').symlink_to(Path(root) / 'old', target_is_directory=True)
            with self.assertRaises(ValueError):
                RecoveryJournal(root, 'alias')
            for name in ('producer.lock', 'journal.db'):
                original = Path(root) / 'old' / name
                moved = original.with_suffix('.moved')
                original.rename(moved)
                original.symlink_to(moved)
                with self.assertRaises(ValueError):
                    RecoveryJournal(root, 'old')
                original.unlink()
                moved.rename(original)

    def test_incomplete_or_contradictory_records_fail_before_success(self):
        for fault in ('head', 'digest', 'identity'):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as root:
                producer = Journal(root, 'old')
                producer.append(envelope(1))
                producer.close()
                db = sqlite3.connect(Path(root) / 'old/journal.db')
                if fault == 'head':
                    db.execute('UPDATE positions SET head=2')
                elif fault == 'digest':
                    db.execute("UPDATE records SET digest='contradiction'")
                else:
                    payload, = db.execute('SELECT payload FROM records').fetchone()
                    payload = payload.replace(b'"execution":"old"', b'"execution":"other"')
                    db.execute('UPDATE records SET payload=?,digest=?', (payload, hashlib.sha256(payload).hexdigest()))
                db.commit()
                db.close()
                with self.assertRaises(JournalGap):
                    reader = RecoveryJournal(root, 'old')
                    try:
                        list(reader.replay(0))
                    finally:
                        reader.close()


if __name__ == '__main__':
    unittest.main()
