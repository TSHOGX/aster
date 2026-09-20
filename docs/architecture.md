# Architecture

The product owns sessions, messages, scheduled work, turn intent, authorization,
recovery and durable trace projection. Khora owns sandboxes, process placement,
filesystems, volumes and network enforcement. The adapter uses the pinned public
Khora SDK, never engine internals or host runsc/network/filesystem operations.

| Component | Responsibility |
|---|---|
| `internal/server` | Authentication, authorization, HTTP/SSE DTOs, message/trace projection |
| `internal/execution` | Retained environment lifecycle, fenced operations, recovery and retirement |
| `internal/retainedapp` | Daemon composition, independent bounded reconciliation loops |
| `internal/store` | Product database, short transactions and durable operation/receipt records |
| `internal/bridge`, `internal/agentbridge` | Journaled process protocol, ACK/replay, turn control |
| `internal/sessionfiles`, `internal/workspace` | Workspace policy, engine streams/mutations, inventory, prepared uploads |
| `internal/egress`, `internal/gateway` | Canonical policy, reserved endpoint tuples, grants and gateway contracts |
| `internal/nativecapture`, `internal/trajectory` | Completed native snapshot capture, validation and immutable archive production |

Guest HOME is `/root`. Pi history stays in the retained overlay at `/root/.pi`.
An opaque volume backs `/workspace`; shared assets mount read-only at `/root/.agents`.
A durable bridge journal supports replay across process and product restarts.
Product retirement revokes authority and settles execution while retaining storage.

The only database schema is the current development schema. Native capture receipts
belong to that product database; archives live in an immutable product-owned directory.
There is no Memory/Evolve database, pipeline, review API or automated asset writer.

Authored content — shared assets and agent configuration — is identified by a digest
over its reviewed source tree. `internal/content` transports a revision into an
immutable engine volume: it prepares a keyed staging volume, writes the tree, derives
a keyed snapshot, then verifies by reading the published volume's own bytes back
before committing the pointer. Every stage is recorded before its remote effect, so
an interrupted publication resumes onto the same engine objects. A published volume
is never rewritten, and a launch mounts only a revision this engine store published.

An environment's identity — driver, template, release pin and resource/storage caps —
is compared exactly and cannot change while its storage is retained. Mounts, network, hostnames and resolvers are generation configuration, applied to a retained paused
environment through a fenced configure operation, which is how a session adopts a
newly published revision without becoming a different environment.

A separate model proxy demonstrates the generic next-hop contract. It owns model
restrictions and upstream credentials; application storage contains only network
authority, original request deadlines and teardown receipts. Gateway processes
recover through trusted Unix controls and kernel process-lifetime evidence.
See [gateway contracts](gateway-contract.md) and [recovery](recovery.md).
