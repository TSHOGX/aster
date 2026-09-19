"""Descriptor-relative guest file operations; no engine/private host paths."""
import hashlib
import json
import os
from pathlib import Path
import stat
import uuid

ALLOWED_ROOTS = frozenset({'/workspace', '/root/.pi/agent/sessions'})


class UncertainMutation(RuntimeError):
    pass


def clean_path(name):
    if not isinstance(name, str) or not name or '\\' in name or '\x00' in name:
        raise ValueError('invalid relative path')
    if any(part in {'', '.', '..'} for part in name.split('/')):
        raise ValueError('invalid relative path')
    return name


def directory_fd(path, parent=None):
    # gVisor may resolve a directory symlink with O_DIRECTORY|O_NOFOLLOW.
    # O_PATH plus an explicit type check pins the entry itself first.
    anchor = os.open(path, os.O_PATH | os.O_NOFOLLOW, dir_fd=parent)
    try:
        if not stat.S_ISDIR(os.fstat(anchor).st_mode):
            raise ValueError('directory component is not a directory')
        return os.open('.', os.O_RDONLY | os.O_DIRECTORY, dir_fd=anchor)
    finally:
        os.close(anchor)


def open_root(path):
    path = str(path)
    if not path.startswith('/'):
        raise ValueError('absolute file root required')
    current = directory_fd('/')
    try:
        for component in path.split('/')[1:]:
            if not component or component in {'.', '..'}:
                raise ValueError('clean absolute file root required')
            child = directory_fd(component, current)
            os.close(current)
            current = child
        return current
    except BaseException:
        os.close(current)
        raise


def parent_fd(root, name, create=False):
    components = clean_path(name).split('/')
    current = os.dup(root)
    try:
        for component in components[:-1]:
            try:
                child = directory_fd(component, current)
            except FileNotFoundError:
                if not create:
                    raise
                try:
                    os.mkdir(component, 0o755, dir_fd=current)
                except FileExistsError:
                    pass
                child = directory_fd(component, current)
                os.fsync(child)
                os.fsync(current)
            os.close(current)
            current = child
        return current, components[-1]
    except BaseException:
        os.close(current)
        raise


def regular_fd(parent, name):
    descriptor = os.open(name, os.O_PATH | os.O_NOFOLLOW, dir_fd=parent)
    try:
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            raise ValueError('file is not regular')
        return os.open(f'/proc/self/fd/{descriptor}', os.O_RDONLY)
    finally:
        os.close(descriptor)


def file_digest(root, name, sync=False, native_id=None):
    parent, leaf = parent_fd(root, name)
    try:
        descriptor = regular_fd(parent, leaf)
        try:
            digest = hashlib.sha256()
            size = 0
            header = bytearray()
            header_complete = False
            while True:
                chunk = os.read(descriptor, 1 << 20)
                if not chunk:
                    break
                size += len(chunk)
                digest.update(chunk)
                if native_id is not None and not header_complete:
                    prefix, separator, _ = chunk.partition(b'\n')
                    header.extend(prefix)
                    if len(header) > 65536:
                        raise ValueError('native session header exceeds limit')
                    header_complete = bool(separator)
            if native_id is not None:
                identity = json.loads(header)
                if not native_id or identity.get('type') != 'session' or identity.get('id') != native_id:
                    raise ValueError('native session identity mismatch')
            if sync:
                os.fsync(descriptor)
                os.fsync(parent)
            return {'path': name, 'size': size, 'sha256': digest.hexdigest()}
        finally:
            os.close(descriptor)
    finally:
        os.close(parent)


def snapshot_native(root, name, destination, native_id, hook=None):
    clean_path(destination)
    if not destination.startswith('.harness-completed/') or len(destination.split('/')) != 2 or not native_id:
        raise ValueError('completed native snapshot destination and identity required')
    parent, leaf = parent_fd(root, name)
    try:
        target, final = parent_fd(root, destination, create=True)
    except BaseException:
        os.close(parent)
        raise
    staging = '.harness-snapshot-' + uuid.uuid4().hex
    try:
        source = regular_fd(parent, leaf)
        try:
            before = os.fstat(source)
            descriptor = os.open(staging, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=target)
            try:
                digest, size = hashlib.sha256(), 0
                header, complete = bytearray(), False
                while True:
                    chunk = os.read(source, 1 << 20)
                    if not chunk:
                        break
                    size += len(chunk)
                    if size > 512 << 20:
                        raise ValueError('native snapshot exceeds byte limit')
                    digest.update(chunk)
                    if not complete:
                        prefix, separator, _ = chunk.partition(b'\n')
                        header.extend(prefix)
                        if len(header) > 65536:
                            raise ValueError('native session header exceeds limit')
                        complete = bool(separator)
                    view = memoryview(chunk)
                    while view:
                        view = view[os.write(descriptor, view):]
                identity = json.loads(header)
                if identity.get('type') != 'session' or identity.get('id') != native_id:
                    raise ValueError('native snapshot identity mismatch')
                after = os.fstat(source)
                if (before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (after.st_size, after.st_mtime_ns, after.st_ctime_ns):
                    raise ValueError('native source changed during completed snapshot')
                os.fsync(source)
                os.fsync(parent)
                os.fchmod(descriptor, 0o444)
                os.fsync(descriptor)
                if hook:
                    hook('snapshot-synced')
                try:
                    os.link(staging, final, src_dir_fd=target, dst_dir_fd=target, follow_symlinks=False)
                except FileExistsError:
                    prior = file_digest(root, destination, sync=True, native_id=native_id)
                    if prior['size'] != size or prior['sha256'] != digest.hexdigest():
                        raise ValueError('completed native snapshot already names different bytes')
                if hook:
                    hook('snapshot-published')
                os.fsync(target)
                return {'path': destination, 'size': size, 'sha256': digest.hexdigest()}
            finally:
                os.close(descriptor)
        finally:
            os.close(source)
    finally:
        try:
            os.unlink(staging, dir_fd=target)
        except FileNotFoundError:
            pass
        os.close(target)
        os.close(parent)


def restore_native(root, name, snapshot, native_id, size, digest, operation):
    if not snapshot.startswith('.harness-completed/'):
        raise ValueError('completed native snapshot required')
    preserved = file_digest(root, snapshot, sync=True, native_id=native_id)
    if preserved['size'] != size or preserved['sha256'] != digest:
        raise ValueError('completed native snapshot no longer matches recorded bytes')
    current = file_digest(root, name, sync=True, native_id=native_id)
    if current['size'] == size and current['sha256'] == digest:
        return current
    # Preserve the interrupted native source before replacing the selected file.
    # A retry following rename checks the restored digest first, avoiding a
    # contradictory second archive of the already-restored completed bytes.
    snapshot_native(root, name, '.harness-completed/interrupted-' + operation + '.jsonl', native_id)
    parent, leaf = parent_fd(root, snapshot)
    try:
        descriptor = regular_fd(parent, leaf)
        with os.fdopen(descriptor, 'rb') as source:
            result = replace(root, name, source, size, digest, operation)
        verified = file_digest(root, name, sync=True, native_id=native_id)
        if result != verified:
            raise ValueError('restored native state changed before verification')
        return result
    finally:
        os.close(parent)


def replace(root, name, source, size, expected_digest, operation, hook=None):
    if type(size) is not int or size < 0 or size > 8 << 30:
        raise ValueError('invalid replacement size')
    if not isinstance(expected_digest, str) or len(expected_digest) != 64:
        raise ValueError('replacement digest required')
    if not operation or any(character not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-' for character in operation):
        raise ValueError('safe operation identity required')
    parent, leaf = parent_fd(root, name, create=True)
    staging = '.harness-upload-' + operation + '-' + uuid.uuid4().hex
    try:
        if hook:
            hook('parent-open')
        descriptor = os.open(staging, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        try:
            remaining = size
            digest = hashlib.sha256()
            while remaining:
                chunk = source.read(min(remaining, 1 << 20))
                if not chunk:
                    raise ValueError('replacement content is shorter than declared size')
                remaining -= len(chunk)
                digest.update(chunk)
                view = memoryview(chunk)
                while view:
                    written = os.write(descriptor, view)
                    view = view[written:]
            if source.read(1):
                raise ValueError('replacement content exceeds declared size')
            if digest.hexdigest() != expected_digest:
                raise ValueError('replacement digest mismatch')
            os.fchmod(descriptor, 0o644)
            os.fsync(descriptor)
            if hook:
                hook('file-synced')
            try:
                existing = os.stat(leaf, dir_fd=parent, follow_symlinks=False)
                if not stat.S_ISREG(existing.st_mode):
                    raise ValueError('replacement target is not a regular file')
            except FileNotFoundError:
                pass
            os.rename(staging, leaf, src_dir_fd=parent, dst_dir_fd=parent)
            try:
                if hook:
                    hook('renamed')
                os.fsync(parent)
            except BaseException as error:
                raise UncertainMutation('replacement visible; parent durability unresolved') from error
            return {'path': name, 'size': size, 'sha256': digest.hexdigest()}
        finally:
            os.close(descriptor)
    finally:
        try:
            os.unlink(staging, dir_fd=parent)
        except FileNotFoundError:
            pass
        os.close(parent)


def cleanup_replacement(root, name, operation):
    if not operation or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-' for c in operation):
        raise ValueError('safe mutation identity required')
    parent, _ = parent_fd(root, name)
    try:
        prefix = '.harness-upload-' + operation + '-'
        for entry in os.listdir(parent):
            if entry.startswith(prefix):
                if not stat.S_ISREG(os.stat(entry, dir_fd=parent, follow_symlinks=False).st_mode):
                    raise ValueError('replacement staging entry is not regular')
                os.unlink(entry, dir_fd=parent)
        os.fsync(parent)
        return {'path': name}
    finally:
        os.close(parent)


def read_range(root, name, destination, offset=0, length=None):
    if type(offset) is not int or offset < 0 or length is not None and (type(length) is not int or length < 0):
        raise ValueError('invalid file range')
    parent, leaf = parent_fd(root, name)
    try:
        descriptor = regular_fd(parent, leaf)
        try:
            os.lseek(descriptor, offset, os.SEEK_SET)
            remaining = length
            while remaining is None or remaining > 0:
                chunk = os.read(descriptor, min(remaining, 1 << 20) if remaining is not None else 1 << 20)
                if not chunk:
                    break
                destination.write(chunk)
                if remaining is not None:
                    remaining -= len(chunk)
        finally:
            os.close(descriptor)
    finally:
        os.close(parent)


def native_state_barrier(path, root='/root/.pi/agent/sessions'):
    relative = str(Path(path).relative_to(root))
    descriptor = open_root(root)
    try:
        return file_digest(descriptor, relative, sync=True)
    finally:
        os.close(descriptor)


def metadata(root, name):
    parent, leaf = parent_fd(root, name)
    try:
        descriptor = regular_fd(parent, leaf)
        try:
            info = os.fstat(descriptor)
            return {'path': name, 'size': info.st_size, 'modified_ns': info.st_mtime_ns}
        finally:
            os.close(descriptor)
    finally:
        os.close(parent)


def list_directory(root, name, after='', limit=32):
    if name:
        parent, leaf = parent_fd(root, name)
        try:
            descriptor = directory_fd(leaf, parent)
        finally:
            os.close(parent)
    else:
        descriptor = os.dup(root)
    try:
        names = []
        with os.scandir(descriptor) as entries:
            for entry in entries:
                names.append(entry.name)
                if len(names) > 10000:
                    raise ValueError('workspace directory exceeds inventory limit')
        names.sort()
        selected = [entry for entry in names if entry > after][:limit]
        entries = []
        for entry in selected:
            clean_path(entry)
            entry.encode('utf-8')
            try:
                info = os.stat(entry, dir_fd=descriptor, follow_symlinks=False)
            except FileNotFoundError:
                continue
            kind = 'other'
            if stat.S_ISREG(info.st_mode):
                kind = 'file'
            elif stat.S_ISDIR(info.st_mode):
                kind = 'directory'
            elif stat.S_ISLNK(info.st_mode):
                kind = 'symlink'
            entries.append({'name': entry, 'type': kind, 'size': info.st_size, 'modified_ns': info.st_mtime_ns})
        next_name = selected[-1] if selected and any(entry > selected[-1] for entry in names) else ''
        return {'entries': entries, 'next': next_name}
    finally:
        os.close(descriptor)
