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
| `internal/egress`, `internal/gateway` | Canonical policy, reserved service tuples, grants and gateway contracts |
| `internal/nativecapture`, `internal/trajectory` | Completed native snapshot capture, validation and immutable archive production |

Guest HOME is `/root`. Pi history stays in the retained overlay at `/root/.pi`.
An opaque volume backs `/workspace`; shared assets mount read-only at `/root/.agents`.
A durable bridge journal supports replay across process and product restarts.
Product retirement revokes authority and settles execution while retaining storage.

The only database schema is the current development schema. Native capture receipts
belong to that product database; archives live in an immutable product-owned directory.
There is no Memory/Evolve database, pipeline, review API or automated asset writer.

Launch plans currently consume explicitly supplied immutable config/asset volume
references. Durable publication and complete external protocol gateway composition
remain unimplemented. See [development scope](development-scope.md).
