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

class StdioShellIntegrationTest(unittest.TestCase):
    def test_real_shell_turn_uses_journal_handshake_and_framed_output(self):
        import os
        import select
        import subprocess
        import sys
        import time
        script = MODULE.parents[0].parent / 'bin/harness-bridge-client'
        with tempfile.TemporaryDirectory() as root:
            # Execute the production guest scripts from the checkout; only the
            # installed binary path differs outside a built guest filesystem.
            launcher = """import runpy,subprocess,sys
script=sys.argv.pop(1)
original=subprocess.Popen
def launch(args,*a,**kw):
    if args[0]=='/usr/local/bin/harness-shell-agent':
        args=[sys.executable,str(__import__('pathlib').Path(script).with_name('harness-shell-agent'))]+args[1:]
    return original(args,*a,**kw)
subprocess.Popen=launch
sys.argv[0]=script
runpy.run_path(script,run_name='__main__')
"""
            process = subprocess.Popen([sys.executable, '-c', launcher, str(script), '--transport', 'stdio', '--bridge-dir', root,
                                        '--session-id', 'session', '--generation-id', 'execution', '--driver-id', 'sh',
                                        '--timeout', '5', 'claim-loop', '--max-turns', '1'],
                                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd=root)
            decoder = stream.Decoder()
            records = []
            synchronized = False
            completed = False
            def send(frame):
                process.stdin.write(stream.encode(dict(version=1, execution='execution', **frame)))
                process.stdin.flush()
            def reply(envelope, kind, payload):
                response = dict(message_id='response-' + envelope['message_id'], request_id=envelope['request_id'],
                                session_id='session', generation_id='execution', type=kind, payload=payload)
                send(dict(kind='command', command_id=response['message_id'], envelope=response))
            try:
                send(dict(kind='sync', command_id='attachment', after=0))
                deadline = time.monotonic() + 12
                while time.monotonic() < deadline and not completed:
                    ready, _, _ = select.select([process.stdout], [], [], 0.2)
                    if not ready:
                        if process.poll() is not None:
                            break
                        continue
                    chunk = os.read(process.stdout.fileno(), 65536)
                    if not chunk:
                        break
                    for frame in decoder.feed(chunk):
                        if frame['kind'] == 'state':
                            synchronized = frame['command_id'] == 'attachment'
                            continue
                        if frame['kind'] != 'record':
                            continue
                        self.assertTrue(synchronized)
                        self.assertEqual(frame['sequence'], len(records) + 1)
                        envelope = frame['envelope']
                        records.append(envelope)
                        kind = envelope['type']
                        if kind == 'hello':
                            reply(envelope, 'hello_ack', {'last_output_sequence_by_turn': {}, 'server_time': '2026-09-19T00:00:00Z'})
                        elif kind == 'probe_network':
                            reply(envelope, 'no_work', {'status': 'probe_ok'})
                        elif kind == 'claim_next_turn':
                            reply(envelope, 'grant', {'turn_id': 1, 'sequence': 1, 'attempt': 1, 'turn_input_schema': 'RunTurn',
                                                     'input': {'content': "printf 'streamed-shell-output\\n'"}})
                        elif kind == 'ack_turn_completed':
                            self.assertEqual(envelope['payload']['status'], 'completed', envelope)
                            output_count = sum(record['type'] == 'emit_output' for record in records)
                            self.assertEqual(envelope['payload']['final_output_sequence'], output_count)
                            completed = True
                        if not completed:
                            send(dict(kind='ack', after=frame['sequence']))
                self.assertTrue(completed, process.stderr.read().decode() if process.poll() is not None else 'turn timed out')
                self.assertEqual(process.wait(timeout=5), 0, process.stderr.read().decode())
                output = json.dumps([record for record in records if record['type'] == 'emit_output'])
                self.assertIn('streamed-shell-output', output)
                journal = stream.Journal(root, 'execution')
                try:
                    head, acknowledged = journal.positions()
                    self.assertEqual(head, len(records))
                    self.assertGreater(head, acknowledged)
                    self.assertEqual(journal.replay(acknowledged)[-1]['envelope']['type'], 'ack_turn_completed')
                finally:
                    journal.close()
            finally:
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=5)
                process.stdin.close()
                process.stdout.close()
                process.stderr.close()

class StdioControlContractTest(unittest.TestCase):
    def test_sync_serializes_replay_and_duplicate_controls_apply_once(self):
        import io
        import threading
        with tempfile.TemporaryDirectory() as root:
            journal = stream.Journal(root, 'execution')
            try:
                journal.append(envelope('x' * (256 << 10)))
                channel = stream.StdioChannel.__new__(stream.StdioChannel)
                channel.journal = journal
                channel.destination = io.BytesIO()
                channel.protocol = {'protocol_version': 2, 'driver_id': 'sh', 'turn_input_schema': 'RunTurn'}
                channel.output_lock = threading.RLock()
                channel.condition = threading.Condition()
                channel.commands = {}
                channel.controls = []
                channel.closed_through = 0
                channel.synchronized = False
                channel._receive({'version': 1, 'kind': 'sync', 'execution': 'execution', 'command_id': 'sync-1', 'after': 0})
                frames = stream.Decoder().feed(channel.destination.getvalue())
                self.assertEqual([frame['kind'] for frame in frames], ['state', 'record'])
                self.assertEqual(frames[0]['command_id'], 'sync-1')
                self.assertEqual(frames[0]['payload'], channel.protocol)
                self.assertEqual(len(frames[1]['envelope']['payload']['payload']['line']), 256 << 10)
                command = {'version': 1, 'kind': 'command', 'execution': 'execution', 'command_id': 'steer-1',
                           'envelope': {'type': 'turn_control', 'turn_id': 3, 'payload': {'command': 'steer', 'message': 'synthetic'}}}
                channel._receive(command)
                channel._receive(command)
                self.assertEqual(len(channel.take_controls(3)), 1)
                self.assertEqual(channel.take_controls(3), [])
                channel.finish_controls(3)
                late = {'version': 1, 'kind': 'command', 'execution': 'execution', 'command_id': 'late-steer',
                        'envelope': {'type': 'turn_control', 'session_id': 'session', 'generation_id': 'execution',
                                     'turn_id': 3, 'payload': {'command': 'steer', 'message': 'too late'}}}
                channel._receive(late)
                channel._receive(late)
                self.assertEqual(channel.take_controls(3), [])
                results = [record for record in journal.replay(0) if record['envelope']['type'] == 'turn_control_result']
                self.assertEqual(len(results), 1)
                self.assertEqual(results[0]['envelope']['payload'], {'command_id': 'late-steer', 'status': 'rejected'})
            finally:
                journal.close()


if __name__ == '__main__':
    unittest.main()
