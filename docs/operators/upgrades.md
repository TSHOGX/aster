# Upgrade services

[Documentation](../README.md) · Deployment and operations

Prepare and inspect the exact replacement artifacts before stopping admission.
These procedures preserve state only within the supported current deployment
contract. Read [compatibility](../reference/compatibility.md) first.

## Choose the procedure

| Change | Procedure |
| --- | --- |
| Application owner executable only, matching compatibility and transferable work | Owner handoff; retain the existing gateway |
| Frontend, gateway or supported host-configuration change | Full quiescent update |
| Additional compatible immutable runtime/content version | [Register/publish inventory](runtime-inventory.md), then select it in Resource Center |
| Engine-store change or unsupported schema/runtime protocol | Fresh installation with an explicitly scoped reset; no automatic migration |
| Studio release | Its independent [backup and upgrade procedure](../../volumes-studio/deploy/README.md#upgrade-studio) |

Changing an environment default does not replace the runtime of existing Projects.
Do not use an owner handoff to bypass a schema, gateway or runtime incompatibility.

## Stage and inspect

Preserve a trusted candidate inventory and runtime/engine identities. Do not rebuild
artifacts at activation time. From the matching source tooling, verify and inspect:

```sh
python3 scripts/candidate.py verify /srv/candidates/aster-new
python3 scripts/candidate.py inspect /srv/candidates/aster-new \
  --config /etc/agent-workbench/application.json
```

Alternatively, inspect the verified replacement executable directly:

```sh
/srv/candidates/aster-new/bin/orchestrator release-identity \
  --config /etc/agent-workbench/application.json
```

Inspection does not open the application database or contact the engine/provider.
Record the target executable digest (`sha256:...`) and compatibility digest privately.
The executable digest is distinct from the guest runtime manifest digest. Matching
compatibility is a prerequisite, not proof of arbitrary binary compatibility.

Arrange supervisors so a retired process does not automatically restart with old
bytes. Preserve a consistent [backup](recovery.md#back-up-the-installation) and a
maintenance window appropriate to the change.

## Full quiescent update

Use a unique `UPDATE_ID` for each attempt. Replace `GENERATION` with the value
returned by drain, and `EXECUTABLE_DIGEST` with the inspected replacement hash.

1. Close admission:

   ```sh
   orchestrator drain --socket /var/lib/agent-workbench/control.sock \
     --owner UPDATE_ID --timeout-seconds 120
   orchestrator drain-status --socket /var/lib/agent-workbench/control.sock
   ```

   New execution is refused while admitted work settles. Held prompts remain held;
   stop, observation and cleanup can continue. A timeout reopens admission before
   activation; it does not terminate work.
2. Inspect blockers and wait for a fresh ready checkpoint:

   ```sh
   orchestrator drain-ready --socket /var/lib/agent-workbench/control.sock \
     --owner UPDATE_ID --generation GENERATION
   ```

   Ready can be revoked if new cleanup or unresolved evidence appears. A status
   counter or prior ready result is not authorization for an unchecked shutdown.
3. Activate the selected executable:

   ```sh
   orchestrator drain-activate --socket /var/lib/agent-workbench/control.sock \
     --owner UPDATE_ID --generation GENERATION --target sha256:EXECUTABLE_DIGEST
   ```

   Activation rechecks readiness, records the target and causes the owner to exit.
   Wait for that exit, then start the verified replacement using the same state,
   engine bindings and reviewed configuration. Replace frontends under their
   supervisors if included in the update. For a gateway change, follow the
   gateway sequence below while the quiescent owner is still available.
4. On the replacement, inspect health and complete:

   ```sh
   orchestrator drain-complete --socket /var/lib/agent-workbench/control.sock \
     --owner UPDATE_ID --generation GENERATION
   ```

   Completion requires the selected executable and settled blockers, then reopens
   admission. Verify login, file access, a native reply and changed functionality.
   Remove only disposable smoke-check resources.

Before activation, abort an unsuitable attempt with:

```sh
orchestrator drain-abort --socket /var/lib/agent-workbench/control.sock \
  --owner UPDATE_ID --generation GENERATION
```

After activation, the gate does not reopen on timeout. Only the selected executable
can start and complete the handoff. If it fails, repair its configuration/dependencies
while admission remains closed. There is no rollback-candidate override. Do not
clear database rows or start an unselected old binary to bypass the fence.

## Owner-only handoff

This mode can retain eligible running Agent work across an owner replacement.
The target and running owner must have matching compatibility digests, and the
existing gateway must remain available with the same registration and capacity.
Configuration edits are fenced during the handoff.

Start with the digests from candidate inspection:

```sh
orchestrator drain --socket /var/lib/agent-workbench/control.sock \
  --owner UPDATE_ID --mode owner --target sha256:EXECUTABLE_DIGEST \
  --compatibility sha256:COMPATIBILITY_DIGEST --timeout-seconds 120
```

Then follow ready, activate, wait-for-exit, replacement startup and complete as
above. `handoff_blockers` identifies work that cannot transfer; `durable_blockers`
can also include retained running work. Starting/stopping/unknown work, uncertain
sends, writes, publication and cleanup can still block handoff.

Retaining a gateway does not guarantee uninterrupted provider streams. New requests
fail during authority outages; existing forwarding has a maximum five-second
continuation window. Exceeding it interrupts the stream. A lost/replaced gateway
after activation requires retained work to settle fully before completion with
its replacement. Do not describe that as preservation of the old stream.

## Replace the gateway

Gateway replacement requires full quiescence, never owner-only readiness. Keep
the owner running so the gateway can check the drain. Start a normal quiescent
drain and inspect its status, then:

```sh
orchestrator gateway-status --socket /var/lib/agent-workbench/gateway.sock
orchestrator gateway-stop --socket /var/lib/agent-workbench/gateway.sock \
  --owner UPDATE_ID --generation GENERATION
```

Wait for exit. Start the reviewed replacement and check status:

```sh
orchestrator gateway --config /etc/agent-workbench/application.json
orchestrator gateway-status --socket /var/lib/agent-workbench/gateway.sock
```

For a gateway-only update, reopen application admission with `drain-abort` using
the same owner/generation. For a combined update, continue the owner activation
procedure instead. A gateway stop refuses active work, stale identity or unavailable
owner checks. If the pre-activation deadline expires, the attempt is abandoned;
establish a new drain before continuing.

Never remove the lifetime lock or clear pending request records. Forced termination
can interrupt streams and is a fault-recovery action, not an orderly update.
