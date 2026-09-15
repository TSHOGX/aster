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
If temporary workspace-local files are unavoidable, use `.harness-local/` or
`*.local.json` / `*.local.yaml` / `*.local.md`, which are ignored. Ignore rules do
not protect already tracked files: inspect the staged diff before committing.

Only reviewed synthetic `.example.json` templates are shipped. The release
builder copies an explicit file allowlist, never an entire deployment/docs folder.
Hosted smoke tests use synthetic data; only schema-validated, allowlisted
technical results tied to the release manifest can be published. Real deployment
reports and business acceptance remain private to the target environment.

Deleting a file in a new commit does not remove previous Git objects, remote
refs, pull-request caches, artifacts or other clones. Before changing repository
visibility, audit all branches/tags/history and hosting-provider artifacts. Any
history replacement must be coordinated and use an exact lease; it does not
promise deletion of the hosting provider's cached or unreachable objects.

The current hardening addresses the new delivery work. It is not a certification
that all pre-existing repository history is safe to publish.
