# Deployment compatibility

[Documentation](../README.md) · Reference

Aster supports one current application schema and protocol set. A historical or
mismatched database is refused; startup does not migrate, convert or delete it.
An unsupported transition needs an explicitly planned fresh installation.

The account language preference is part of the current product schema. Builds
without that field have a different schema identity; upgrading such an installation
requires a separately planned fresh installation under the same compatibility policy.

## Inspect before upgrading

Run the selected executable's `release-identity` with the intended configuration,
and compare it with live diagnostics as described in [upgrades](../operators/upgrades.md).
Use the reported identity rather than a version label or a copied numeric protocol
list. It identifies executable bytes, schema and host configuration/protocols.

Owner handoff also checks persisted runtime, environment, network and account/resource
configuration. Matching compatibility alone does not bypass readiness or authorize
a different executable after activation. Configuration's base directory matters
because relative paths resolve there.

## Retained Projects and multiple versions

Several immutable runtime templates can coexist if each satisfies the current
host/runtime contract. Existing Projects retain their exact registered runtime,
owned storage and mount plan. Changing the default selects a choice for new Projects;
it does not migrate old ones. Keep referenced versions available until their
Projects and cleanup no longer depend on them.

Compatible data updates are narrower than runtime upgrades: they replace selected
read-only data publications at a start/resume boundary, without adopting the entire
new template. See [environment updates](../admin/environment-updates.md).

## Unsupported changes and rollback

Changing the engine store or using unsupported persisted formats cannot be repaired
by editing IDs or adding a compatibility override. Follow the explicitly scoped
[reset procedure](../operators/recovery.md#deliberate-destructive-reset) when a fresh
installation is chosen. This discards selected old Project state; it does not
convert history or make restoring an incompatible database safe.

After activation, only the selected executable can complete the handoff. There is
no rollback-candidate flag. Repair that candidate while admission remains closed.
A complete matching offline backup is a separate recovery resource, not permission
to mix an old database with newer engine/volume state.

Studio differs from Aster: its authoring database uses ordered migrations. Back it
up before upgrading. Older Studio binaries refuse newer schemas; a binary-only
downgrade is not a restore. See [Studio operations](../../volumes-studio/deploy/README.md).
