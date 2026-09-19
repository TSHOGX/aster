import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

MODULE = Path(__file__).resolve().parents[1] / 'files/usr/local/lib/harness_stream.py'
spec = importlib.util.spec_from_file_location('harness_stream', MODULE)
stream = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stream)


def envelope(text='synthetic output'):
    return {'message_id': 'message', 'session_id': 'session', 'generation_id': 'execution',
            'type': 'emit_output', 'turn_id': 1, 'payload': {'output_sequence': 1, 'payload': {'line': text}}}


class StreamProtocolTests(unittest.TestCase):
    def test_split_and_partial_retry_resynchronization(self):
        frame = {'version': 1, 'kind': 'record', 'execution': 'execution', 'sequence': 1, 'envelope': envelope('newlines\nand separator\x1e')}
        encoded = stream.encode(frame)
        for split in range(len(encoded)):
            decoder = stream.Decoder()
            self.assertEqual(decoder.feed(encoded[:split]) + decoder.feed(encoded[split:]), [frame])
            decoder = stream.Decoder()
            self.assertEqual(decoder.feed(encoded[:split]) + decoder.feed(encoded), [frame])

    def test_durable_replay_exceeds_process_tail_and_survives_reopen(self):
        with tempfile.TemporaryDirectory() as root:
            journal = stream.Journal(root, 'execution')
            record = journal.append(envelope('x' * (256 << 10)))
            journal.close()
            reopened = stream.Journal(root, 'execution')
            try:
                self.assertEqual(reopened.replay(0), [record])
                reopened.acknowledge(1)
                self.assertEqual(reopened.replay(1), [])
                with self.assertRaises(stream.JournalGap):
                    reopened.replay(0)
                next_record = reopened.append(envelope('next'))
                self.assertEqual(next_record['sequence'], 2)
            finally:
                reopened.close()

    def test_capacity_failure_preserves_sequence_and_old_records(self):
        with tempfile.TemporaryDirectory() as root:
            journal = stream.Journal(root, 'execution', max_bytes=1024)
            try:
                first = journal.append(envelope())
                with self.assertRaises(stream.JournalFull):
                    journal.append(envelope('x' * 2048))
                self.assertEqual(journal.positions(), (1, 0))
                self.assertEqual(journal.replay(0), [first])
                with self.assertRaises(stream.JournalGap):
                    journal.acknowledge(2)
                journal.acknowledge(1)
                self.assertEqual(journal.append(envelope())['sequence'], 2)
            finally:
                journal.close()

    def test_commands_are_durable_and_contradictory_duplicates_fail(self):
        frame = {'version': 1, 'kind': 'command', 'execution': 'execution', 'command_id': 'control',
                 'envelope': {'type': 'turn_control', 'payload': {'command': 'steer', 'message': 'synthetic'}}}
        with tempfile.TemporaryDirectory() as root:
            journal = stream.Journal(root, 'execution')
            self.assertTrue(journal.receive_command(frame))
            journal.close()
            journal = stream.Journal(root, 'execution')
            try:
                self.assertFalse(journal.receive_command(frame))
                changed = json.loads(json.dumps(frame))
                changed['envelope']['payload']['message'] = 'contradiction'
                with self.assertRaises(ValueError):
                    journal.receive_command(changed)
            finally:
                journal.close()

    def test_one_producer_per_execution_and_bounded_frames(self):
        with tempfile.TemporaryDirectory() as root:
            journal = stream.Journal(root, 'execution')
            try:
                with self.assertRaises(BlockingIOError):
                    stream.Journal(root, 'execution')
                with self.assertRaises(ValueError):
                    stream.Decoder().feed(b'\x1e' + b'x' * (stream.MAX_FRAME_BYTES + 1))
                with self.assertRaises(ValueError):
                    journal.append(dict(envelope(), generation_id='other'))
            finally:
                journal.close()


if __name__ == '__main__':
    unittest.main()
