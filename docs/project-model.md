# Project product contracts

The `orchestrator/internal/product` application module defines the Project
aggregate and its fresh SQLite schema. Its contract tests exercise this module
directly; they do not qualify the deployed HTTP or sandbox runtime.

A Project owns live resource permission. Agent Sessions and native Terminals
have immutable Project parents and independent revisions. Sessions describe
execution, Terminals describe processes, and Projects describe resource
lifecycle. A browser view is not a persisted resource or a lifecycle owner.

Only the Project owner has live access. Conversation grants are separate read
capabilities: they do not authorize Project events, Files, terminal input, or
Agent actions. Session or Project deletion revokes conversation grants.
Deletion initially records intent; resource cleanup must finish before reporting
a resource deleted. Child deletion does not delete workspace files or change
its Project's lifecycle.

A prompt has an explicit `held` state before admission. Releasing it queues
work, advances Project activity, and invalidates an idle deadline. Every admitted
turn has immutable request identity; replay with different content is rejected.
Requests and scoped projection events commit atomically. Output bytes and file
contents do not belong in the product event outbox.

Mutation commands compare the edited entity's revision. Session changes do not
advance sibling or Project lifecycle revisions. All child operations resolve
the stored Project parent; an arbitrary child ID cannot grant resource access.
Lists and event replay use bounded pages and reauthorize each request.

The database opens only an exact current schema. It rejects foreign schemas and
drift without migration or reset. The writer uses SQLite WAL with FULL
synchronization; actual power-loss guarantees also depend on the storage stack.

Run the contracts with:

```sh
go -C orchestrator test -race ./internal/product
go -C orchestrator vet ./internal/product
```

The Project lifecycle service reserves one active placement per Project before
remote work. It provisions one workspace volume at `/workspace` and one retained
runtime volume at `/runtime`. Child creation leaves these mounts and the Project
network profile unchanged. Only immutable published volumes may be shared mounts.

Provisioning, pause, resume and deletion use durable operation lanes. A worker
performs one remote phase per reconciliation, preserving create keys and original
conditional resource references across lost replies. Uncertain placements remain
charged to capacity. Confirmed pause releases placement capacity; retained storage
remains owned until Project deletion verifies its removal.

Idle uses durable activity and a revision-fenced deadline. Admitted turns,
retained running Terminals, child cleanup and guest-dependent Files/control work
block pause. Held prompts, created Terminal rows and browser connections do not.
Explicit pause rejects busy Projects. New admitted work during an issued pause
remains queued for a later resume. Restart is a cold pause/resume, without process
memory preservation.

Real pinned-Khora tests cover retained bytes, independent Session state paths,
lost successful lifecycle replies and verified sandbox/volume deletion. They also
show that process-group termination does not stop an escaped `setsid` descendant;
such descendants require supervision and directed cleanup. An escaped descendant
can retain stdout, so waiting for output EOF alone cannot establish cleanup.
Application ownership is single-process; lifecycle lane locks are not a claim of
active-active fencing.

## Native Terminals

A Terminal is a Project child with a stable identity, persistent dimensions and a
succession of PTY **incarnations**. It has no conversation, turn, prompt marker or
model access, and its bytes never enter the product event outbox. `terminal_incarnations`
records one row per PTY process with its store/sandbox/epoch/handle, an application
owner fence, requested size, state and exit code.

A Terminal row alone owns no process and pins nothing. A live incarnation always
pins the Project, including while silent and while every browser has detached.
Deletion intent lives in its own column, so `state` stays descriptive: a Terminal
that exited 130 still reports 130 after cleanup, and a deletion request never
overwrites how a child was running.

Closing a browser view performs no database write and does not stop the PTY. An
explicit close requests termination and escalates after a bounded grace period,
because an interactive login shell ignores `SIGTERM`; only the process's own
terminal frame records the exit code. Ctrl-C is the interrupt **byte** written to
the PTY, not a directed signal: the engine signals the handle's whole process
group, so a directed `SIGINT` would end the shell instead of the command being
interrupted. Reopening an ended Terminal creates a new incarnation at the persisted
size and never replays the previous one. A replaced placement records the PTY as
`lost` with an explicit reason and no exit code: the process did not exit, its
outcome simply cannot be observed.

One application attachment drains each live PTY and fans output out to authorized
browsers, which never reach the engine. Each observer has a bounded queue; a
stalled one is disconnected with an explicit gap marker and resyncs rather than
stalling the reader, a sibling, or Agent ingest. A bounded in-memory replay ring
repaints an ordinary reconnect and marks a gap when it cannot. There is no durable
PTY journal, so full terminal history across application restart or placement loss
is not promised. At most one connection holds input; an additional view is
read-only until it explicitly claims a free claim, and unacknowledged input is
never replayed after a reconnect.

## Browser boundary

`internal/producthttp` is the product's browser boundary. It resolves the actor,
enforces origin for every state-changing request and for the WebSocket handshake,
bounds request bodies, and returns projections; it owns no execution or recovery
loop. Terminal output, input, resize and control travel on one WebSocket per
attached Terminal — binary frames are raw PTY bytes, text frames are a small JSON
control protocol — so a terminal flood cannot delay Agent completion or lifecycle
delivery on the SSE projection stream.

`internal/edge` is the single browser origin: it routes the product API, its event
stream and terminal sockets to the application and everything else to the frontend.
It exists because a WebSocket upgrade cannot pass through the frontend's fetch
proxy, and because that proxy stripped `Origin` before forwarding, leaving origin
enforcement where it could no longer see the browser's request. The application
receives the real `Origin` and a forwarded host naming the public origin, while a
client-supplied forwarding chain is discarded.

Terminal contracts are verified against real PTYs in an isolated pinned engine:
concurrent terminals sharing one workspace, raw byte fidelity in both directions
including input split inside a UTF-8 sequence, guest-observed resize, interrupt
sparing the shell, detach past the engine ring rejoining the same process, and
cleanup that waits for termination evidence while sparing siblings. No Chromium
journey renders a live terminal yet.

## Project Files

`internal/product.Files` serves the Project workspace independently of the selected
Agent Session. Files and native processes can read, replace, rename and remove the
same paths concurrently using ordinary Linux behavior. There are no workspace
locks, turn-based upload restrictions or file ownership inferred from messages.

The `workspace-files` Go executable confines operations to `/workspace`. It walks
with directory descriptors, opens components using `O_PATH|O_NOFOLLOW`, and checks
the selected inode before opening regular file content through its descriptor.
All traversed symlinks and nonregular read targets are refused. Removing a symlink
unlinks the entry itself; removing a directory requires it to be empty. Absolute,
traversing, malformed and excessively deep paths are refused.

Uploads stream through one helper with a declared length and SHA-256 digest,
limited to 256 MiB. Only complete, verified bytes replace the target through a
same-directory rename. Cancellation cannot publish a partial replacement; an
abruptly killed helper can leave a removable `.upload-*` temporary file. Transfers
use bounded buffers, and directory pages contain at most 200 observations. Neither
file bytes nor terminal bytes enter the Agent journal or product event outbox.

Mutation IDs identify immutable requests. A duplicate returns its existing outcome
without dispatching again, even if a sibling has since changed the file. Lost
replies report `unknown`; recovery terminates or observes the original helper and
releases its activity without replaying the mutation. A known process exit and a
known mutation outcome are separate facts. Confirmed helper errors report failure;
a rename whose durability confirmation failed reports an unknown outcome.

Explicit reads and writes request normal Project resume when paused and return
pending until placement is ready. Directory listing and watch observation never
resume a Project. Files watches invalidate browser inventory; gaps, disconnects
and resets require bounded rescans and do not represent a transactional snapshot
or a complete write audit. Watches hold no lifecycle activity.

The Go API uses Project permission, origin checks, bounded bodies and authenticated
operation lookup. Reads/downloads stream raw bytes; uploads require `Content-Length`,
`X-Content-SHA256` and `Idempotency-Key`. Mutations and operation outcomes are rooted
at `/api/projects/{project}/files`. Browser integration and the composed deployment
are qualified separately from these application contracts.

The isolated pinned-engine gate runs confinement tests inside runsc, including
adversarial symlink swaps. It also verifies an 8 MiB transfer alongside a native
PTY, cancellation retaining prior bytes, a lost write reply followed by a sibling
overwrite, watch invalidation, and retained bytes across cold pause/resume.

## Schedules and adjacent capabilities

Schedules belong to a Project and choose an existing Agent Session or create one
Session per run in that Project. Manual and cron runs use the foreground turn
admission transaction. Repeating a manual request ID or delivering the same cron
occurrence twice observes one durable run and one turn. Run history reports the
turn's actual state; schedule completion never pauses a sandbox independently.

Cron uses five fields and an explicit IANA timezone. An empty expression is
manual-only. Missed occurrences coalesce into one run and advance beyond the
current time. Disabling a schedule stops new admissions. Deleting a Project or
target Session disables its schedules atomically. Already admitted runs retain
ordinary Session controls and cleanup. Enabled schedules can wake a paused Project
through normal admission; pausing a Project does not disable schedules.

Authenticated conversation readers use separate read projections. Their grants
authorize conversation messages and search results, without native records,
Project events, live Files, terminal input or Agent commands. Subscriptions
reauthorize and close after revocation. Search results include Project, Session
and message identities so navigation retains the correct parent context.

Public shares freeze a conversation frontier and up to 16 explicitly selected
files, limited to 16 MiB per file and 64 MiB total. Files stream to a separate
host-owned directory for each share before publication. Later workspace writes
and messages do not change that published selection. The selection is an
observation, not an atomic workspace snapshot. Public projections exclude native
records and runtime identities. Tokens are random, stored by digest, expiring,
revocable and rotatable. Lost creation responses can be recovered by listing the
owner's shares and rotating a published token.

Session and Project deletion revoke all their shares immediately. Cleanup removes
owned frozen bytes; public snapshots do not outlive parent deletion indefinitely.
Interrupted pending publications are failed and reclaimed after owner restart,
never published from partial selections. Active downloads recheck authorization.

An unnamed Session receives a bounded title from its first admitted prompt.
Optional title generation is host-only, and a user's explicit rename wins over
any delayed result. Notifications retain their audience and independent per-user
read markers. Turn outcomes and conversation grants produce directed notices;
operators can insert broadcast notices through an internal application port.
Neither titles nor notifications retain Project runtime activity.
