# Navigation, scheduling and network implementation

The current implementation uses one canonical schema and host protocol contract.
Operator-registered immutable runtime versions may coexist under that contract;
Projects retain exact releases, mounts and artifacts. Format/protocol changes
require an explicitly authorized fresh installation. There is no migration chain
or old-client adapter.

## Delivered behavior

- Global routes normalize selection independently of Project workspace preferences.
  Received conversations require an active reader grant. Navigation protects drafts
  and fences late responses.
- The Scheduled workspace provides owner-authorized filtering, bounded lists and
  history, live invalidation, complete prompt editing, shared mutation receipts and
  explicit revision conflicts.
- One-shot, Cron, elapsed minute intervals and manual tasks use one admission path.
  One-shot consumption is transactional. Definition revisions change only on edits;
  scheduler progress does not invalidate them. Unknown execution is not retried.
- Measured native Pi tools manage the same tasks through the reserved gateway route
  and protected owner authority. Persisted cards, source links and Inbox delivery
  connect conversation intent to its actual result.
- Bounded interval checks stop on count, deadline or a host-bound finish operation.
  Pending checks can remain quiet while failures and unresolved outcomes stay visible.
- Direct IPv4 and DNS defaults are durable policy snapshots selected at creation or
  resume. Conditional engine updates and DNS projection precede guest admission;
  running Projects retain their effective policy.

See [conversational scheduling acceptance](conversational-scheduling-implementation.md)
for authority, lifecycle and qualification details, and [configuration](configuration.md)
for database network policy boundaries. [Delivery](delivery.md) defines candidate checks and
[bootstrap](bootstrap.md) defines the scoped reset procedure. The latest installed
artifacts and checks are recorded in [fresh deployment](fresh-deployment.md).
