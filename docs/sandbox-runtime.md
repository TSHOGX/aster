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

The Files helper confines all operations to `/workspace`. It walks components
with `O_PATH|O_NOFOLLOW` and verifies each selected object with `fstat`; ordinary
`O_RDONLY|O_DIRECTORY|O_NOFOLLOW` alone is insufficient in the pinned runsc.
Regular content is opened through the selected descriptor. Symlinks, special
files, absolute paths, traversal and private runtime access are rejected.

Read-only access uses a reusable framed helper per Project/placement. Directory
pages freeze a sorted metadata observation with opaque expiring cursors; they
contain at most 200 entries and obey encoded-byte bounds. One scan observes at
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
pause and deletion cancel reader leases; idle helpers exit, and orphan cleanup
requires original-handle termination or placement-loss evidence. List/watch never
activate a Project. An explicit content open retains the existing activation
semantics. A content response interrupted after headers aborts its HTTP body.

Writes still require length/SHA-256, replace atomically after verification and
retain durable command identities/unknown outcomes. Mutation helpers cannot
perform list/read operations; the old per-read mutation path is removed. Preview
ETags do not implement atomic conflict detection for saves.

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
