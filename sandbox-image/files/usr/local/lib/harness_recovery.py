"""Read-only recovery of one retired execution's retained journal."""
import fcntl
import hashlib
import os
from pathlib import Path
import sqlite3
import tempfile

from harness_files import directory_fd, open_root, regular_fd
from harness_stream import Decoder, JournalGap, MAX_FRAME_BYTES, VERSION, encode

MAX_SNAPSHOT_BYTES = 512 << 20


class RecoveryJournal:
    def __init__(self, root, execution):
        if not execution or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-' for c in execution):
            raise ValueError('safe execution identity required')
        self.execution = execution
        self.root = self.partition = self.producer = None
        self.snapshot = self.db = None
        try:
            self.root = open_root(root)
            self.partition = directory_fd(execution, self.root)
            self.producer = regular_fd(self.partition, 'producer.lock')
            fcntl.flock(self.producer, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.snapshot = tempfile.TemporaryDirectory(prefix='harness-journal-recovery-')
            budget = MAX_SNAPSHOT_BYTES
            for name in ('journal.db', 'journal.db-wal'):
                try:
                    source = regular_fd(self.partition, name)
                except FileNotFoundError:
                    if name.endswith('-wal'):
                        continue
                    raise
                try:
                    with open(Path(self.snapshot.name) / name, 'xb') as target:
                        while True:
                            chunk = os.read(source, 1 << 20)
                            if not chunk:
                                break
                            budget -= len(chunk)
                            if budget < 0:
                                raise JournalGap('retired journal snapshot exceeds recovery limit')
                            target.write(chunk)
                finally:
                    os.close(source)
            # SQLite may rebuild shared-memory metadata, but only in this private
            # copy. Never open or checkpoint the original guest database.
            self.db = sqlite3.connect(f'file:{self.snapshot.name}/journal.db?mode=ro', uri=True)
            self.db.execute('PRAGMA query_only=ON')
            self.db.execute('PRAGMA trusted_schema=OFF')
            self.head, self.ack = self.db.execute('SELECT head,ack FROM positions WHERE singleton=1').fetchone()
            if type(self.head) is not int or type(self.ack) is not int or not 0 <= self.ack <= self.head:
                raise JournalGap('invalid retired journal positions')
            count, first, last = self.db.execute('SELECT count(*),min(sequence),max(sequence) FROM records').fetchone()
            if count != self.head - self.ack or (count and (first != self.ack + 1 or last != self.head)):
                raise JournalGap('retired journal does not cover its declared head')
        except BaseException:
            self.close()
            raise

    def close(self):
        if self.db is not None:
            self.db.close()
            self.db = None
        if self.snapshot is not None:
            self.snapshot.cleanup()
            self.snapshot = None
        for name in ('producer', 'partition', 'root'):
            descriptor = getattr(self, name)
            if descriptor is not None:
                os.close(descriptor)
                setattr(self, name, None)

    def replay(self, after):
        if type(after) is not int or not self.ack <= after <= self.head:
            raise JournalGap('host position is outside retired journal range')
        rows = self.db.execute('SELECT sequence,length(payload) FROM records WHERE sequence>? ORDER BY sequence', (after,))
        for sequence, length in rows:
            if sequence != after + 1 or sequence > self.head or type(length) is not int or not 0 < length <= MAX_FRAME_BYTES + 2:
                raise JournalGap('invalid retired journal record boundary')
            payload, digest = self.db.execute('SELECT payload,digest FROM records WHERE sequence=?', (sequence,)).fetchone()
            if not isinstance(payload, bytes) or hashlib.sha256(payload).hexdigest() != digest:
                raise JournalGap('retired journal digest mismatch')
            frames = Decoder().feed(payload)
            if len(frames) != 1 or frames[0].get('kind') != 'record' or frames[0].get('sequence') != sequence or frames[0].get('execution') != self.execution:
                raise JournalGap('retired journal identity mismatch')
            yield frames[0]
            after = sequence
        if after != self.head:
            raise JournalGap('retired journal tail is missing')

    def synchronize(self, request, destination):
        if request.get('kind') != 'sync' or request.get('execution') != self.execution or not request.get('command_id'):
            raise ValueError('historical reader accepts scoped synchronization only')
        after = request.get('after', 0)
        if type(after) is not int or not self.ack <= after <= self.head:
            raise JournalGap('host position is outside retired journal range')
        destination.write(encode({'version': VERSION, 'kind': 'state', 'execution': self.execution,
                                  'command_id': request['command_id'], 'after': self.ack, 'head': self.head}))
        destination.flush()
        for record in self.replay(after):
            destination.write(encode(record))
            destination.flush()
