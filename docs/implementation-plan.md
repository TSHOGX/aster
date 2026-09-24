# Navigation, scheduling, and network policy delivery

The original delivery implemented global route isolation, visual scheduling and
network policy reload. The subsequent destructive reset request replaces its
migration-preserving database approach with a fresh current-only installation.
The original sequence below records that work; the follow-up takes precedence.

## Fresh-installation follow-up

The subsequent destructive-reset request supersedes the original preservation
and migration plan below. The current implementation has one canonical schema,
no migration chain, no historical schema branches, no migration-only read cleanup,
and no old-executable rollback override. Deployment replaced legacy application
and engine state with fresh stores. The [fresh deployment](fresh-deployment.md)
is complete and supersedes the original no-deployment boundary. See [bootstrap](bootstrap.md) and the
[current contract](compatibility.md). The earlier qualification record describes
its recorded source only, not the newly rebuilt deployment.

## Original ordered stages

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

Each implementation stage received a Conventional Commit after its relevant
checks. Follow-up fixes discovered during qualification were committed separately
with regression coverage.

## Original design constraints

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
scope. The initial code confirmed route spreading and unvalidated shared selection, an
ID-paged schedule overview and modal Cron editor, and saved startup profiles with
no paused network-update adapter. The repository already had schema migrations
and schema-13 qualification, despite the root README claiming no migrations.
The README and affected architecture/API/operations contracts have been corrected.

## Progress

- Review and sequencing: complete.
- Route isolation: implemented. Twelve route/preference unit tests, frontend
  typecheck/lint, 43 production-build browser checks, and focused product and HTTP
  authorization race tests passed. Shared details use the explicit `conversation`
  parameter; old mixed Session URLs are canonicalized to the list.
- Scheduling contracts: implemented with schema 14, owner catalog epochs,
  Unicode title matching, chronological opaque cursors, authoritative previews,
  and explicit history continuation. Focused race tests cover migration rollback,
  multiple pages, authorization, filtering, concurrent ticks, month-end skips,
  and both DST transitions.
- Scheduled workspace: implemented with explicit routes, responsive panes,
  localized recurrence controls, exact advanced-rule retention, draft guards,
  bounded history, and uncertain-write/conflict recovery. Nineteen focused
  production browser tests passed, including light/dark English/Chinese forms at
  1440, 1280, 1024, 768, and 390px. Screenshots were inspected; the former modal
  and its CSS were removed. A moderated usability study remains unperformed.
- Network lifecycle: implemented with durable schema-15 defaults, atomic
  operation selection, conditional paused-binding replacement, recovery,
  diagnostics/control, automatic reload, and independent handoff policy checks.
  Focused race tests passed. The real isolated Khora/runsc network gate passed
  in 22.52 seconds, including DNS, 102,400 verified TLS package bytes, denied
  undeclared access, removal on resume, retained volumes and native model work.
  Public Internet connectivity was not exercised.
- Cross-cutting review: fixed late schedule mutation responses replacing another
  selection, kept turn-rail paging intent available during background refresh,
  retained expired-directory explanations across queued refreshes, aligned
  multibyte task names with server limits, and mapped actual run states. Removed
  obsolete schedule copy and routed recovery/retirement through transactional
  saved-profile selection.
- Final qualification: complete. Full Go race suite and vet passed, with focused
  race checks after recovery follow-ups. Frontend typecheck/lint, 186 unit tests
  (one opt-in skip), production builds, and the final 137-test browser sweep
  passed (42 opt-in skips). The real released browser passed three workflows in
  39.81 seconds; its separate combined-workload test was skipped. All six released
  owner/gateway/native/handoff tests passed. The sanitized
  [qualification record](../dev/measurements/proposal-delivery.json) contains
  source/artifact identities, outcomes, resolved failures and scope limits.

## Root causes and final authority

| Problem | Root cause | Final authority and behavior |
| --- | --- | --- |
| Global views display an unrelated conversation | Route spreading combined workspace identity with global-page selection; generic owner read access was treated as received sharing | Central normalization and destination construction discard unrelated fields; a grant-specific detail lookup precedes shared messages/SSE |
| Ordinary task creation requires Cron expertise | The modal exposed a raw expression and defaulted silently to manual-only | Visible Daily 09:00 default, visual presets, explicit manual/advanced modes, exact expression preservation and scheduler-backed previews |
| Global task filters/order cannot scale honestly | ID-only pagination and client-local state could not represent chronological filtered results | Owner-authorized server filtering and sorting with catalog-revision cursors from one read snapshot |
| Drafts or task selection can be lost on asynchronous work | Editing, reading, navigation and operation outcome were conflated | Explicit routes/modes, shared draft guards, selected-response fencing, stable uncertain request identities and bounded conflict rebase |
| Network configuration edits leave existing Projects stale | Defaults were copied at startup and saved profiles were reused without a lifecycle update phase | Durable accepted defaults are selected transactionally into creation/resume intent; running placements retain their policy |
| Interrupted network updates risk mixed permissions or lost authority | Engine mutation, saved profile and guest DNS had no coordinated update protocol | Complete conditional paused-binding verification, durable target recovery, atomic effective-profile/revision commit and readiness-gated projection |
| Mutable settings weaken release identity | One digest mixed fixed deployment inputs with network defaults | Fixed compatibility plus an independently checked accepted policy revision; reload is fenced during handoff |

## Original delivery boundaries

Schema 15 requires the existing full quiescent activation gate. Older schema
owners and the retired browser route/schedule contracts are not supported
rollback targets. No live deployment, production configuration, credentials or
outbound policy was changed. Public Internet/NAT connectivity remains a deployment
qualification, and the proposed moderated usability study was not performed.
Arbitrary intervals, one-time tasks, schedule-level completion, notification
preferences and richer execution timestamps remain intentionally deferred.

Raw evidence is retained privately. Public qualification contains synthetic
identities, source/artifact digests and outcomes only. Existing user data and
uncertain-operation evidence remain accounted for throughout migration/recovery.

## Commit sequence

| Stage | Commit |
| --- | --- |
| Review and ordered delivery plan | `adb77f2` |
| Global routes and grant-specific shared selection | `e7b1a61` |
| Schedule preview, filtering and cursor contracts | `afebf37` |
| Visual Scheduled workspace | `7d4483e` |
| Durable network policy reload and lifecycle | `4d6989e` |
| Async navigation and recovery authority | `ea9ec4b` |
| Recovery notices and multibyte form validation | `81f2006` |
| Current binary-preview browser contract | `7d17896` |
