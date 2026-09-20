# Khora development scope

This branch targets Khora exclusively. Development may break production compatibility
and uses a single current database schema, with no intermediate-schema upgrades.
Sessions, messages, workspaces, scheduling, execution recovery, authorization, trace
production and Pi native history remain product responsibilities. Memory and Evolve
are removed. Shared assets are manually maintained, versioned and read-only.

Development checks must cover core behavior, recovery, authorization and real Khora
integration. Production upgrades, migration tooling, rollout, rollback and production
acceptance are deferred together to a separate project after implementation is complete.
Production Memory/Evolve data will be archived separately, not imported into this
system. Development work must not change production data.

The local branch development plan is `private/khora-refactor-plan.md`. Private
evidence and machine-specific plans remain untracked and must not enter release
artifacts.
