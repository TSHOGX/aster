# Private reference data

The generic image contains no business corpus. Provision authorized reference
material in a private reviewed asset tree, with corresponding index, facts and
skills. Keep source filenames, internal schemas, bucket layouts and credentials
in `private/` or protected storage outside the repository.

Publish a new immutable source version when the reference material changes;
do not mutate a live snapshot. Existing Projects retain their mounted selection.
Compatible read-only data mounts can be changed through an explicit environment
update; required system publications follow their environment/runtime rules.

See [content publication](../../../docs/operators/runtime-inventory.md) and
[environment updates](../../../docs/admin/environment-updates.md).
