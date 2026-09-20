"""Application launch/configuration for retained Khora guest overlays."""
import json
import hashlib
import os
from pathlib import Path
import tempfile

MAX_CONFIG_BYTES = 1 << 20


def template_protocol(driver, manifest=Path('/etc/harness-image/agents.json'), component_root=Path('/')):
    raw, metadata = _read_config(manifest)
    if metadata.get('schema_version') != 1:
        raise ValueError('guest template manifest schema is incompatible')
    runtime = metadata.get('runtime') or {}
    if (runtime.get('backend'), runtime.get('engine_revision'), runtime.get('bridge_stream_version'), runtime.get('home'), runtime.get('retained_overlay')) != ('khora', 'e8cd021a9c7c', 1, '/root', True):
        raise ValueError('guest template runtime is incompatible')
    if runtime.get('engine_module') != 'v0.0.2-0.20260920024301-e8cd021a9c7c':
        raise ValueError('guest template engine module is incompatible')
    selected = [entry for entry in metadata.get('drivers', []) if entry.get('driver_id') == driver]
    if len(selected) != 1 or selected[0].get('bridge_protocol_version') != 2 or selected[0].get('turn_input_schema') != 'RunTurn':
        raise ValueError('guest template driver protocol is incompatible')
    binary = Path(selected[0].get('binary_path', ''))
    if not binary.is_absolute() or '..' in binary.parts:
        raise ValueError('guest template driver binary path is invalid')
    binary = component_root / binary.relative_to('/')
    # Pi's installed executable is an npm symlink; its declared target bytes
    # are still checked against the release manifest.
    if not binary.is_file():
        raise ValueError('guest template driver binary is missing')
    with binary.open('rb') as source:
        driver_digest = hashlib.sha256()
        for chunk in iter(lambda: source.read(1 << 20), b''):
            driver_digest.update(chunk)
    if 'sha256:' + driver_digest.hexdigest() != selected[0].get('installed_binary_digest'):
        raise ValueError('guest driver binary digest mismatch')
    files = runtime.get('files') or {}
    required = ('/usr/local/bin/harness-bridge-client', '/usr/local/bin/harness-khora-launcher',
                '/usr/local/lib/harness_stream.py', '/usr/local/lib/harness_launch.py')
    if any(path not in files for path in required):
        raise ValueError('guest template component identity missing')
    for name, expected in files.items():
        path = Path(name)
        if not path.is_absolute() or '..' in path.parts:
            raise ValueError('guest template component path is invalid')
        path = component_root / path.relative_to('/')
        if path.is_symlink() or not path.is_file():
            raise ValueError('guest template component is not a regular file')
        digest = hashlib.sha256()
        with path.open('rb') as source:
            for chunk in iter(lambda: source.read(1 << 20), b''):
                digest.update(chunk)
        if 'sha256:' + digest.hexdigest() != expected:
            raise ValueError('guest template component digest mismatch')
    canonical = json.dumps(metadata, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode('utf-8')
    return {'protocol_version': 2, 'driver_id': driver, 'turn_input_schema': 'RunTurn',
            'manifest_digest': 'sha256:' + hashlib.sha256(canonical).hexdigest()}


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


def _replace_config(path, raw, mode=0o444):
    descriptor, staging = tempfile.mkstemp(prefix='.config-', dir=path.parent)
    try:
        with os.fdopen(descriptor, 'wb') as output:
            output.write(raw)
            output.flush()
            os.fchmod(output.fileno(), mode)
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
            if not isinstance(providers, dict) or not providers:
                raise ValueError('Pi config must declare external gateway providers')
            for provider in providers.values():
                if not isinstance(provider, dict) or provider.get('apiKey') != 'harness-placeholder':
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


SERVICE_HOSTS_MARKER = '# harness service hosts'


def project_service_hosts(entries, path=Path('/etc/hosts')):
    """Publish this generation's service hostnames for the guest resolver.

    The engine projects no /etc/hosts, and a guest must resolve the exact
    synthetic addresses the confirmed network declaration steers. The block is
    rewritten whole on every launch, so a superseded generation's names cannot
    survive into the next one, and anything the image shipped above it is kept.
    """
    resolved = {}
    for entry in entries or ():
        name, separator, address = entry.partition('=')
        name, address = name.strip().lower(), address.strip()
        if not separator or not name or not address:
            raise ValueError('service host must be name=address')
        if name in resolved:
            raise ValueError('duplicate service hostname')
        if any(character.isspace() for character in address) or ':' in address:
            raise ValueError('service host address must be a bare IPv4 literal')
        octets = address.split('.')
        if len(octets) != 4 or not all(part.isdigit() and 0 <= int(part) <= 255 and str(int(part)) == part for part in octets):
            raise ValueError('service host address must be a bare IPv4 literal')
        for label in name.split('.'):
            if not label or label.startswith('-') or label.endswith('-') or not all(c.isalnum() or c == '-' for c in label):
                raise ValueError('invalid service hostname')
        resolved[name] = address
    if path.is_symlink():
        raise ValueError('host file must not be a symlink')
    existing = path.read_text(encoding='utf-8').splitlines() if path.is_file() else []
    preserved = []
    for line in existing:
        if line.strip() == SERVICE_HOSTS_MARKER:
            break
        preserved.append(line)
    body = list(preserved)
    if resolved:
        body.append(SERVICE_HOSTS_MARKER)
        for name in sorted(resolved):
            body.append(f'{resolved[name]}\t{name}')
    _replace_config(path, ('\n'.join(body) + '\n').encode('utf-8'), mode=0o444)
    return resolved


def launch_environment(driver):
    if driver not in {'pi', 'sh'}:
        raise ValueError('unsupported guest driver')
    environment = {
        'PATH': '/opt/node-v24.15.0/bin:/usr/local/bin:/usr/bin:/bin',
        'HOME': '/root', 'USER': 'root', 'LOGNAME': 'root',
        'LANG': 'C.UTF-8',
        'PI_CODING_AGENT_DIR': '/root/.pi/agent',
        'PI_CODING_AGENT_SESSION_DIR': '/root/.pi/agent/sessions',
        'PI_OFFLINE': '1', 'PI_SKIP_VERSION_CHECK': '1', 'PI_TELEMETRY': '0',
    }
    return environment
