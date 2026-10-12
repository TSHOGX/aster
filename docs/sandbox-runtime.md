# Files and native runtime

The released image contains pinned sandboxd, `agent-runner`, `workspace-files`,
`runtime-cleanup`, `runtime-verify`, Node and native Pi. Python and scientific
packages are workload tools. Application launch, journal replay, Files and
cleanup protocols are implemented in Go.

Pi runs in RPC mode with a stable native Session file under
`/runtime/sessions/<session>/native`. Each Session initially selects models and
settings from the Project's immutable `/opt/agent-config/pi` mount. Its original
provider catalog survives later runner incarnations. The application injects an
incarnation service grant where a configured provider expects a service token;
upstream credentials never enter that catalog.

The builtin system context is `/usr/local/share/project-runtime/system.md`.
Reviewed `/opt/shared-assets/AGENTS.md` and optional
`/opt/agent-config/pi/APPEND_SYSTEM.md` append context. Explicit skills come from
`/opt/shared-assets/skills`. Automatic extensions, skills, prompt templates and
themes are disabled; the explicit reviewed skills still load through Pi's native
CLI. Context directories are logical organization, not isolation between sibling
Project processes.

When compute is active, the Files helper confines operations to `/workspace`. It walks components
with `O_PATH|O_NOFOLLOW` and verifies each selected object with `fstat`; ordinary
`O_RDONLY|O_DIRECTORY|O_NOFOLLOW` alone is insufficient in the pinned runsc.
Regular content is opened through the selected descriptor. Symlinks, special
files, absolute paths, traversal and private runtime access are rejected.

Active read-only access uses a reusable framed helper per Project/placement. Directory
pages freeze a sorted metadata observation with opaque expiring forward and
backward cursors. Name seeks return a bounded window from one server inventory.
Pages contain at most 200 entries and obey encoded-byte bounds. One scan observes at
most 100,000 names; snapshots are not atomic filesystem snapshots. Reads reject
symlink/special-file substitutions using the same descriptor traversal as writes.

Each content open captures at most 256 MiB into unnamed host temporary inodes under the application state directory,
verifies its size and SHA-256, then exposes a read-only representation. Strong
ETags, conditional responses and single byte ranges refer to those exact bytes.
Even matching metadata triggers a fresh capture before returning 304. The host
bounds captures to 512 MiB, sixteen active requests/readers, two requests per
Project and one directory scan per Project. Cancellation has request identity;
credit flow control prevents a slow consumer from overflowing another request.

Pure reads create no mutation or activity rows. Schema 5 retains only each
Project's current helper identity for cleanup after owner loss. Placement changes,
pause and deletion cancel guest reader leases; idle helpers exit, and orphan cleanup
requires original-handle termination or placement-loss evidence. Browsing and
content opens never activate a Project. A content response interrupted after
headers aborts its HTTP body.

Paused Projects use the saved workspace volume through Khora's authenticated
volume APIs. They can list, preview, download, upload, edit, rename, create folders
and remove entries without reserving CPU or starting a guest. A new Project can
create just its workspace volume before its first Agent/Terminal. Saved profiles
and volume creation keys survive owner loss; first compute startup attaches that
same workspace and materializes the remaining saved owned slots before creating
the placement. Full-plan reservations are retained between these steps; changing
caller defaults cannot rebind a saved slot.

Direct access holds the Project's existing lifecycle execution lane while
checking paths and performing volume I/O. Every path component is checked for
symlinks and special files while the guest remains stopped. Resume and deletion
cannot execute midway through that sequence. Completed unnamed captures release
the lane before HTTP delivery, so a slow download does not prevent resume.
Paused directory snapshots retain the same sorted, expiring pagination contract.
Conversation projections remain available directly from application storage.

Khora permits direct writes with no live writable placement, and rejects them
while a guest holds the volume writable to avoid incoherent gofer caches. Active
mutations therefore retain the guest helper. Paused uploads verify length and
SHA-256 in unnamed host staging, import to a unique recorded temporary path, and
rename into place only after a complete import. An explicit quota failure reclaims staging and leaves the destination intact;
a partial import never replaces the destination. The schema records volume authority separately from process
authority. Lost direct-operation replies remain unknown and are not replayed;
unknown volume mutations block further mutations and resume pending operator
recovery. Guest-process exit alone cannot settle them. Local file mutations
publish durable invalidation hints even when no guest watch exists.

Writes still require length/SHA-256, replace atomically after verification and
retain durable command identities/unknown outcomes. Mutation helpers cannot
perform list/read operations; the old per-read mutation path is removed. Preview
ETags do not implement atomic conflict detection for saves.

Native journals use format 2, created directly with their ACK-compaction indexes
in one transaction. Older journal formats are retired by destructive reset.
Versioned empty
databases and journals missing required indexes are refused without recreating
schema or rewriting command evidence. Inspect the original state rather than
resetting an unresolved journal or replaying its commands. This guest change
requires a newly assembled immutable image.

Native journal records and user-visible projections are separate. A native
`agent_end` can precede retry or compaction and does not complete an application
command. The pinned RPC extension reports a correlated `prompt_end` with the final
completed/failed/canceled outcome after the whole native prompt settles. Only then
does the Go guardian settle descendants and create a synced immutable capture,
before the next prompt can replace its source. Host exports retain native history for inspection and recovery; rendered
conversation history does not substitute for the native Session.


Retry events and post-run compaction remain attached to the original command and
turn. The guardian retains its busy reservation through that work; another prompt
cannot enter during retry backoff. Stop during backoff or compaction settles as
canceled without dispatching a canceled preflight prompt. The settlement event
uses reserved journal capacity. Invalid, foreign or unapplied settlement events
fail closed; there is no timeout or `get_state.isStreaming` inference of completion.


Live host ingestion commits adjacent records from the same incarnation in bounded
batches: at most sixteen records/256 KiB, with one larger legal frame alone and a
five-millisecond collection bound. The decoder has its own sixteen-record/256 KiB
credit limit. Control/ownership boundaries preserve order. Raw identities, command
outcomes and watermarks commit atomically; a failed batch returns no ACK. This
amortizes durable transaction cost without dropping native evidence or weakening
FULL durability, and does not change the guest wire or journal format.


The pinned engine normalizes whitespace at path boundaries. Root file names with
leading/trailing spaces are preserved by the adapter; ambiguous parent-directory
components with leading/trailing whitespace are rejected before engine I/O.
Ordinary spaces inside directory/file names remain supported.

## Native scheduling tools

Runtime manifest version 4 requires schedule protocol 1 and measures
`/usr/local/share/project-runtime/platform-schedules.mjs` as a data asset. The
release builder installs the embedded reviewed source, and generation and guest
verification compare its bytes with that source. A missing, changed or unsupported
bundle fails qualification; older manifests are refused by this host.
The extension is not an executable binary and needs no execute permission.

For a Project whose current manifest qualifies and whose declared Agent audience
has `schedule_tools: true`, the owner verifies gateway platform protocol support
before providing the runner a platform endpoint. The endpoint uses the Project's
immutable synthetic service destination, port and configured TLS mode. No gateway
or failed qualification means no scheduling tool advertisement; browser scheduling
remains available. Existing service bindings are not rewritten.

The runner writes a mode-0600 scheduling configuration in the logical Session's
runtime directory, using its incarnation service credential, then launches Pi
with an explicit `--extension` path. Automatic extension/skill/template/theme
discovery stays disabled. Credentials travel through the start environment and
private configuration, never native commands, tool arguments, receipts or logs.
The Project remains the Linux trust domain; logical Session files do not isolate
hostile sibling guest-root processes.

Native tools are `schedules_create`, `schedules_list`, `schedules_get`,
`schedules_update`, and `schedules_cancel`. A mutation identity hashes the logical
Session, native Session, persisted assistant-entry ID and provider tool-call ID;
all are captured before awaiting network activity. Relative one-shot delays use
one server clock anchor. Bounded retries preserve identical request bytes and the
expected execution Run. A missing response after sending is an unknown outcome,
with an instruction to inspect saved tasks before recreating intent. Aborting
a tool stops retries without retracting a committed task. Native error results
use bounded messages; successful mutation results retain structured task receipts
and omit full prompts from their presentation metadata.

Qualification uses the actual pinned Pi loader and AgentSession with deterministic
model streams. It verifies explicit loading with discovery disabled, structured
native events, lost-response retries, provider-ID reuse, parallel invocation
identity and cancellation uncertainty. No provider credentials are required.

`schedules_create`/`update` accept a `check` option with a mandatory deadline,
optional first check time, finite count (default 12, maximum 100), and notification
policy (default `on_finish`). Use this with an interval trigger to poll uncertain
events. The Agent performs the external check itself; there is no condition DSL,
workflow engine, event subscription, or recursive task tree.

`schedules_finish` takes no model-authored identity. Context supplies the sole
scheduled input's schedule/revision and current execution Run. The host checks
that attribution again in the receipt transaction. Initial Prompts and consumed
Steers both count toward authority: any scheduled input removes create/update/
cancel/list authority, and a mixed Run cannot finish an arbitrarily chosen task.
A scheduled Run can inspect its own associated task; only an unambiguous check
can finish. Pause/cancel/edits fence late finish. Automatic count/deadline exhaustion
preserves the admitted definition revision so its live final check may still
finish. Finish stops admission, records the execution Run, and awaits its real
outcome for delivery.

Slot initialization uses the public idempotent Volume snapshot operation for a
built-in source, then the same bounded content verifier used by publication. A
created but unverified copy is allocated and inaccessible. Retrying verification
uses that isolated copy; ready user data is never reseeded or compared to its
original source. The same slot projection supplies sandbox create, full mount
observation, network transition and cleanup. The `slots` isolated runtime suite
measures eight declared mounts, native-copy independence, read-only owned/shared
access, owned-byte accounting and cleanup without deleting the shared source.
