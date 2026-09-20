# Session lifecycle

A product session owns conversation, workspaces and Pi native history independently
of its replaceable execution processes. Session status is `created`, `running_active`,
`running_idle`, `pausing`, `paused`, `failed` or `destroyed`; the revisioned runtime
projection also reports live/starting/released sandbox availability and queued turns.

A turn is durable before dispatch. Accepted Khora bindings govern claim and control.
Idle release verifies pause and writer termination, then resumes with a new process
when more work arrives. Native history and durable journals survive process replacement.

Stop reserves a turn-scoped abort and parks queued prompts. Steering reserves the
source prompt until the guest outcome is known; transport receipt alone does not retire
it. Recovery preserves these distinctions across owner replacement and replay.

DELETE requests product retirement: revoke grants/routes, block new admission, settle
active work and recovery obligations, verify final pause, then commit the destroyed
session event. Pending reconciliation returns 503 and retries in the background.
The engine sandbox, overlay and volumes remain retained; retirement does not delete
storage. There is no automatic session expiration or process checkpoint lifecycle.

[Recovery](recovery.md) describes background discovery of incomplete provisioning,
starting processes, idle pauses and the obligations that survive a restart.
