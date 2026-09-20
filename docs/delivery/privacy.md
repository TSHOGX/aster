# Public source and private operations

This repository and its Release assets must be safe to publish. They contain
source, generic examples, build inputs and synthetic test results only.

Do not commit host inventories, machine/cloud IDs, actual hostnames or addresses,
real deployment paths/account mappings, capacity/incident snapshots, runtime PIDs,
production BUILD_IDs, actual executable fingerprints, database backups, operation
journals, screenshots, raw qualification logs or credentials. Public software
versions and source-commit migration fixtures are different from observations
about a particular machine.

Keep target configuration and operational records outside the checkout in the
operator's private configuration/state directories or private operations system.
For temporary operational handoff, use the ignored root `private/` directory
with restricted permissions. It may hold archived local scripts/configuration and
audit evidence, but must never be force-added, globbed into release inputs or
served by the application. Alternatively use `.harness-local/` or
`*.local.json` / `*.local.yaml` / `*.local.md`, which are ignored. Ignore rules do
not protect already tracked files: inspect the staged diff before committing.

Synthetic fixtures may be versioned. Real deployment reports, raw integration
logs and machine-specific plans remain private. No production release pipeline
is provided by this development branch.

Deleting a file in a new commit does not remove previous Git objects, remote
refs, pull-request caches, artifacts or other clones. Before changing repository
visibility, audit all branches/tags/history and hosting-provider artifacts. Any
history replacement must be coordinated and use an exact lease; it does not
promise deletion of the hosting provider's cached or unreachable objects.

Current-tree hardening covers public references, defaults, code comments and
embedded examples. It is not a certification
that all pre-existing repository history is safe to publish.
