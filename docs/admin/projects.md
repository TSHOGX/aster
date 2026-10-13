# Inspect and control projects

[Documentation](../README.md) · Administration

Open **Projects** in Administration, or follow a user's Projects link. Filter by
owner, title/ID or state and open a Project to inspect its saved environment,
network, mounts, storage and lifecycle progress.

## Read status before acting

Allocated and reserved storage are declared capacities, not filesystem disk usage.
A pending deletion or uncertain allocation can retain capacity until cleanup is
observed. Shared publication references are not charged repeatedly for each mount.

Live observations carry timestamps. **Unavailable**, **absent** and **unchecked**
are different states; an unknown observation does not prove that compute stopped.
Refreshing details does not start compute, initialize storage or retry a mutation.
Routine metadata does not expose conversation bodies or live workspace content.

## Choose an action

Review the interruption warning before confirming:

| Action | Result |
| --- | --- |
| Pause Project | Stops active child work and retains the paused Project under an execution hold |
| Stop Project work | Stops work and leaves the Project paused under an execution hold |
| Restart saved environment | Stops work, then attempts to resume the same saved environment |
| Apply desired network now | Stops work, then attempts resume with the reviewed desired network |

These actions preserve owned files, mount capacities and the saved runtime release.
They do not reseed initialized data or delete the Project. Unstarted prompts are
held, and schedules skip targets held by an administrative control.

A restart rechecks the owner account, permissions, quotas and registered artifacts.
If the desired network changes after review, the operation refuses to resume with
an unreviewed choice. A denied resume leaves the execution hold and files intact;
correct the cause and submit a new authorized action or have the owner explicitly resume.

## Follow completion

The initial response means **accepted**, not completed. Follow its operation link
and per-Project progress through stopping and, where applicable, resuming. An engine
timeout can leave the target unknown while reconciliation continues. Do not clear
state or send competing restarts to make the status disappear.

Account suspension supersedes an earlier restart. Re-enabling the account does not
restart that old operation. Project deletion is a separate cleanup request and
can take precedence over an in-progress control.

For environment-version differences, use the Project's Environment details and
[environment updates](environment-updates.md). For unresolved engine or storage
failures, ask the operator to follow [recovery](../operators/recovery.md).
