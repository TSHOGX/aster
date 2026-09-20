# Control plane

Sessions and messages are product identity. Replaceable generations bind to retained
Khora environments through opaque execution records. Durable operations record intent
before remote I/O and settle results under owner, epoch and environment fences.

The only bridge store is the accepted-execution path. Turn claim, dispatch, heartbeat,
completion and control outcomes validate authority; network grants are scoped to the
accepted generation and turn. The process journal feeds durable ingress and idempotent
message/trace projection before publication to HTTP/SSE subscribers.

Independent bounded workers reconcile engine events, executions, workspace inventory,
prepared content, product housekeeping and retirement. Native capture is a separate
producer command reading committed snapshots through Khora and recording product
receipts. No downstream memory consumer is part of this architecture.

Background discovery covers incomplete preparation, pending remote operations,
starting bindings, active/idle adoption, interrupted journals, controls, gateway
drains, capture and retained retirement. Immutable producer records remain separate
from public events derived from product state, so interruption classification never
rewrites journal evidence. See [recovery](recovery.md) and the
[gateway contract](gateway-contract.md).
