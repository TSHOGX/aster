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
