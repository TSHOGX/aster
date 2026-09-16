# Private reference data

The generic image contains no business corpus. Operators may provision authorized
reference material into a private KB snapshot and add the corresponding INDEX,
facts and skills there. Keep source filenames, internal schemas, bucket layouts
and credentials outside the public repository and release build inputs.

Running generations stay pinned to their mounted snapshot. Publish a new immutable
snapshot to change reference data; do not mutate a live snapshot in place.
