# Navigation, scheduling, and network policy delivery

This work replaces ambiguous route state, the Cron-first schedule editor, and
startup-only network defaults. No legacy client contract is retained. Existing
data and durable operation evidence must remain accounted for; removing backward
compatibility does not authorize deleting user data.

## Ordered stages

1. **Route isolation.** Normalize routes on parsing, navigation, serialization,
   and history. Give received conversations their own explicit selection and an
   authoritative grant lookup. Guard unsaved files before leaving a Project.
   Test stale URLs, late reads, revocation, deep links, and Back/Forward.
2. **Scheduling contracts.** Add authoritative recurrence previews, bounded
   recurrence metadata, owner-scoped title/status filtering, and chronological
   pagination with explicit invalidation on concurrent changes. Characterize DST
   using the scheduler's parser. Keep prompts out of overview responses.
3. **Scheduled workspace.** Replace the modal editor with responsive list/detail
   and explicit create/edit routes. Add visual recurrence controls, localized
   summaries, previews, draft guards, lossless advanced rules, selected history,
   idempotent actions, and revision-conflict reconciliation.
4. **Network lifecycle.** Publish validated immutable direct/DNS policy snapshots;
   reject unsupported configuration edits. Record target policy in durable
   creation/resume intent, conditionally update paused Khora bindings, reconcile
   uncertain replies, and project matching DNS before admitting guest work.
   Running placements retain their effective policy. Integrate diagnostics and
   release/handoff identity explicitly.
5. **Qualification and documentation.** Remove superseded implementations and
   stale contracts, run race/vet and frontend production checks, exercise focused
   browser journeys, and record runtime qualification separately from fixtures.

Each implementation stage receives a Conventional Commit after its relevant
checks. Changes discovered during validation remain part of the owning stage.

## Design constraints

- Global routes never inherit Project selection. Workspace preferences are
  remembered state, not current-page authority.
- A received-conversation detail requires an active reader grant independently
  of the paginated list. Generic owner-or-reader message access remains valid.
- Schedule search and ordering apply before pagination. Pages remain bounded;
  there is no per-row detail fan-out or overview polling.
- Daily 09:00 in the detected IANA zone is the visible creation default. Manual
  execution is explicit. Complex Cron remains exact unless deliberately replaced.
- Preview and execution share the same parser. No arbitrary intervals, one-time
  schedules, notification settings, or schedule-level completion are invented.
- Mutable network settings are limited to direct IPv4 rules and resolvers.
  Retained model routes, host mappings, resource identities, and volumes survive.
- Existing schema migration and owner activation gates govern persistence changes.
  Historical release qualification does not certify the new contracts.

## Initial review findings

All proposal documents were reviewed, including their acceptance and deferred
scope. The code confirms route spreading and unvalidated shared selection, an
ID-paged schedule overview and modal Cron editor, and saved startup profiles with
no paused network-update adapter. The repository currently has schema migrations
and schema-13 qualification, despite the root README claiming no migrations.
That stale documentation must be corrected during final reconciliation.

## Progress

- Review and sequencing: complete.
- Route isolation: implemented. Twelve route/preference unit tests, frontend
  typecheck/lint, 43 production-build browser checks, and focused product and HTTP
  authorization race tests passed. Shared details use the explicit `conversation`
  parameter; old mixed Session URLs are canonicalized to the list.
- Implementation and qualification: in progress; stage evidence will be recorded
  here as checks complete.
