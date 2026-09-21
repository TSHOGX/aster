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
