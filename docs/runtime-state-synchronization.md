# Runtime state synchronization

The browser treats `session.runtime` and GET `/sessions/:id/runtime` as the
same versioned projection. `revision` must cover **all** of its dependencies:
turns, session status, active generation, generation status and resource state.
The database column retains its historical name `turn_revision`; it is now the
revision of the entire runtime projection. SQLite triggers enforce this for
normal writers, recovery, checkpointing and idle release alike. No-op lifecycle
updates do not advance it, and unrelated generations do not invalidate a session.
Existing databases receive the triggers during startup migration.

Previously only turn changes advanced the revision. Idle release could change
`live` to `released` without changing any turn, so the frontend correctly rejected
the refreshed snapshot as a duplicate. The session list retained its old sandbox
field and displayed Idle until a page reload. Accepting equal revisions would
hide this bug but let reordered requests overwrite newer state.

The runtime endpoint reads its revision, lifecycle and turns in one read
transaction. Snapshots produced inside a write transaction continue to use that
transaction. Clients accept strictly newer revisions, regardless of whether the
snapshot arrived through HTTP or SSE.

SSE remains the immediate update path. Initial connection, reconnect and replay
gaps reload the selected conversation, artifacts and runtime even if already
cached. Opening a cached conversation always refreshes its runtime. Visible tabs
also reconcile the session list and selected runtime every 15 seconds, including
idle sessions, and when visibility/network connectivity returns. Hidden tabs
skip polling, and periodic requests do not overlap. This bounds stale lifecycle
state when no event arrives without polling message histories on every tick.

Session-list responses have no server revision. Their merge therefore preserves
rows changed, created or removed locally since the request began; an old response
cannot overwrite a runtime push delivered during that request. Transient full
refresh failures preserve the last successful list.

Regression coverage includes sandbox-only revision changes, idle release,
no-op revision stability, runtime migration tests and stale session-list merges.
This protocol does not give message/artifact lists a revision or make shared
reader conversations fully live; those still rely on their existing event/access
contracts and detail refreshes.
