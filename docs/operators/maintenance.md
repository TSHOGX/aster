# Maintain and monitor

[Documentation](../README.md) · Deployment and operations

Use the running owner's protected Unix socket for live administration. Keep its
state directory owner-only and do not open a second database writer. The examples
use `/var/lib/agent-workbench/control.sock`; use your configured path.

## Inspect health and capacity

```sh
orchestrator diagnostics --socket /var/lib/agent-workbench/control.sock
orchestrator diagnostics --socket /var/lib/agent-workbench/control.sock --project PROJECT_ID
orchestrator gateway-status --socket /var/lib/agent-workbench/gateway.sock
orchestrator drain-status --socket /var/lib/agent-workbench/control.sock
```

| Observation | How to use it |
| --- | --- |
| Resource counts, occupied Projects, pending work | Locate unexpected ongoing work before pausing or upgrading |
| `state.storage` allocated/reserved capacities | Identify owned storage, publications and uncertain allocations; these are not measured disk blocks |
| Saved versus observed Project bindings | Investigate pending network/environment changes or missing runtime artifacts |
| `workspace_reads` | Check active file captures and host-space pressure; capture admission is bounded to 512 MiB |
| `writer` queues and rejections | Identify application contention; a background pending result can mean deferred work |
| `agent_pressure` | Diagnose journal/runtime-volume pressure; samples do not establish command completion |
| `schedules` | Inspect due-task age, paused errors, retries and admission delay |
| Gateway authority failures and pending settlements | Diagnose provider access and update blockers |

Published storage and its registry references must not be counted as separate
copies. Publication staging and its immutable derivative are separate allocations.
Unknown creates retain their reservation/charge until resolved. Copy-on-write
savings do not increase quotas. Monitor physical disk usage separately.

Desired network or data versions can remain pending while a Project runs. Inspect
its saved/effective configuration and timestamped observation before deciding to
restart it. A default/template edit alone does not change an existing Project.
Counts can reset at process restart; durable task and operation status remain the
source for individual outcomes.

## Recover administrator access

Use the running owner when possible:

```sh
orchestrator account recover-admin \
  --socket /var/lib/agent-workbench/control.sock \
  --username administrator --password-stdin
```

Provide the password through stdin with an approved secret mechanism. With the
owner stopped, replace `--socket PATH` with `--config FILE` for offline recovery.
The same exclusive writer lock applies. Recovery restores active administrator
status and installs a single-use temporary password; the user must complete setup.
An expired or lost setup session needs another reset.

Routine account creation and resets belong in [Administration](../admin/accounts.md).

## Send a notice

Address an existing immutable account ID, not a username:

```sh
orchestrator notify --socket /var/lib/agent-workbench/control.sock \
  --audience ACCOUNT_ID --title Maintenance --body 'Maintenance is scheduled.'
```

Omitting `--audience` sends a broadcast. Do that only when you intend to notify all
accounts. A notice grants no access to referenced resources. Delivery must be
enabled in [notification configuration](configuration.md#notifications).

## Change a platform resource cap

Inspect current dimensions and revisions first:

```sh
orchestrator limits inspect --socket /var/lib/agent-workbench/control.sock
orchestrator limits set --socket /var/lib/agent-workbench/control.sock \
  --dimension workspace_bytes --maximum 1073741824 \
  --expected-revision 1 --request-id workspace-cap-change-01
```

Replace the example revision with the inspected value and use a unique request ID.
For a lost response, repeat the identical command/request ID. Use only dimensions
returned by inspect and nonnegative integer values. `mount_count` must be a
qualified bound from 2 to 128; transfer capacity cannot exceed the 256 MiB hard bound.

This changes a platform cap, not group offers or user requests. Reductions preserve
allocations and requested overrides but can block increases or resume. They do not
resize volumes. Raising the cap can restore previously capped requests. Review the
user impact alongside [resource administration](../admin/resources.md).

## Routine service care

Keep owner, gateway and frontend supervision independent. Browser closure is not
a stop request. Use application controls for terminal stop, Project pause and deletion;
wait for observed cleanup rather than removing host files manually.

Use [upgrades](upgrades.md) for orderly replacement and [recovery](recovery.md) for
faults. Keep private logs, diagnostic outputs and maintenance records in `private/`
or protected operational storage. Diagnostics do not garbage-collect source artifacts.
