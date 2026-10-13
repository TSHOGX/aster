# Installation tools

Use the operator guides for commands that prepare and inspect deployment artifacts. `candidate.py build` exports committed source; `verify` checks its inventory and `inspect` reports deployment identity. These commands do not activate services.

`rebuild.py` assembles application, Studio, the pinned Khora engine and a fresh guest
runtime from one committed revision in an isolated checkout. `deploy.py` validates
that complete build against a prepared local host and supervisor plan. Activation
requires its explicit `--fresh` option, which erases application, Studio and engine
state and creates new administrator credentials.

Run `python3 scripts/rebuild.py --help` and `python3 scripts/deploy.py --help`
for arguments. The complete builder requires a clean committed checkout by default;
`--source-ref` explicitly selects a committed revision. Deployment requires a
separately prepared host, private configuration and supervisor templates. Run it
without `--fresh` to validate those inputs before activation. These tools have not
yet been qualified together by a complete build and deployment on a prepared
`/etc/aster` host. They do not automatically adapt an existing installation's
service names or storage paths.

The deployment plan is a private JSON file with these required keys:

```json
{
  "config": "/etc/aster/application.json",
  "namespace": "aster",
  "unit_templates": "/etc/aster/unit-templates"
}
```

Prepare `asterapp` and `asterweb`, the `/var/lib/aster` and
`/var/lib/aster-studio` state directories, and a dedicated btrfs mount at
`/srv/aster/engine`. The namespace, TLS, provider settings and environment files
must already exist. The script reads `/etc/aster/owner.env`, `gateway.env` and
`studio.env` before stopping services. Store environment values as literal
`NAME=value` assignments, optionally quoted; shell expansion is not performed.

Provide one `aster-NAME.service` template for each of `web`, `gateway`,
`application`, `studio`, `frontend`, `admin-frontend`, `studio-frontend`,
`studio-tls`, `engine-tls` and `engine`. Supported replacements are
`@APPLICATION@`, `@STUDIO@`, `@ENGINE@`, `@RUNTIME@` and `@CONFIG@`.
Templates must select the prepared namespace, state paths and the built runtime
as the engine template named by the application configuration. Keep both the
build directory and unit templates outside all reset directories.

Run validation without `--fresh` first. It checks artifact inventories, the engine
identity, runtime contents, configuration, reset-directory boundaries and unit
syntax. Back up the installation using the [recovery guide](../docs/operators/recovery.md)
before explicitly choosing `--fresh`; the script does not create a backup.

- [Install Aster](../docs/operators/installation.md)
- [Upgrade services](../docs/operators/upgrades.md)
