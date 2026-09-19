"""Descriptor-relative guest file operations; no engine/private host paths."""
import hashlib
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


def file_digest(root, name, sync=False):
    parent, leaf = parent_fd(root, name)
    try:
        descriptor = regular_fd(parent, leaf)
        try:
            digest = hashlib.sha256()
            size = 0
            while True:
                chunk = os.read(descriptor, 1 << 20)
                if not chunk:
                    break
                size += len(chunk)
                digest.update(chunk)
            if sync:
                os.fsync(descriptor)
                os.fsync(parent)
            return {'path': name, 'size': size, 'sha256': digest.hexdigest()}
        finally:
            os.close(descriptor)
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
