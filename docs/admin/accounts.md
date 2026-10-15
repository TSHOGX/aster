# Accounts, groups and permissions

[Documentation](../README.md) · Administration

Sign in with an administrator account and open `/admin`. Use **Users**, **Groups**,
**Policies** and **Audit** to manage access. Host operators create the first
administrator during [installation](../operators/installation.md).

## Create an account or reset a password

1. In Users, create the account with its intended role and group memberships.
2. Review its effective permissions and resource allowances.
3. Deliver the one-time temporary password through your approved private channel.
4. Ask the user to sign in and choose a permanent password.

Temporary passwords are shown once, expire after 24 hours and permit only password
setup. The setup session lasts 15 minutes. A retried response cannot reveal the
same temporary password. If the original response is lost, first determine whether
the operation succeeded; issue a new reset if the user needs a replacement credential.

Passwords require 12–128 Unicode characters and at most 512 UTF-8 bytes. Password
replacement revokes previous sessions. Usernames remain reserved when accounts
are disabled; resource ownership uses a stable account identity, not the display name.

## Set permissions through groups

1. Create a policy and set the required actions to Unset, Allow or Deny.
2. Bind policies to a group and add its members.
3. Configure that group's resource policy and default templates when needed.
4. Open a user's effective-access view to check the resulting permissions and
   their group/policy sources. Do not assume one grant overrides every other constraint.

Project management, Agent execution, terminal use, scheduling, file actions and
sharing have separate permissions. File export also requires read permission.
Granting an environment does not automatically grant its shared directories.
Use [Resources](resources.md) to configure those grants and defaults together.

Preview group deletion before confirming it. Review affected users, lost grants
and resource changes. Deleting a populated group does not silently assign a
replacement membership. Refresh a stale preview and review it again.

## Suspend an account

Suspension disables login, revokes sessions and service access, pauses schedules,
holds unstarted prompts and suspends sharing. It also starts an operation to stop
active work and pause the account's Projects while retaining their data.

Follow the operation and its Project targets until their effects are observed.
**Accepted** or disabled account status does not mean every process has stopped.
A target can remain unknown while the engine is unavailable.

Re-enabling restores login and current policy access. It does not automatically
restart work. After suspension completes, the owner explicitly resumes Projects,
releases held prompts, resumes schedules and reactivates eligible shares as needed.
Account enablement cannot revive an expired or revoked public link.

## Review changes and sensitive access

Use Audit filters for actor, target, action and time, then follow operation links
for completion details. A save with an uncertain response should be retried using
the editor's retained request, not a new competing edit. If saving succeeded but
refresh failed, reload saved data before attempting another write.

Administrative conversation inspection is separate from routine Project metadata.
It requires current administrator authority and the explicit content-read grant,
and successful reads are audited with their purpose. It does not grant arbitrary
workspace, terminal or host-file access. See [Security](../reference/security.md).

## Group configuration drafts

Open **Groups → select a group → Evolution permissions** to manage scoped
configuration, workflow-authoring, run-start and source-evidence permissions. Search for an account,
inspect its explicit grants and effective access, then choose **Allow** or **Deny**.
Review the exact account/capability change before applying it. Ordinary accounts
also need current group membership. Explicit denial applies to administrators;
evidence and workflow-authoring access always require explicit allow. Studio
author/review/publish grants are managed separately.

**Refresh permissions** reloads current grants. If another administrator changes a
grant, refresh and review a new change. After a lost response or reload, use
**Check original request** to recover the same command. Revocation affects current
access immediately and does not erase already exported copies. Grant management
requires platform administration and remains available to repair a group-level
configuration denial.

Group configuration drafts are available through the authenticated API. An
administrator can assign `config.read` and `config.manage` separately with
`PUT /api/admin/groups/{group}/optimization/grants`. Supply `request_id`,
`account_id`, `action`, `effect` (`allow` or `deny`), and `expected_revision`
(zero for a new grant). Ordinary accounts also need current membership in that
group. An explicit deny takes precedence, including for administrators. Inspect
`GET /api/admin/groups/{group}/optimization/grants/{account}` for saved revisions
and current effective decisions before updating a grant.

Use `GET /api/admin/groups/{group}/optimization/draft` to read the latest draft
and `PUT` at the same path to save it. Saving requires `request_id`,
`expected_revision` (the draft revision), and `configuration`. Optional `settings`
contains `service`, `evaluation`, and a `bindings` map keyed by binding ID, with
boolean `service` and `evaluation` inheritance flags. Omitting a binding flag keeps
its configuration value explicit. Omitting the group evaluation choice uses the
pinned template policy for inheriting bindings. The configuration's
`group_id` must match the path, `revision` must be zero, and `auto_enabled` must
be false. Incomplete drafts are allowed. A save does not activate configuration
or change running work. On a revision conflict, read the latest draft before
reapplying edits; retry an unchanged request with its original request ID.

These requests use the normal login session and same-origin mutation checks.
Configuration grants do not confer source access or permission to publish content.

To check a saved draft, POST to
`/api/admin/groups/{group}/optimization/preview` with `expected_revision` (the
current desired configuration revision, initially zero),
`expected_draft_revision`, and `auto_enabled`. The response includes central
validation issues and the required participants. `central_valid` does not mean
Studio or Evaluation Space has accepted the configuration. Preview does not
reserve output paths or start work.

POST the same fields plus `request_id` to the `/activate` endpoint to create a
pending activation. This requires both configuration read and manage access.
A `202` response acknowledges the operation; it does not confirm activation.
Read `/api/admin/groups/{group}/optimization` to compare desired and active
revisions. When both participant clients are configured, the worker reserves Studio outputs,
reconciles the Evaluation Space projection, adopts Studio reservations, and marks
the revision active after both receipts are verified. A participant outage leaves
the operation pending for retry.

POST `request_id` and `expected_revision` to the `/disable` endpoint to disable
automatic admission immediately, including while activation is pending. This
requires manage access and retains the pending operation and active revision.

Inspect an activation at
`GET /api/admin/groups/{group}/optimization/operations/{operation}` using its
`operation_id`. This requires `config.read` and returns the exact configuration
revision/digest plus recorded participant states and bounded failure codes. Missing
participant entries mean no receipt has been recorded. `catalog_rejected` indicates
Evaluation Space rejected the configuration's catalog validation. Operation lookup
is scoped to the group and retains historical operations. The `current` field
identifies the current desired operation; a historical `active` state records that
activation succeeded, not that the configuration is still selected.

After correcting a failed operation's cause, POST `request_id` and
`expected_revision` to
`/api/admin/groups/{group}/optimization/operations/{operation}/retry`.
This requires `config.manage` and retains the same configuration and operation ID.
A successful request returns `204`. Retry an uncertain response with the same
request ID. If activation fails again, inspect the new failure and use a new
request ID for another deliberate retry; replaying the previous request cannot
restart a later failure.

To abandon a failed activation, POST `request_id` and `expected_revision` to
`/api/admin/groups/{group}/optimization/operations/{operation}/discard`.
This requires `config.manage` and returns `202` after recording discard intent.
State remains `discarding` while the worker verifies and releases that operation's
pending Studio reservations. Retry and replacement activation are blocked during
cleanup. `discarded` confirms cleanup; the prior active revision remains unchanged.
Already adopted Studio ownership is never released by this compensation path and
keeps discard unresolved. Missing service configuration or revoked authority can
also delay cleanup. Inspect operation state before submitting replacement work.

Read an operation's retained state transitions at
`GET /api/admin/groups/{group}/optimization/operations/{operation}/events?after=0&limit=50`.
Events have operation-local sequence numbers and timestamps. Continue with
`after=next_after` when `has_more` is true; limits range from 1 to 100. Every page
requires current `config.read` authority. Retrying an already acknowledged mutation
does not append another state-transition event. Participant details remain available
on the operation status endpoint.

## Declare configuration sources

Use `PUT /api/admin/groups/{group}/optimization/sources/{source}` with
`request_id`, `expected_revision`, and a typed `source` policy. The source ID must
match the path, and `source.policy_revision` must be the next revision. Policies
identify the source kind/resource, permitted audience groups, derivative publication
permission, exclusions, and an empty capture frontier. Configuration authors must
select the exact current policy; editing these fields inside a configuration does
not create source authority.

The declaring account must own an existing Project or conversation source. Volume
sources require explicit Studio read and configure grants. Global administrator
status alone does not bypass these resource checks. The original issuer controls
subsequent policy revisions. These requests require a normal login and same-origin
mutation checks.

Readers with `config.read` can inspect retained policy provenance through
`GET /api/admin/groups/{group}/optimization/sources/{source}/revisions/{revision}`.
A historical policy is evidence only: it cannot restore revoked source authority
or grant access to the underlying content.

List the group's current source declarations with
`GET /api/admin/groups/{group}/optimization/sources?limit=50`. Continue with
`after=next_after` when `has_more` is true. Limits range from 1 to 100, and each
page requires current `config.read`. This list contains explicit policy declarations,
not a grant to inspect source content; activation still checks current issuer access
and policy revision. Policies in another group are never merged into this list.

Retrieve exact saved configuration content at
`GET /api/admin/groups/{group}/optimization/revisions/{revision}` using a desired
or active revision from the status response. This requires current `config.read`
and returns the configuration, its digest, and its operation ID. The saved content
is immutable: immediate disable changes current admission state without rewriting
that revision's original `auto_enabled` value. Consult configuration status for the
current automatic-admission setting.

A `reservation_rejected` participant failure means Studio rejected the output
reservation or its adoption, for example because another binding owns an overlapping
prefix or the library is archived. Inspect the selected output and current ownership
before retrying. A different output requires corrected configuration and, where
applicable, the explicit transfer workflow; retry does not change the saved payload.

The group grant API also accepts `workflow.edit` for workflow authoring and
publication, and `run.start` for run-admission authority. Workflow authoring requires
an explicit allow, including for administrators. It does not grant configuration
activation, model/tool execution, or source evidence access. This is
separate from configuration editing. Central service admission requires the exact
active configuration and an enabled binding with current source authority. Automatic
admission additionally requires automation enabled, no pending configuration update,
and a non-manual trigger. These checks do not yet expose a user-facing run endpoint.

### Authorize evaluator dataset use

An administrator can grant an evaluator access to one exact sealed dataset artifact
for a group through `POST /api/admin/groups/{group}/evaluation-dataset-grants`.
Send `request_id`, `expected_revision` (zero for a new grant), `artifact`, `service`
and `enabled`. The artifact is its complete immutable scoped reference; the service
is an exact `{id, revision, digest}` evaluator reference. The response contains the
new grant revision. Reusing a request ID with different content is rejected.

This grant is separate from group membership and library access. The requester must
also retain permission to start group work and read/review the output library.
Set `enabled` to false with the current expected revision to revoke future evaluator
use. Already dispatched jobs retain uncertain spending until cancellation and provider
reconciliation finish; revocation does not fabricate a zero-cost result.

### Authorize a sealed corpus for generation

Use `POST /api/admin/groups/{group}/generation-input-grants` to approve one exact
sealed artifact for generation. Send `request_id`, `expected_revision`, `artifact`,
`source_ids`, and `enabled`. Each source ID must already have an explicit source-use
policy for that group, permit derivative publication, and remain under the caller's
source authority. The grant records those exact policy revisions. Granting group
membership alone does not make personal work eligible for collection.

The response contains the new revision. To revoke use, send `enabled: false` with
the current expected revision. New generation effects and result submission then
fail current authority checks; cleanup and accounting for already dispatched work
remain available. Generation and evaluation reservations share the group's concurrent
token/cost ceiling, and uncertain work retains its hold until terminal settlement.
This grant provisions input authority; it does not schedule or execute a workflow.

### Open Evaluation Space

Open `/evaluation-space/` and choose **Sign in** to use your central account.
Non-administrators need current group membership and a `config.read` grant for
at least one group. Select a group or search its name; switching groups preserves
their separate permissions and configuration status. Direct group links return to
the same page after sign-in.

The overview shows desired and active revisions, automatic optimization status,
and separate management, execution and source-evidence permissions. A configuration
grant does not permit source evidence reads. An empty list means no groups match
the current search or grants; a denied group and an unavailable service display
different recovery messages. **Refresh** checks current authority and clears stale
metadata. Language preferences are shared with the central application.

**Sign out of this central session** also ends the originating central login and
its derived application sessions. Configuration editing and activation are available
through the group administration page described below.

Workflow editors use group-scoped links under
`/evaluation-space/groups/{group}/workflows/{workflow}`. The editor keeps executable
JSON and canvas layout revisions separate, validates the same typed graph used by
the runner, uses compare-and-swap draft saves, and publishes immutable revisions
without activating a group. Publishing requires the explicit `workflow.edit` grant;
it does not grant configuration activation or run admission.

### Edit and activate group optimization

Open **Groups → select a group → Automatic optimization**. A configuration draft
is editable and does not enable execution. Select approved source policies, choose
published templates and services, and add goal bindings. The catalog distinguishes
an empty catalog from an unavailable connection or denied access. Service selectors
check current availability and do not expose operator connection or credential fields.

A resource owner can use **Authorize a source I own** or **Edit source policy** to
issue a reuse policy. Search and select an owned Project or session, or a Studio
library with read and configuration permission. Project/session searches include
only the current account's resources, even for administrators; deleting resources
are excluded. Change the source kind to clear the old resource selection. Select
the owning group and any other authorized audience groups with the group checkboxes,
then choose permitted derivative publication and exact exclusions. Configuration-manager or administrator status alone
does not authorize someone else's source. Selecting a newer policy revision is explicit;
existing draft references are not silently upgraded.

Choose a default evolution service and evaluation policy. Defaults and per-binding
inheritance choices are saved centrally with the draft and are available in a new tab
or another authorized administrator's session. New bindings inherit both group defaults.
Each binding labels an inherited value or an explicit override. Choosing a service or
editing a binding's evaluation policy creates an override; **Use group service default**
and **Reset evaluation to group default** restore inheritance. **Use template evaluation
defaults** removes the group evaluation choice, so inheriting bindings use their exact
pinned template's suggestion. Explicit **Off** remains off when defaults change.
Each binding retains its exact template/workflow/service revisions, selected sources,
typed inputs, budget, output library ID, owned directory and reservation generation.
The library must already exist with the template's required profile; configure it in
Volume Studio first. Use **Search output libraries** to browse libraries you can read. Incompatible
profiles, archived libraries, ownership by another group and missing configuration
permission explain why a choice is unavailable. The selected library shows its
owner, accepted main, latest published version and directory reservations. Overlapping
reservations are flagged; **Use matching reservation generation** copies the current
generation (or the next generation for a released reservation). **Refresh output
metadata** reloads current information. Activation rechecks ownership and permissions;
choosing a library does not reserve it or convert its profile. Expand **Workflow, inputs and
execution limits** for parameters, source/dependency/artifact input ports, triggers,
schedule/timezone and budget overrides. JSON parameters must be an object; published
schema validation occurs in the compatibility preview.

Evaluation **Off** stores a resolved disabled policy. **Informational** and
**Required for merge** reveal profile, evaluator and comparison-reference controls.
Turning evaluation off hides local choices; turning it on can restore those choices
while editing. **Reset evaluation to group default** restores inheritance. Saving resolves inherited
values from the chosen group defaults or verified immutable template revision. Review
the saved effective policy before activation. Later draft/default edits do not change
an active configuration, existing runs or another group's pinned settings.

Choose **Save configuration draft**, then **Preview activation**. The preview shows
added, removed and changed bindings, output/evaluation changes, admitted-run count,
and the exact resolved configuration. Central source/schedule checks and Evaluation
Space compatibility must pass. Studio validates and reserves output paths during
activation; preview alone makes no reservation. Check the review acknowledgement and
choose **Activate reviewed configuration**. Follow both participant states until the
active revision advances. Failed activation offers **Retry activation** and
**Discard failed activation**; the prior active revision remains visible.

Local drafts and original mutation requests are retained in account/group-scoped tab
storage. After a lost response or reload, choose **Check original request**. Concurrent
server edits retain your local fields and report a conflict; **Reload server status**
shows the latest saved revision without replacing local content. **Use latest saved
draft** explicitly replaces local edits after confirmation. Configurations with integers
outside the browser's exact range are read-only to prevent rounding on save.
**Disable and drain** stops new automatic admission while allowing already admitted
work to finish under current authority. Manual runs remain available. Canceling active
runs is handled separately from this disable action.

### Capture and seal authorized corpus episodes

Open **Groups → select a group → Corpus and historical backfill** to inspect the active
configuration, source policies, collector progress and queued work. The page refreshes
current permissions and shows changed or unavailable source policies separately from
empty sources. Administrators can explicitly grant themselves evidence access from the
page; this audited action does not replace the source owner's reuse policy.

Expand **Capture an execution or seal a snapshot** to select an approved source,
capture a known terminal execution, seal after a chosen sequence, download a snapshot,
or inspect a binding's successful consumption frontier. Errors retain their distinction
from an empty successful result. These operations require current server-side authority.


Group configuration managers need an explicit `source.read` group grant to read
raw corpus evidence, including when they are administrators. The source must have a
current reuse policy, include this group in its audience, and permit derivative
publication. Project/session policies can exclude exact `sessions/ID`, `turns/ID`
or `executions/ID` selectors. Excluding a turn covers its retries; excluding an
execution selects only that attempt. Wildcards, prose filters, and workspace-file
paths are not Project/session evidence selectors. Excluded records produce no corpus
episode; automatic collection advances past them. Volume corpora can be provisioned
as explicitly approved sealed inputs.

Use `POST /api/admin/groups/{group}/corpus/capture` with `request_id`, `source_id`,
`source_revision` and `execution_id`. Running tasks are rejected. Completed/failed/
unknown outcomes remain distinct. Stored conversation text is bounded and accompanied
by available verified native text/tool observations; missing, partial or truncated
captures are labelled and are not treated as complete evidence. Hidden reasoning
blocks are omitted. The current native runtime records execution boundaries, so
capture excludes earlier session and imported history. Missing or incomplete boundaries
produce partial evidence rather than exporting the whole session snapshot.

Local prompt IDs group retries into an evidence family. Conversation copies retain
their original family ID through subsequent copies. Corpus records include
`family_kind`: `local`, `copy`, `declared` for explicitly curated evidence, or `unknown`.
The example validators hold unknown-family evidence and collapse citations from the
same family to one representative; copying or retrying an incident cannot multiply
its support. Imported historical turns are not new executions.

A late exported capture creates a new immutable episode revision. Seal a bounded
snapshot with `POST /api/admin/groups/{group}/corpus/snapshots`, supplying `request_id`,
`source_id`, `source_revision`, `after_sequence` and `limit` (1–100). Optional
`through_sequence` freezes the upper bound, excluding later revisions. Sealing chooses
the latest revisions, freezes content and records its exact input-use grant atomically.
It does not consume any goal's frontier. Download the sealed bytes from
`GET /api/admin/groups/{group}/corpus/snapshots/{snapshot}/content`; current evidence
and source authority are rechecked and audited before content is returned.

`GET /api/admin/groups/{group}/corpus/bindings/{binding}/sources/{source}/frontier`
shows that binding's successful consumption. Terminal generation settlement advances
only the successful binding that pinned the snapshot; a failed sibling retains its
prior frontier.

When an active configuration has automation enabled and an enabled event or schedule
binding, the collector automatically captures new terminal executions from that
binding's Project/session sources and seals bounded snapshots. It uses the activation
author's current `config.manage` and explicit `source.read` grants plus the source's
current reuse policy. Revocation stops collection without advancing its cursor;
restore the grants or activate an authorized configuration to resume. Missing traces
remain labelled, and late native captures or conversation projections create new
immutable revisions for eligible executions.

Collection starts at activation. Existing terminal executions are excluded, even if
their native captures arrive later. Disabling automation stops collection; re-enabling
establishes a new baseline instead of replaying the disabled interval. Reactivating
an unchanged enabled source preserves pending work. Historical capture remains an
explicit operation using the endpoints above.

Inspect `GET /api/admin/groups/{group}/corpus/collectors` with `config.read` to see each
source's activation baseline, processed event cursor, sealed-through sequence and
latest snapshot ID. A snapshot failure leaves the capture cursor unchanged, and
restart resumes from durable state. Automatic bindings dispatch through the configured
Evaluation Space and Studio participant connections.

Automatic binding schedules require an explicit IANA `timezone` and either a
five-field cron expression or `@every Nm` with 1–10080 minutes. For example,
`schedule: "0 9 * * 1-5"` with `timezone: "Asia/Shanghai"` selects weekday mornings.
Cron follows the selected zone's daylight-saving rules; intervals measure elapsed
time and retain their phase after delays.

The trigger worker records one pending occurrence per binding. Missed schedule ticks
coalesce into that occurrence, and newly sealed input updates its selected source
ranges. Event bindings queue when a new snapshot is sealed. Use
`GET /api/admin/groups/{group}/evolution/triggers?after=0` with `config.read` to inspect
up to 50 occurrences and their ranges; use the response's `next` cursor for subsequent
pages. Reload from zero to refresh pending state. `pending_reason` explains missing
input, an exceeded input budget, or a prerequisite occurrence that is not yet available. `dispatch_state`
shows whether the frozen assignment is waiting for dependencies, pending admission,
dispatched, skipped, or superseded.
`dispatch_error` distinguishes authority denial, budget waiting, and admission retry.
Disabling automation or activating a new configuration supersedes work that has not
been admitted. Already admitted work retains exact retry and drain behavior, subject
to current source and resource grants. Re-enabling starts a new schedule and input
baseline without historical replay.

Each automatic binding requires `inputs`, keyed by the published workflow's input-port
names. Use `{"sources":["source-id"]}` to assemble a bounded corpus delta, or
`{"artifact":{...}}` for a complete immutable artifact reference already sealed in
Evaluation Space and granted for generation. A dependency port instead uses
`{"dependency":{"binding_id":"skills","output_port":"candidate"}}`. A port
selects exactly one form. Source lists must select the binding's approved Project/session sources. Activation
checks port names and schemas against the published workflow. For example:

```json
"inputs": {
  "corpus": {"sources": ["team-project"]}
}
```

Dispatch freezes input references, configuration and output bases before transmission.
Bindings prepared together share the same base for each output Volume and reuse
identical sealed inputs. New evidence remains pending while a binding's generation is
active or its spending is unsettled. Retries preserve the original run and assignment;
a lost response does not create a replacement run. Node models and tools are pinned
by the immutable workflow graph, including nested workflows.

List upstream binding IDs in `depends_on` and map each dependency to an explicit
published output port. Activation checks the output schema against the consumer input
and requires the consumer to declare its upstream sources. Dependencies form an
acyclic graph. A dependent occurrence waits until its prerequisites have occurrences
in the same batch; it never substitutes an unrelated run's latest output.

Waiting consumers retain their original base, source snapshots, and upstream run IDs
through restart. Only successful settled generation exposes dependency output. The
consumer pins the exact output artifact and winning fence; current upstream authority
is checked again on admission and execution. Failed or canceled prerequisites skip
consumers, including downstream chains, without consuming their source frontiers or
creating result branches. Successful consumers retain original snapshot provenance
and advance their own frontiers after settlement. `dependency_pending` means execution
or settlement is unfinished; `dependency_output_pending` means output transport needs
retry; `dependency_authority_denied` requires restoring current permission. Skipped
work retains `dependency_failed` and requires a new occurrence to run again.

For an input-size limit, reduce the selected sources or raise the binding's
artifact budget within the group ceiling. Restore revoked permissions before retrying
an authority-denied assignment; its original pins remain unchanged.

### Preview and execute bounded historical backfill

On the group's **Corpus and historical backfill** page, select local start/end times,
an episode limit and bindings. Required prerequisites are included automatically. Review
each binding's token/cost budget; expand **Advanced budget limits** for attempts, timeout
and artifact size. Choose **Preview historical backfill** to freeze the exact selection.
The review shows source/execution identity, family provenance, capture completeness,
outcome and aggregate spending ceilings. Download the frozen inputs before checking
**I reviewed the selected evidence and spending ceilings** and executing.

The page retains uncertain requests in account/group-scoped tab storage. After a lost
response or reload, choose **Check original request** to reconcile the same ID. Another
mutation stays blocked until that result is known. The backfill ID in the URL restores
the review after reload; **Load backfill** opens another known ID. Changed configurations
require a fresh preview. Only the preview's original account can execute or cancel it.
If evidence access is revoked, owners can still load the non-content status and cancel
unprepared work; evidence details and downloads remain denied.


An administrator with current `config.manage`, `run.start`, and explicit `source.read`
permission can preview historical Project/session evidence without enabling automation.
POST `/api/admin/groups/{group}/evolution/backfills/preview` with a unique `request_id`,
the active `configuration_revision`, RFC3339 `from` and `until` timestamps,
`max_episodes` (1–32), and `budgets` keyed by binding ID. The range selects executions
by start time, includes `from`, excludes `until`, and may span at most 90 days. Only
terminal executions from the bindings' declared corpus inputs qualify. Current source
exclusions apply. Include budgets for every selected binding's prerequisite bindings.

Each budget specifies `max_tokens`, `max_cost_micros`, `max_attempts`, `timeout_seconds`,
and `max_artifact_bytes`. Every bound must be within that binding's configured ceiling.
Choose enough capacity for the model service's per-call reservation; a smaller ceiling
can prevent a call even when its eventual usage would have been low. Concurrent work
still shares the group's spending ceiling.

The preview returns its ID and digest, exact selected execution IDs, family/capture/
outcome labels, requested budgets, aggregate maximum cost/tokens, and reserved run IDs. It retains the selected bytes
and starts no generation. A range exceeding `max_episodes` is rejected rather than
silently truncated; narrow the range and preview again. At least one selected binding
must use a Project/session corpus input. Missing native traces remain explicitly
partial evidence. Late capture arrivals do not change an existing preview; create a
new preview to include them. Inspect the exact frozen corpus bytes with
`GET /api/admin/groups/{group}/evolution/backfills/{id}/inputs/{binding}/{port}`.
This download rechecks current source/evidence/use permission and audits the export.

Execute the reviewed selection with
`POST /api/admin/groups/{group}/evolution/backfills/{id}/execute`, supplying a new
`request_id` and the preview's exact `digest`. The original previewing administrator
must execute it. Use the same endpoint ending in `/cancel` to cancel a preview or queued
batch before preparation. Cancellation remains available to the owning administrator
after source-read revocation and returns no episode metadata. Both actions support
exact request replay. Inspect
`GET /api/admin/groups/{group}/evolution/backfills/{id}` for state and errors; use the
returned run IDs and the group's trigger records for subsequent generation status.

A queued batch waits for its bindings to become available. Preparation freezes current
Studio bases once per output Volume and retains dependency plans. A changed active
configuration makes an unprepared backfill stale and supersedes prepared runs that
have not been admitted; create a new preview instead of
reusing its digest. Once prepared, generation uses the normal current-authority,
capacity, retry, cancellation, and Studio review rules. Disabling automatic triggers
does not cancel manually requested backfill. Historical previews and successful
backfill runs do not advance automatic capture or consumption frontiers.
