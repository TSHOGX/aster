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
