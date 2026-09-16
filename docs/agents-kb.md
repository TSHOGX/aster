# Optional memory and knowledge subsystem

This subsystem has its own data and job lifecycle. It is integrated with the core
runtime through trajectory ingestion and read-only KB mounts; it is not required
for ordinary session/turn execution. A missing extraction LLM disables extraction,
not the workbench or core scheduler.

## Ownership boundaries

| Part | Location | Ownership |
|---|---|---|
| Control history adapter | `internal/controlsource` | Reads control-plane state for ingestion |
| Native trajectory capture | `internal/trajectory` | Archives/parses driver history |
| Memory storage | `internal/memorystore` | Owns the separate memory database |
| Extraction and curation | `internal/memory` | Optional LLM/agentic pipeline and snapshot generation |
| Knowledge mount | `internal/runtime` | Pins a read-only snapshot for each generation |
| KB review HTTP/CLI | `internal/server`, `cmd/orchestrator` | Review/approval of staged knowledge |
| External evolution/evaluation | Separately deployed project | Its runner, evaluation policy, jobs and state are outside the core release |
| External integration UI | `/evolution`, `/api/evolution/*` | Reads configured reports and starts the external runner |

Do not treat the external project as source included in this repository, or
interpret its report format and job schedule as the core runtime's roadmap.

## Data flow and persistence

Native Pi JSONL is captured from driver homes into the trajectory archive. Capture
records snapshots, events and source pointers in `memory.db`. When native traces
are unavailable, control history can supply a lower-fidelity projection. Capture
is fingerprinted to avoid rebuilding unchanged sessions.

The memory database also stores episode summaries, semantic memories, skill
candidates, extraction state, evolution runs and agent-call records. Extracted
knowledge and human review are not disposable caches. Retain them with the KB and
native archives in a consistent backup. Cross-file references to control sessions
are application relationships, not enforced SQLite foreign keys.

Facts and skills require distinct evidence sessions before promotion. The scalar
curator combines evidence counts with support, confidence and contradiction
checks; the agentic graph adds consolidation, verification, curation and critique.
Exact thresholds live in `internal/memory/curator.go` and associated tests.

## Snapshot contract

```text
agents-kb/
  current -> snapshots/<digest>/
  pending -> snapshots/<digest>/
  pending.json
  snapshots/<digest>/
  reports/
  approvals.log
```

The runtime resolves `current` to a concrete snapshot directory when building the
sandbox spec. Running generations remain pinned; new generations see a newly
approved snapshot without rebuilding the rootfs. Never edit a published snapshot
in place.

The generic image contains only a bootstrap constitution and portable examples.
Domain corpora, business schemas, credentials and private skills are supplied by
each deployment outside the public repository. Keep the snapshot's INDEX aligned
with the skills actually provisioned there.

Publication is staged by default (`auto_publish: false`). Explicit approval changes
`current`; rejection preserves the staged snapshot as evidence. Per-entry review
can retain approved facts/files and demote rejected facts. `auto_publish: true`
is an explicit opt-in to unreviewed publication, not the default guarantee.

```bash
orchestrator kb status --config /private/harness.yaml
orchestrator kb diff --config /private/harness.yaml
orchestrator kb approve <digest> --config /private/harness.yaml
orchestrator kb reject <digest> --reason "review decision" --config /private/harness.yaml
```

Approve/reject require the pending digest to prevent stale decisions. The ordinary
KB review routes and the external evolution allowlist are separate authorization
surfaces; do not assume the `evolution_admin` flag governs every endpoint.

## Job ownership: choose one scheduling authority

The internal pipeline runs an initial sweep when enabled, then periodically
(default interval one hour). The term "nightly" is an operational convention,
not a guaranteed once-per-day service timer.

Alternatively, invoke explicit CLI jobs from a target-owned timer or scheduler:

```bash
orchestrator backfill --config /private/harness.yaml
orchestrator replay --date YYYY-MM-DD --config /private/harness.yaml
```

`backfill` captures history; `replay` runs the configured extraction pipeline for
a date window. These commands may write memory/KB state and call external models.
Do not run them as part of a routine schema migration or restore check.

Use either the internal timer or a coordinated external job schedule, not
independent overlapping writers. Deployment drain/hold blocks new internal jobs;
external workers and timers must also be inventoried and stopped for backups.
`scripts/harness-kb-nightly.sh` is an optional generic wrapper requiring explicit
binary, config, environment and lock paths. It installs no cron entry.

## External evolution integration

The external interface uses these server-side frontend settings:

- `EVOLUTION_PROJECT_ROOT`: separate runner project, including its runner script.
- `EVOLUTION_STATE_ROOT`: private report/run/KB state for that project.
- `EVOLUTION_MEMORY_DB`: explicit shared or dedicated memory database path.
- `EVOLUTION_BUN`: runner executable.
- `EVOLUTION_VIEWERS`: comma-separated authenticated user allowlist (`*` is an
  explicit broad-access choice).

If the integration is not configured, its API reports unavailable. The public UI
contains no site-specific SQL recipes or fabricated production reports. Configure
all dependencies and validate data ownership before enabling job execution.

The route currently launches a detached external process and tracks file-backed
run records. Operations must account for this process when stopping writers; it
is not automatically equivalent to a core `schedule_run` or a managed sandbox.
This boundary and its limitations are documented here without claiming the
external project has been bundled or standardized.

## Sharing and privacy

Extraction currently aggregates the configured users' trajectories into a shared
KB. Review for sensitive content before approval. Prompts and filters are not a
substitute for explicit review, scoped input selection and private data storage.
Memory evaluation artifacts and deployment reports must never enter public CI
logs, release attachments or checked-in examples.

Integration follow-up is tracked separately in [issue #10](https://github.com/TSHOGX/agent-runtime-platform/issues/10); this document describes only existing behavior.
