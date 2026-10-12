# Project model

A Project is the authorization, workspace, runtime and reachability unit. It owns
one sandbox binding and a saved list of Volume slots. Workspace and runtime are
required writable owned roles with saved configurable guest destinations
(`/workspace` and `/runtime` are bootstrap defaults). Additional owned data slots may be writable or read-only.
Reviewed shared read-only publications are referenced by slots and never deleted
by Project cleanup. Stable slot keys, not array positions, identify resources.

Project states describe provisioning, active execution, pause/resume, failure and
deletion. Durable operations preserve original keys and revision fences across
retries. A Project is active only after binding observation, host/resolver
projection and runtime-release verification have succeeded.

An Agent Session owns ordered prompt intents, native execution runs and exact
input-delivery evidence. Its logical identity survives worker and placement
changes. Each runner incarnation has an owner fence, command journal and native
capture lineage. Held prompts require Release for ordinary dispatch, but can
be explicitly Steered into an eligible run. Prompt-local revisions govern row
mutations; Stop/Steer carry the exact run/incarnation/owner fence. Session
control revision signals a refresh while token events advance the display.

A Terminal is a retained PTY resource with its own incarnation, dimensions and
observed exit status. Browser sockets are observers. One observer holds input;
read-only observers can watch. Detach changes no execution intent. Reopening an
exited Terminal creates a new incarnation and resets the renderer.

Admission accounts for real work: pending released prompts, live execution runs,
unresolved deliveries, terminal processes,
workspace mutations, captures, recovery and cleanup. An idle Project pauses only
when those blockers have settled and its idle deadline expires. Read-only
inventory and browser watchers do not extend the deadline. Idle Agent slots can
quiesce without pausing a Project or its sibling Sessions.

Schedules belong to Projects. They may target an existing Session or create a
Session per run. Conversation-reader grants expose conversation projections only.
Public shares freeze messages and selected files with a revocable bearer token;
they do not grant a live workspace or terminal.

Session retirement removes its private runtime/capture state after termination
and recovery settle. Project deletion waits for sandbox destruction, owned-volume
reclamation and host artifact cleanup. Small tombstones preserve deletion truth;
private conversation payloads, shares and owned artifacts are purged.
