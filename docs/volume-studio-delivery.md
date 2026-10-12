# Volume Studio delivery tracker

This tracks implementation and verification of independent authoring at
`/volumes-studio/`. A completed stage does not imply that later release gates
passed. The existing application remains the sole account authority. Source
publication and locally registered runtime artifacts have separate lifecycles.

## Stages

| Stage | Status | Deliverable / exit evidence |
| --- | --- | --- |
| 1. Portable content contract | Implemented | Independent Go module; strict producer/consumer decoders; canonical Go/Node vectors; path, identity, digest and profile boundary tests |
| 2. Shared identity and independent shell | In progress | Central code/PKCE exchange, private introspection/revocation, durable Studio sessions, exact-prefix gateways and cookie filtering implemented; real cross-application browser qualification remains |
| 3. Durable authoring | In progress | SQLite WAL/FULL metadata, immutable blobs, revision/receipt mutations, atomic tree operations, restore, quotas and GC interlocks implemented; resumable uploads remain |
| 4. Publication and distribution | In progress | Frozen versions and durable integrity validation with principal checks implemented; authenticated export grants, source retirement and backup remain |
| 5. Authoring interface | Pending | Library/tree/editor, uploads, conflicts, history/diff, restore and publish review/progress |
| 6. Consumer import and engine adaptation | In progress | Trusted immutable identity admission, durable local transfer cache, verified checkpoints, restart reconciliation and quarantine implemented; network acquisition, engine materialization, quota admission and local registration remain |
| 7. Admin and runtime lifecycle | Pending | Release picker, Environment integration, retained Project bindings, reference-aware retirement and descendant cleanup |
| 8. Operational qualification and documentation | Pending | Independent release, restore, browser/engine/fault/identity/maximum-size gates and complete operations guidance |

## Ownership and invariants

- Studio has its own process, configuration, database, blob storage and release.
  An independent front door preserves the fixed prefix and filters cookies.
- Identity flows through a narrow central adapter; no shared database or signing
  key lets Studio mint Aster sessions. Authority expires within a bounded window.
- Draft mutations use expected revisions and durable receipts. Blobs become durable
  before metadata references them. Publication freezes an exact revision.
- Aster accepts only operator-configured sources, verifies the exact descriptor,
  and acquires all bytes locally before engine materialization. Ready runtime
  artifacts have no Studio availability dependency.
- Runtime publication retains staging → verification → keyed snapshot → derived
  verification. Uncertain writes retain quarantined allocations and accounting.
- Source deletion does not delete copied runtime content. Runtime cleanup honors
  saved Environment/Project references and observed engine descendants.
- Historical application adapters and superseded code are removed as replacement
  stages land. Aster's schema remains an exact-current contract. Studio data
  retention and its independent backup/upgrade procedures are a separate boundary.

## Outstanding release gates

Real shared login/logout, callback attacks, identity outages, prefix/deep-link
routing, cookie filtering, uploaded active-content isolation, durable upload
resume, stale revision/target conflicts, freeze isolation, GC/export/backup races,
service revocation, import restart and source outages, uncertain engine outcomes,
aggregate reservation exhaustion, saved Project pinning, owned-copy edits,
descendant-aware cleanup and restore remain required.

The target envelope is 256 MiB/file, 1 GiB/tree and 10,000 entries. Manifest
validation tests alone do not qualify storage, engine throughput or memory.
Real scale gates must measure bounded buffers with concurrent large uploads and
imports, plus 10,000-entry freeze under representative SQLite contention.
