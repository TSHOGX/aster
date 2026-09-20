# Control plane

Sessions and messages are product identity. Replaceable generations bind to retained
Khora environments through opaque execution records. Durable operations record intent
before remote I/O and settle results under owner, epoch and environment fences.

The only bridge store is the accepted-execution path. Turn claim, dispatch, heartbeat,
completion and control outcomes validate authority; service grants are scoped to the
accepted generation and turn. The process journal feeds durable ingress and idempotent
message/trace projection before publication to HTTP/SSE subscribers.

Independent bounded workers reconcile engine events, executions, workspace inventory,
prepared content, product housekeeping and retirement. Native capture is a separate
producer command reading committed snapshots through Khora and recording product
receipts. No downstream memory consumer is part of this architecture.

The current implementation has tested recovery paths for idle/active adoption,
interrupted journals, workspace writes and retained retirement. Comprehensive unresolved
start/control and unexpected-epoch cases remain development work; static launch plans
also do not establish complete external egress composition.
