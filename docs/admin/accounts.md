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

Group configuration drafts are available through the authenticated API. An
administrator can assign `config.read` and `config.manage` separately with
`PUT /api/admin/groups/{group}/optimization/grants`. Supply `request_id`,
`account_id`, `action`, `effect` (`allow` or `deny`), and `expected_revision`
(zero for a new grant). Ordinary accounts also need current membership in that
group. An explicit deny takes precedence, including for administrators.

Use `GET /api/admin/groups/{group}/optimization/draft` to read the latest draft
and `PUT` at the same path to save it. Saving requires `request_id`,
`expected_revision` (the draft revision), and `configuration`. The configuration's
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

The group grant API also accepts `run.start` for run-admission authority. This is
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

### Capture and seal authorized corpus episodes

Group configuration managers need an explicit `source.read` group grant to read
raw corpus evidence, including when they are administrators. The source must have a
current reuse policy, include this group in its audience, and permit derivative
publication. Capture supports selected Project/session sources with no exclusions;
policies with exclusions are rejected. Volume corpora can still be provisioned as explicit
sealed inputs.

Use `POST /api/admin/groups/{group}/corpus/capture` with `request_id`, `source_id`,
`source_revision` and `execution_id`. Running tasks are rejected. Completed/failed/
unknown outcomes remain distinct. Stored conversation text is bounded and accompanied
by available verified native text/tool observations; missing, partial or truncated
captures are labelled and are not treated as complete evidence. Hidden reasoning
blocks are omitted. Local prompt IDs group retries into an evidence family; imported
copy provenance remains explicitly unknown.

A late exported capture creates a new immutable episode revision. Seal a bounded
snapshot with `POST /api/admin/groups/{group}/corpus/snapshots`, supplying `request_id`,
`source_id`, `source_revision`, `after_sequence` and `limit` (1–100). Sealing chooses
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
restart resumes from durable state. Collection and sealing do not themselves dispatch
a generation run; submit an authorized native assignment with the selected sealed
inputs to run it.
