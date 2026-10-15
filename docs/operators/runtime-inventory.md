# Manage runtime and content inventory

[Documentation](../README.md) · Deployment and operations

Runtime registrations and content publications are immutable versions. Registering
or publishing makes inventory available; it does not automatically select it in an
environment or replace an existing Project's saved runtime.

## Prepare an immutable runtime

Use a compatible, trusted engine template and its exported manifest. When assembling
one locally, use a new destination on a disposable privileged Linux builder with
the source-pinned Go/Node/native inputs, debootstrap/chroot tooling, module access
and reviewed package mirrors:

```sh
sudo env SANDBOX_FAMILY=minimal SANDBOX_RELEASE_VERSION=v1 \
  sandbox-image/build-khora-template.sh /srv/build/sandbox-minimal-v1
sudo env SANDBOX_FAMILY=work SANDBOX_RELEASE_VERSION=v1 \
  sandbox-image/build-khora-template.sh /srv/build/sandbox-work-v1
```

The destination and adjacent `.manifest.json` must not exist. An optional second
argument can name a pure OS/Pi base; an existing application template is not a
valid base. The base must have the matching `/etc/aster-sandbox-family` marker.
Minimal includes the runner, Pi, Node, Git and shell tools. Work adds the pinned
Python/data/document packages. Each generated `system.md` contains only the release
name and version; organization instructions belong in shared assets. Never patch installed guest binaries in place or include credentials,
user history or nonempty workspace/runtime state in a new image.

Review [builder configuration](../../sandbox-image/build.env.example) and
[workload package pins](../../sandbox-image/requirements.lock). Preserve the exported
manifest and use its exact digest. Image assembly does not establish production
suitability; qualify the final image with the matching application and host substrate
before selecting it. Keep build and acceptance records in private storage.

## Register a compatible runtime version

After installing the engine template, register it through the protected owner socket:

```sh
orchestrator runtime register --socket /var/lib/agent-workbench/control.sock \
  --id agent-runtime --version v2 --template agent-runtime-v2 \
  --manifest /srv/build/agent-runtime-v2.manifest.json --digest sha256:EXACT_DIGEST
```

Use the generated manifest digest, not an executable hash or arbitrary label.
Registration validates the current contract and engine identity and returns
`default_changed: false`. Select the registered version in an environment separately.
Several compatible versions may coexist; retained Projects continue to need their
exact versions even after the default changes.

## Publish reviewed system content

Prepare private reviewed copies of the [Agent configuration](../../config/agent-config.example/README.md)
and shared assets, then publish them using their explicit configuration. The base
shared-assets source has root `AGENTS.md` and an empty `skills/` directory; create
that directory when copying a source archive, which cannot preserve empty
directories. The complete builder prepares it under `content/shared-assets`.
Keep `pi/APPEND_SYSTEM.md` empty unless adding reviewed platform instructions.
Ordinary Studio editing never exposes agent configuration.

```sh
mkdir -p /srv/reviewed/shared-assets/skills
```

Publish the configured source:

```sh
orchestrator publish --socket /var/lib/agent-workbench/control.sock \
  --config /srv/reviewed/bootstrap.json
```

Sources are not watched for live changes. Publication produces immutable read-only
content; it neither changes the database default nor silently upgrades existing
Projects. Original source paths are not needed for ordinary startup after successful
publication, but retain reviewed inputs privately for future publication/recovery.

## Publish a data or workspace source

Inspect the exact reviewed source, then publish with explicit capacities and roles:

```sh
digest=$(orchestrator volume inspect-source --source /srv/reviewed/examples | jq -r .digest)
orchestrator volume publish --socket /var/lib/agent-workbench/control.sock \
  --source /srv/reviewed/examples --id examples --version v1 --digest "$digest" \
  --capacity-bytes 8388608 --minimum-bytes 1048576 --roles data,workspace
```

The source must be an absolute regular directory of bounded, non-executable files.
Review the inspected result before publishing. Capacity is the publication's physical
allocation declaration; minimum bytes is the minimum allowed copy capacity. Source
changes, invalid paths, duplicate roles and insufficient capacity are refused.
Use `--clean-runtime-seed` only when explicitly asserting that a runtime seed has
no identity, journals, live handles or secrets.

Publication can retain staging and derivative allocations during uncertain outcomes.
Do not delete staging to clear capacity. Repeating the identical publication is
idempotent; reusing its immutable identity for different bytes or capacity conflicts.
Use [Volume Studio](../admin/volume-studio.md) for libraries authored and reviewed through scoped grants.

## Disable, enable and retire

Inspect the exact identity and revision before a registry mutation:

```sh
orchestrator runtime inspect --socket /var/lib/agent-workbench/control.sock \
  --kind sandbox --id agent-runtime --version v2 --digest sha256:EXACT_DIGEST
orchestrator runtime disable --socket /var/lib/agent-workbench/control.sock \
  --kind sandbox --id agent-runtime --version v2 --digest sha256:EXACT_DIGEST \
  --expected-revision 1 --request-id disable-runtime-v2-01
```

Replace revision `1` with the inspected value. Use `--kind volume` for a publication.
`enable` and `retire` use the same identity/revision/request pattern. Retry an uncertain
reply with the identical request ID and body.

Disable prevents new selection/allocation but preserves existing saved references,
including resume and interrupted provisioning. Enable restores selection eligibility.
Neither changes defaults; keep an enabled eligible choice for new Projects.

Retire permanently tombstones an already disabled registration. It is refused while
referenced by environments/defaults, live/deleting Projects or unsettled work. Wait
for cleanup, not merely the deletion request. An ID/version cannot be reused after
retirement. Registry retirement does not delete engine templates/publication volumes
or reclaim their physical accounting; use the appropriate observed cleanup lifecycle.

Read [environment updates](../admin/environment-updates.md) before removing versions
retained for rollback, and [recovery](recovery.md) for unresolved allocations.
