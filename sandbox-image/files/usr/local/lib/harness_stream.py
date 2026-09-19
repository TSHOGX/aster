"""Framing and durable guest journal for the process bridge carrier."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import threading

VERSION = 1
MAX_FRAME_BYTES = 8 << 20
DEFAULT_JOURNAL_BYTES = 256 << 20
SEPARATOR = b"\x1e"


class JournalFull(RuntimeError):
    pass


class JournalGap(RuntimeError):
    pass


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def validate(frame):
    if not isinstance(frame, dict) or type(frame.get("version")) is not int or frame["version"] != VERSION:
        raise ValueError("unsupported bridge stream version")
    if not isinstance(frame.get("execution"), str) or not frame["execution"]:
        raise ValueError("bridge execution is required")
    for field in ("sequence", "after", "head"):
        value = frame.get(field, 0)
        if type(value) is not int or value < 0 or value >= 1 << 64:
            raise ValueError("invalid bridge stream position")
    kind = frame.get("kind")
    if kind == "record":
        if frame.get("sequence", 0) == 0 or not isinstance(frame.get("envelope"), dict):
            raise ValueError("journal record requires sequence and envelope")
    elif kind == "command":
        if not isinstance(frame.get("command_id"), str) or not frame["command_id"] or not isinstance(frame.get("envelope"), dict):
            raise ValueError("command requires identity and envelope")
    elif kind == "command_ack":
        if not isinstance(frame.get("command_id"), str) or not frame["command_id"]:
            raise ValueError("command acknowledgment requires identity")
    elif kind not in {"ack", "sync", "state", "error"}:
        raise ValueError("unsupported bridge stream frame")
    return frame


def encode(frame):
    raw = canonical(validate(frame))
    if len(raw) > MAX_FRAME_BYTES:
        raise ValueError("bridge stream frame exceeds byte limit")
    return SEPARATOR + raw + b"\n"


class Decoder:
    def __init__(self):
        self.buffer = bytearray()
        self.active = False

    def feed(self, chunk):
        frames = []
        offset = 0
        while offset < len(chunk):
            if not self.active:
                start = chunk.find(SEPARATOR, offset)
                if start < 0:
                    return frames
                self.active = True
                self.buffer.clear()
                offset = start + 1
            boundary = len(chunk)
            for marker in (SEPARATOR, b"\n"):
                index = chunk.find(marker, offset)
                if index >= 0:
                    boundary = min(boundary, index)
            if len(self.buffer) + boundary - offset > MAX_FRAME_BYTES:
                self.active = False
                self.buffer.clear()
                raise ValueError("bridge stream frame exceeds byte limit")
            self.buffer.extend(chunk[offset:boundary])
            offset = boundary
            if boundary == len(chunk):
                break
            marker = chunk[offset]
            offset += 1
            if marker == 0x1E:
                self.buffer.clear()
                continue
            raw = bytes(self.buffer)
            self.active = False
            self.buffer.clear()
            frames.append(validate(json.loads(raw)))
        return frames


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


class Journal:
    """One producer per execution; FULL-sync commits precede wire publication.

    Output acknowledgments collect only the committed prefix. Command receipts
    survive for the execution lifetime so a duplicate cannot reapply Stop/steer.
    Reopening never resets sequence positions, even after every record was ACKed.
    """
    def __init__(self, root, execution, max_bytes=DEFAULT_JOURNAL_BYTES):
        if not execution or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-" for c in execution):
            raise ValueError("safe execution identity required")
        if max_bytes <= 0:
            raise ValueError("positive journal capacity required")
        self.execution = execution
        self.max_bytes = max_bytes
        self.root = Path(root) / execution
        self.root.mkdir(parents=True, exist_ok=True)
        sync_directory(self.root.parent)
        self.lock = threading.RLock()
        self.producer = open(self.root / "producer.lock", "a+b")
        try:
            fcntl.flock(self.producer, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BaseException:
            self.producer.close()
            raise
        try:
            self.db = sqlite3.connect(self.root / "journal.db", check_same_thread=False)
            self.db.execute("PRAGMA journal_mode=WAL")
            self.db.execute("PRAGMA synchronous=FULL")
            self.db.executescript('''
                CREATE TABLE IF NOT EXISTS positions(singleton INTEGER PRIMARY KEY CHECK(singleton=1),head INTEGER NOT NULL,ack INTEGER NOT NULL,bytes INTEGER NOT NULL);
                INSERT OR IGNORE INTO positions VALUES(1,0,0,0);
                CREATE TABLE IF NOT EXISTS records(sequence INTEGER PRIMARY KEY,payload BLOB NOT NULL,digest TEXT NOT NULL);
                CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY,payload BLOB NOT NULL,digest TEXT NOT NULL);
            ''')
            self.db.commit()
            sync_directory(self.root)
        except BaseException:
            if hasattr(self, "db"):
                self.db.close()
            self.producer.close()
            raise

    def close(self):
        with self.lock:
            self.db.close()
            self.producer.close()

    def positions(self):
        with self.lock:
            head, ack, _ = self.db.execute("SELECT head,ack,bytes FROM positions WHERE singleton=1").fetchone()
            return head, ack

    def append(self, envelope):
        if envelope.get("generation_id") != self.execution:
            raise ValueError("journal envelope belongs to another execution")
        with self.lock, self.db:
            head, _, used = self.db.execute("SELECT head,ack,bytes FROM positions WHERE singleton=1").fetchone()
            frame = {"version": VERSION, "kind": "record", "execution": self.execution, "sequence": head + 1, "envelope": envelope}
            payload = encode(frame)
            if used + len(payload) > self.max_bytes:
                raise JournalFull("bridge journal capacity exhausted; output remains unacknowledged")
            digest = hashlib.sha256(payload).hexdigest()
            self.db.execute("INSERT INTO records VALUES(?,?,?)", (head + 1, payload, digest))
            self.db.execute("UPDATE positions SET head=?,bytes=bytes+? WHERE singleton=1", (head + 1, len(payload)))
        return frame

    def replay(self, after, limit=128):
        with self.lock:
            head, ack = self.positions()
            if type(after) is not int or after < ack or after > head:
                raise JournalGap("host journal position is outside the retained range")
            rows = self.db.execute("SELECT payload,digest FROM records WHERE sequence>? ORDER BY sequence LIMIT ?", (after, max(1, min(limit, 1024)))).fetchall()
            result = []
            for payload, digest in rows:
                if hashlib.sha256(payload).hexdigest() != digest:
                    raise JournalGap("retained bridge record digest mismatch")
                frames = Decoder().feed(payload)
                if len(frames) != 1:
                    raise JournalGap("retained bridge frame is incomplete")
                result.append(frames[0])
            return result

    def output_watermarks(self):
        with self.lock:
            rows = self.db.execute("SELECT payload FROM records ORDER BY sequence").fetchall()
            result = {}
            for (payload,) in rows:
                envelope = Decoder().feed(payload)[0]['envelope']
                if envelope.get('type') == 'emit_output':
                    turn = int(envelope['turn_id'])
                    sequence = int(envelope['payload']['output_sequence'])
                    result[turn] = max(sequence, result.get(turn, 0))
            return result

    def acknowledge(self, sequence):
        with self.lock, self.db:
            head, ack, _ = self.db.execute("SELECT head,ack,bytes FROM positions WHERE singleton=1").fetchone()
            if type(sequence) is not int or sequence < 0 or sequence > head:
                raise JournalGap("acknowledgment exceeds durable journal head")
            if sequence <= ack:
                return
            reclaimed = self.db.execute("SELECT coalesce(sum(length(payload)),0) FROM records WHERE sequence<=?", (sequence,)).fetchone()[0]
            self.db.execute("DELETE FROM records WHERE sequence<=?", (sequence,))
            self.db.execute("UPDATE positions SET ack=?,bytes=bytes-? WHERE singleton=1", (sequence, reclaimed))

    def receive_command(self, frame):
        validate(frame)
        if frame["kind"] != "command" or frame["execution"] != self.execution:
            raise ValueError("command belongs to another execution")
        payload = encode(frame)
        digest = hashlib.sha256(payload).hexdigest()
        with self.lock, self.db:
            existing = self.db.execute("SELECT digest FROM commands WHERE id=?", (frame["command_id"],)).fetchone()
            if existing:
                if existing[0] != digest:
                    raise ValueError("contradictory duplicate bridge command")
                return False
            used = self.db.execute("SELECT bytes FROM positions WHERE singleton=1").fetchone()[0]
            if used + len(payload) > self.max_bytes:
                raise JournalFull("bridge command journal capacity exhausted")
            self.db.execute("INSERT INTO commands VALUES(?,?,?)", (frame["command_id"], payload, digest))
            self.db.execute("UPDATE positions SET bytes=bytes+? WHERE singleton=1", (len(payload),))
        return True


class StdioChannel:
    """Single input reader and serialized output over a persisted journal.

    A sync response precedes replay under the same output lock. Records emitted
    while detached remain in the journal and are recovered on the next sync.
    """
    def __init__(self, journal, source, destination, protocol):
        self.journal = journal
        self.source = source
        self.destination = destination
        self.protocol = dict(protocol)
        self.output_lock = threading.RLock()
        self.condition = threading.Condition()
        self.error = None
        self.synchronized = False
        self.commands = {}
        self.controls = []
        self.decoder = Decoder()
        self.reader = threading.Thread(target=self._read, daemon=True)
        self.reader.start()

    def _write(self, frame):
        self.destination.write(encode(frame))
        self.destination.flush()

    def emit(self, envelope):
        with self.output_lock:
            frame = self.journal.append(envelope)
            if self.synchronized:
                self._write(frame)
            return frame

    def _receive(self, frame):
        if frame['execution'] != self.journal.execution:
            raise ValueError('host frame names another execution')
        if frame['kind'] == 'sync':
            with self.output_lock:
                after = frame.get('after', 0)
                head, acknowledged = self.journal.positions()
                self.journal.replay(after, 1)  # Validate even an empty window.
                self._write({'version': VERSION, 'kind': 'state', 'execution': self.journal.execution,
                             'command_id': frame.get('command_id', ''), 'after': acknowledged, 'head': head,
                             'payload': self.protocol})
                while after < head:
                    records = self.journal.replay(after)
                    if not records:
                        raise JournalGap('journal sequence is missing')
                    for record in records:
                        if record['sequence'] != after + 1:
                            raise JournalGap('journal sequence is missing')
                        self._write(record)
                        after = record['sequence']
                self.synchronized = True
        elif frame['kind'] == 'ack':
            self.journal.acknowledge(frame.get('after', 0))
        elif frame['kind'] == 'command':
            first = self.journal.receive_command(frame)
            envelope = frame['envelope']
            with self.condition:
                request_id = envelope.get('request_id')
                if request_id:
                    self.commands[request_id] = envelope
                if first and envelope.get('type') == 'turn_control':
                    self.controls.append(dict(envelope.get('payload') or {}, token=frame['command_id'], turn_id=envelope.get('turn_id')))
                self.condition.notify_all()
            with self.output_lock:
                self._write({'version': VERSION, 'kind': 'command_ack', 'execution': self.journal.execution,
                             'command_id': frame['command_id']})
        else:
            raise ValueError('unexpected host bridge frame')

    def _read(self):
        try:
            while True:
                # Read the descriptor directly: a daemon blocked while holding
                # BufferedReader's lock can abort CPython during process exit.
                chunk = os.read(self.source.fileno(), 65536)
                if not chunk:
                    raise EOFError('bridge process input closed')
                for frame in self.decoder.feed(chunk):
                    self._receive(frame)
        except BaseException as error:
            with self.condition:
                self.error = error
                self.condition.notify_all()

    def wait_response(self, request_id, timeout):
        import time
        deadline = None if timeout is None else time.monotonic() + timeout
        with self.condition:
            while request_id not in self.commands:
                if self.error:
                    raise RuntimeError(f'bridge input unavailable: {self.error}') from self.error
                remaining = None if deadline is None else deadline - time.monotonic()
                if remaining is not None and remaining <= 0:
                    raise TimeoutError(f'timed out waiting for {request_id}')
                self.condition.wait(remaining)
            envelope = self.commands.pop(request_id)
        if envelope.get('type') == 'error':
            raise RuntimeError((envelope.get('payload') or {}).get('error') or 'bridge error')
        return envelope

    def take_controls(self, turn_id):
        with self.condition:
            selected = [command for command in self.controls if command.get('turn_id') == turn_id]
            self.controls = [command for command in self.controls if command.get('turn_id') != turn_id]
            return selected
