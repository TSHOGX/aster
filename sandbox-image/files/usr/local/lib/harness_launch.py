"""Application launch/configuration for retained Khora guest overlays."""
import json
import os
from pathlib import Path
import tempfile

MAX_CONFIG_BYTES = 1 << 20


def _directory(path):
    if path.is_symlink():
        raise ValueError('agent directory must not be a symlink')
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not path.is_dir():
        raise ValueError('agent path is not a directory')
    _sync_dir(path)
    _sync_dir(path.parent)


def _read_config(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError('immutable config source must be a regular file')
    with open(path, 'rb') as source:
        raw = source.read(MAX_CONFIG_BYTES + 1)
    if len(raw) > MAX_CONFIG_BYTES:
        raise ValueError('agent config exceeds byte limit')
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError('agent config must be an object')
    return raw, value


def _sync_dir(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _replace_config(path, raw):
    descriptor, staging = tempfile.mkstemp(prefix='.config-', dir=path.parent)
    try:
        with os.fdopen(descriptor, 'wb') as output:
            output.write(raw)
            output.flush()
            os.fchmod(output.fileno(), 0o444)
            os.fsync(output.fileno())
        os.replace(staging, path)
        _sync_dir(path.parent)
    finally:
        try:
            os.unlink(staging)
        except FileNotFoundError:
            pass


def prepare_pi(home=Path('/root'), source=Path('/harness-config/pi'), assets=Path('/root/.agents')):
    """Pi needs a writable directory for settings locks, even for config reads."""
    for path in (home, home / '.pi', home / '.pi/agent', home / '.pi/agent/sessions'):
        _directory(path)
    agent = home / '.pi/agent'
    configs = {}
    for name in ('models.json', 'settings.json'):
        raw, parsed = _read_config(source / name)
        configs[name] = raw
        if name == 'models.json':
            providers = parsed.get('providers')
            if not isinstance(providers, dict) or set(providers) != {'harness_anthropic_proxy'}:
                raise ValueError('Pi config must select the external model gateway provider')
            provider = providers['harness_anthropic_proxy']
            if provider.get('apiKey') != 'harness-placeholder':
                raise ValueError('Pi config must contain only the non-secret gateway placeholder')
            if not isinstance(provider.get('models'), list) or not provider['models']:
                raise ValueError('Pi model catalog is required')
    for name, raw in configs.items():
        _replace_config(agent / name, raw)
    auth = agent / 'auth.json'
    if auth.is_symlink():
        raise ValueError('Pi auth path must not be a symlink')
    _replace_config(auth, b'{}\n')
    target = agent / 'AGENTS.md'
    shared = assets / 'AGENTS.md'
    if shared.is_file():
        if target.is_symlink():
            if os.readlink(target) == str(shared):
                return agent
            # A session-authored link is not owned by the launcher.
        elif not target.exists():
            target.symlink_to(shared)
            _sync_dir(agent)
    return agent


def launch_environment(driver):
    if driver not in {'pi', 'sh'}:
        raise ValueError('unsupported guest driver')
    environment = {
        'PATH': '/opt/node-v24.15.0/bin:/usr/local/bin:/usr/bin:/bin',
        'HOME': '/root', 'USER': 'root', 'LOGNAME': 'root',
        'LANG': 'C.UTF-8', 'HARNESS_BACKEND': 'khora',
        'PI_CODING_AGENT_DIR': '/root/.pi/agent',
        'PI_CODING_AGENT_SESSION_DIR': '/root/.pi/agent/sessions',
        'PI_OFFLINE': '1', 'PI_SKIP_VERSION_CHECK': '1', 'PI_TELEMETRY': '0',
    }
    # Launch identity is not guest secrecy or upstream authorization.
    for name in ('PI_MODEL', 'ANTHROPIC_BASE_URL'):
        if os.environ.get(name):
            environment[name] = os.environ[name]
    return environment
