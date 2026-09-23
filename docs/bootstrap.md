# First deployment and ungated-owner bootstrap

Fresh installations can start with the current release gate. Replacing an already
running ungated owner is not qualified by the ordinary drain or rollback commands.
Do not deploy over that process on the strength of an idle status sample, a closed
browser tab or an HTTP maintenance page.

## Current decision

The inspected ungated baseline has no global admission-control endpoint. Its
scheduler, queued command dispatcher, recovery, title generation, capture and
cleanup workers run independently of browser ingress. Existing Terminal WebSockets
can still send input after a proxy starts refusing new HTTP requests. Its operator
status does not account for every in-flight host operation, and its gateway shares
the owner process lifetime.

Consequently this repository does not claim a generic safe live bootstrap for that
baseline. That deployment remains pending. The release coordinator is qualified
for owners that already implement its fence. Current owner handoff and rollback
evidence cannot retroactively establish a fence in an older process.

## Required external maintenance boundary

Before an ungated replacement can be qualified, a deployment-specific mechanism
must establish all of the following and keep them enforced through replacement:

- Block new user and automation execution requests at every reachable ingress,
  including private API/control clients and already-open Terminal input channels.
- Fence scheduler admission, release-held and queued dispatch, recovery that starts
  new work, title generation and other internal sources independently of browser
  traffic. Disabling one schedule or waiting between ticks is insufficient.
- Allow already accepted work, Stop, ingestion and required cleanup to settle,
  without killing active model streams or declaring unknown outcomes idle.
- Observe durable blockers and all in-flight host operations under the same fence,
  and prove that new work cannot cross the boundary after readiness is checked.
- Keep maintenance closed after coordinator/source crash. Abort before replacement
  on timeout; do not reopen automatically after an uncertain activation.
- Validate the replacement's schema/runtime compatibility and preserve accepted
  writes. Never restore a pre-upgrade database or volume snapshot over newer work.

These are qualification requirements, not a checklist that turns an observation
into authorization. An external proxy alone cannot satisfy the internal-source
items. Live DDL injection or a second writer bypassing the Store lifetime lock is
not a supported way to retrofit this mechanism.

## Deployment outcomes

For a fresh installation, use a new state directory and verified immutable runtime,
start the owner, then its independently supervised gateway. Do not point the new
installation at live legacy resources or treat a new empty database as migration
of accepted history.

For an owner that already supports the gate, use the [release runbook](operations.md)
and an exact [qualified compatibility combination](compatibility.md). For an
ungated owner, keep deployment pending until the external boundary above has been
implemented and rehearsed on a disposable copy of that deployment. No live bootstrap
or legacy resource migration is implied by a successful local build/test run.
