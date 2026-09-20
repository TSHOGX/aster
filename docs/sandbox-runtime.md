# Khora execution

Khora is independently deployed and accessed only through public APIs. The product
persists its durable store identity, sandbox ID, placement epoch, mutation revision
and process handle. Every operation is fenced by the product owner and environment
state. Ambiguous writes remain unresolved until exact observable postconditions prove
the outcome; a transport error is never evidence of writer termination.

Pause releases processes while retaining overlay and mounted volumes. Resume launches
new processes and restores native Pi history. There are no process-memory checkpoints,
host runtime bundles, direct network setup or filesystem fallbacks in the product.

The guest launcher validates the pinned template components, prepares read-only config,
and starts the journaled process bridge. The bridge negotiates identity before replay,
commits durable source records before sending them, and advances acknowledgments only
after product persistence. Historical recovery explicitly names the original execution.

See [session lifecycle](session-lifecycle.md) and [gateway contract](gateway-contract.md).
