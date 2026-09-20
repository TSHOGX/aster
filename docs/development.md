# Development

Use a private data directory and fresh current-schema database. The application has
no embedded runtime and cannot open a legacy or earlier-branch database. Start the
HTTP daemon only with explicit product YAML and a validated Khora deployment JSON.
The existing integration tests contain synthetic deployment documents.

Khora is pinned in `orchestrator/go.mod`; private module access is needed for a cold
module cache. `scripts/fetch-go-dependencies.sh` handles the CI credential handoff.
No sibling-directory replacement is required.

Build/test commands are in [verification](verification.md). Pure Go tests run without
an engine. Real integration tests use the `khoraintegration` build tag and require an
isolated engine, a freshly finalized template and disposable storage/network namespaces.
Keep the engine store, volumes, cgroups and network namespace isolated from other work.

The frontend uses Node and npm; `npm ci`, then `npm run dev`. Set its orchestrator
origin for the local daemon. Authentication and same-origin proxy checks remain active.

Scope is [Khora-only branch development](development-scope.md). The private branch
plan describes overall objectives, publication, gateway composition, recovery and
sustained development verification. Preparation is complete; subsequent iterations
follow that plan when authorized as development work.
