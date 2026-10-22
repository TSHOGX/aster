# Accounts, groups and permissions

[Documentation](../README.md) · Administration

Sign in with an administrator account and open `/admin`. The **Overview** shows the
current number of users, groups, policies and Projects, and explains in order how
users, groups, policies, Resource Center, Projects and Audit fit together. Use
**Users**, **Groups**, **Policies** and **Audit** to manage access. Destructive
actions such as suspending an account, resetting a password, deleting a policy or
retiring an import ask for confirmation in a dialog that lists their effect. Host operators create the first
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

Open the group in Evolution Space (**Groups → select a group → Manage evolution in
Evolution Space**) and choose **Settings → Permissions** to manage scoped
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
Studio or Evolution Space has accepted the configuration. Preview does not
reserve output paths or start work.

POST the same fields plus `request_id` to the `/activate` endpoint to create a
pending activation. This requires both configuration read and manage access.
A `202` response acknowledges the operation; it does not confirm activation.
Read `/api/admin/groups/{group}/optimization` to compare desired and active
revisions. When both participant clients are configured, the worker reserves Studio outputs,
reconciles the Evolution Space projection, adopts Studio reservations, and marks
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
Evolution Space rejected the configuration's catalog validation. Operation lookup
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
and a non-manual trigger. Authorized operators can also use
[manual execution](#start-configured-workflows-manually) with automation off.

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

### Open Evolution Space

Choose **Evolution Space** from the workbench account menu, use **Manage evolution
in Evolution Space** on an administration group page, or open `/evolution-space/`,
then choose **Sign in** to use your central account.
Non-administrators need current group membership and a `config.read` grant for
at least one group. Evolution Space reopens the group you used last, or your only
group; otherwise choose one from the list. **Switch group** in the sidebar changes
groups, and each group keeps its separate permissions and configuration. The
**Evolution Space** title returns to the group list. Direct group links return to
the same page after sign-in.

The sidebar organizes each group's work:

- **Overview**: configuration status, recent runs and **How the pieces fit
  together**, which links the four steps in order.
- **Build**: **Goal templates**, **Workflows** and **Evaluation profiles**.
- **Operate**: **Runs** (history and **New run**) and **Evaluations**.
- **Settings**: **Configuration and activation**, **Sources and corpus** and
  **Permissions**.

Sections you lack permission for are shown disabled with the missing permission.

The pieces relate as follows:

1. A **workflow** says how the work runs: a graph of agent, validator and builder
   nodes with typed input and output ports. Publishing a draft creates an immutable revision.
2. A **goal template** says what to produce: the goal, the compatible workflow
   revisions, the output profile, a suggested output directory and a default evaluation mode.
   It names no Volume.
3. A **configuration binding**, under **Settings → Configuration and activation**, says where it
   applies: the template and workflow revision, the sources to read, the output Volume and
   directory, the trigger, the budget and the evaluation mode.
4. Each **run** writes an isolated branch in Volume Studio for expert review and merge.

An **evaluation profile** is optional. It defines how to measure a candidate on a
held-out dataset. A binding sets evaluation to **Off**, **Informational** or **Required for merge**
and, unless Off, names the profile and evaluator service.

The overview shows desired and active revisions, automatic optimization status,
and separate management, execution and source-evidence permissions. A configuration
grant does not permit source evidence reads. An empty list means no groups match
the current search or grants; a denied group and an unavailable service display
different recovery messages. **Refresh** checks current authority and clears stale
metadata. Evolution Space applies the language, theme and accent chosen in Workbench
settings. Run and evaluation detail pages preserve list filters and pagination when
you choose **Back to runs** or **Back to evaluations**.

A completed run shows **Open branch in Volume Studio** after output transfer settles.
Review and merge that branch, then publish a version in Studio. A no-change result
explains why no branch needs review. Pending output transfer retries automatically;
use **Refresh** to check it rather than rerunning generation. Publication, central
import and Project deployment remain explicit, separate steps.

**Sign out of this central session** also ends the originating central login and
its derived application sessions (**Account → Sign out of this central session**).
Delegated group managers edit and activate configuration under **Settings →
Configuration and activation**, described below. This does not grant access to
user, policy or resource administration. Unsaved workflow, template, profile and
configuration edits warn before leaving the page.

Open **Workflows** to find saved drafts and **Built-in examples**. Each example card
opens a preview with four tabs: **Pipeline** shows the read-only graph and explains every
node (select a node to read its prompt); **Worked sample** shows two input episodes, the
files the builders write and the proposals held for review with their reasons;
**Output fields** lists the fields each proposal carries; and **Baseline evaluation**
shows the suggested metrics and a dataset line. With `workflow.edit`, choose
**Copy to my drafts**: Evolution Space installs the example for the registered model
(or asks you to choose one), then creates an editable draft under the ID you enter. If no
model is available, previews still work; a platform administrator adds one under
**Settings → Model connections**.

The built-in examples cover three memory classes. Procedural: `skills` (draft, critique
up to twice, build `skills/<id>/SKILL.md`) and `corrections` (lessons from failures, each
backed by a completed episode). Semantic: `facts` (atomic facts with scope, valid time and
supersession) and `knowledge` (a semantic layer of entities, dimensions, metrics,
relationships and terms, verified again after consolidation). Episodic: `episodes` (one
case record per source episode). Validators hold unsupported, failed-only, truncated,
copied-only or sensitive evidence with a reason; held items stay in the run and never
become files. Each example writes its own output directory, so several can share one
corpus and Volume.

Platform administrators manage installation-wide models under **Settings → Model
connections**; other users see the entry disabled. **Add connection** takes an ID,
display name, base URL of an OpenAI-compatible service (for example
`https://api.example.com/v1`), API key and model. **Test connection** sends a short
request with exactly these settings, reports the latency and loads the service's
model list into the **Model** picker. The API key is write-only: the page shows only
whether a key is set and its last four characters, and **Edit** keeps the stored key
unless you enter a new one. Changing the base URL, model, output limit, temperature or
JSON mode creates a new revision; existing workflows keep the revision they pinned.
Under **Limits and prices**, **Tokens reserved per call** is reserved from the run budget
for each call, so a binding's maximum tokens must be at least that value. The
configuration page warns when the group ceiling or a binding budget is below the
largest reservation and offers **Raise maximum tokens** to fix it. Workflows,
Templates, Configuration and Runs each show a one-line step indicator marking where
the page sits in the workflow → template → binding → run flow. Turning
**Enabled** off stops new calls. Enabled connections appear in the editor's
**Registered model** list and in example installation.

In the editor, the node palette explains each node kind. For an agent node choose a
**Registered model**; **Exact model reference** keeps the raw ID, revision and digest fields.
With `workflow.edit`, choose **New or fork draft** and a fresh draft ID. Start empty,
copy an exact saved draft revision owned by that group, copy a published workflow
revision (a template's **Fork into a draft** preselects one), or copy the workflow pinned
by a run in that group. Run forks also require `source.read`. A draft revision fork
copies the latest saved layout for that executable revision; a run fork starts
without a canvas layout. Forking leaves the source unchanged. Incomplete drafts can
be saved, but must pass validation before publication or testing.

If the creation response is lost, reopen **New or fork draft** and choose
**Recover draft creation**. The account- and group-scoped pending request survives
reload in the same browser tab. Recovery opens the same draft and preserves any
edits saved since creation. After an explicit rejection, choose a fresh ID and an
accessible source revision before retrying.

Workflow editors use group-scoped links under
`/evolution-space/groups/{group}/workflows/{workflow}`. The editor keeps executable
JSON and canvas layout revisions separate, validates the same typed graph used by
the runner, uses compare-and-swap draft saves, and publishes immutable revisions
without activating a group. Publishing requires the explicit `workflow.edit` grant;
it does not grant configuration activation or run admission.
The canvas shows the workflow as a graph from workflow inputs to outputs. Drag
between ports to connect matching schemas, drag nodes to arrange them, select a
node to edit it in the inspector and press Delete to remove the selection.
**Auto-layout** arranges the graph. Moving nodes changes only the layout revision,
never the executable digest. Loops, maps and subgraphs show their bounds. **List**
switches to an ordered node list with the same editing; narrow screens start there.
The workbench also offers a searchable keyboard node palette, typed connections with
explicit edge removal, executable digest feedback, undo/redo and local recovery. The inspector exposes implementation revisions for transforms and
validators; routing predicates and defaults; join handling for inactive inputs;
and immutable body revisions, limits and failure behavior for maps, loops and
subgraphs. Revision JSON contains `id`, `revision` and `digest`. Incomplete JSON
remains recoverable when switching nodes or reloading a saved local recovery copy,
and blocks saving until corrected. Validation identifies and focuses invalid node
settings; graph-wide edge and cycle errors focus the executable configuration.
Publication alone never changes the active group configuration. Publication shows
the exact workflow reference with **Create goal template with this workflow**,
which opens a new template with that revision selected. To adopt it in an existing
template, publish a new template revision that includes it, then use the template in
configuration. This does not upgrade other groups or earlier runs. Run and evaluation deep links retain
the group scope; their browser views show frozen pins, deduplicated event cursors,
authorized node attempts, effective prompts, inputs, outputs, artifact metadata,
evaluator metrics and explicit stale or redacted states. Artifact metadata includes
identity, digest, size and media kind; bytes still require a separate current grant.
They never expose dataset bytes or service credentials.

Use **Run history** and **Evaluation history** on the group page to browse older
work with **Next page** and **First page**. Run history filters apply across all
retained runs, before pagination. Filter by execution state, exact template or
workflow ID/revision, source snapshot ID, base commit, requester account ID or
creation time. The requester is the account that admitted the work, including the
configuration actor for automatic runs. Enter time bounds with a timezone; the
start is included and the end is excluded. Results use stable ID order.

Execution, handoff and evaluation have separate columns. Filters and page positions
survive reload and browser Back. Applying new filters returns runs to their first
page while leaving the evaluation page unchanged. Failed reads clear displayed
rows; use **Retry** after access or service recovery. On narrow screens, focus the
run table to scroll across its columns with the keyboard.

Open a node attempt to retain its selection in the run URL. **Link to this attempt**
can be copied or bookmarked; reload restores the exact dynamic attempt even when
it falls beyond the first results page. Parent attempts and event entries navigate
to the corresponding attempt, and reused checkpoints link to their source attempt.
Each lookup checks current source access. Missing attempts show an explicit status;
denied or unavailable evidence is cleared until a successful refresh.

Use **Test saved draft** after saving a complete, valid graph. Select an admitted
source run from the same group, choose the full workflow, a node, or a subgraph
container, and set the test budget within the source run's limits. Node and
container tests execute their required ancestors with fresh model calls. The
source input ports and schemas must match the draft. Testing requires
`workflow.edit`, `run.start` and `source.read`; it does not activate the draft.
Tests have their own runs and evidence, share group capacity, and never create
Studio branches, production evaluations or consumable dependency outputs.
After a lost response, use **Recover draft test** to recover the same saved
revision, scope and budget. The pending request survives a page reload in the
same browser session. The inspector identifies the test scope and source run.

For Agent nodes with a maximum-attempt setting above one, an authenticated known
retryable model failure can start the next numbered attempt within the run budget.
The inspector preserves both attempts and each attempt's cost. Automatic model
retries require no declared tools and complete tool evidence; omitted tool traces,
uncertain requests and tool-bearing calls do not trigger a fresh attempt.

Terminal runs also offer **Retry as new run**. The server copies the source run's
frozen pins and budget into a fresh queued run with `retry_of` lineage; the source
run and its evidence remain unchanged. The action requires `run.start`, rejects a
different group scope, and is safe to replay after a lost response. Retrying a
draft test preserves its test restrictions and rechecks the testing permissions.

### Publish goal templates

Open **Goal templates** in a group. Configuration read access permits browsing
published templates; `workflow.edit` permits authoring for that group. Choose
**New template** and fill in the template ID, goal, compatible workflows (the first
selected published workflow sets the typed input and output ports; later ones must
match exactly), output profile, suggested output directory, default evaluation and
parameters schema. **Template document** shows the exact definition that will be
published. The form lists anything still missing before **Publish template
revision** becomes available. The server calculates the template's own digest.

A template's detail page offers **New revision**, which keeps the ID and increments
the revision, and **Duplicate as new template**, which starts a copy at revision 1
under a new ID owned by your group. Another group cannot append revisions to a
template your group owns. Existing publications remain immutable and readable,
including after an upgrade. After a lost publication response, publish again
without changes to recover the exact revision.

**Built-in examples** on the template page shows the same five examples. **Install
template** publishes the example's template and workflows for the registered model and
opens the template; installed examples show **Use in configuration**. An example installed
for one model cannot be reinstalled for another; copy its workflow and publish your own
template instead.

**Use in configuration** opens **Settings → Configuration and activation** with that
template revision selected for **Add template binding**.
For an existing binding, use **Binding template revision** to select the published
template. Its workflow remains selected if still supported; otherwise the first
explicitly compatible workflow is selected. Review that choice, input mappings and
parameters before activation. Template selection preserves output ownership, budgets
and explicit evaluation overrides; inherited evaluation uses the selected defaults.
A different output profile is flagged and requires a compatible template.
Review the binding changes and activate with configuration-management permission. Publishing
alone changes no active configuration and grants no source, model or output access.

### Publish evaluator profiles

Evaluation is optional; without it candidates go straight to expert review. Open
**Evaluation profiles** with configuration management permission and use **Create an
evaluation profile**:

1. **Start from a baseline** chooses a built-in preset (or **Start from scratch**) and the
   profile ID and revision. A preset fills the metric thresholds.
2. **Choose the held-out dataset** picks a sealed dataset of this group, or uploads one with
   **Upload a dataset** (`.json`, `.jsonl`, `.csv` or `.txt`, up to 4 MiB). JSON and JSON
   Lines files are checked for syntax; otherwise Aster does not interpret the bytes. The
   evaluator service receives them unchanged, so its scorer defines the format; JSON Lines
   with one test case per line is typical, and each preset shows an example line. Keep held-out
   cases out of the sources workflows learn from. Uploading the same file and ID again replays;
   a different file under the same ID is refused. Ask an operator to seal larger datasets.
3. **Pin the scorer** enters the exact scorer, runtime and model revisions your operator
   registered for the evaluator service. Keep **Require the exact model** on to report a
   result from a different model as inconclusive.
4. **Set pass thresholds** edits each metric's name, unit, rule (≥ or ≤), value, minimum
   samples and minimum coverage. A result passes only when every metric is reported with
   enough samples and coverage and meets its threshold. Delta metrics compare the candidate
   with the reference content, so 0 means no worse than the current content.

**Publish evaluation profile** creates the revision; select it in a binding's evaluation
policy. An administrator must still [authorize evaluator dataset use](#authorize-evaluator-dataset-use)
for the sealed dataset before an evaluation runs. Uploaded datasets are never exposed as
generation artifacts or reports.

**Published profiles and JSON editor** lists existing revisions and edits the exact profile
document. Use a new ID with revision 1, or increment the revision when editing. The server
calculates the profile's own digest; referenced revisions still need their exact digests.
Profile revision histories belong to the dataset's group and cannot move between groups.

Use **Load more profiles** for additional catalog pages. Publication creates an
immutable revision; existing runs and reports keep their original conditions.
After a lost publication response, retry the unchanged definition to recover the
same revision. Refreshing or selecting another profile asks before discarding
unsaved changes. Failed or denied reads clear the previous document and choices;
refresh after access or service recovery. Publication does not activate a group
policy: select the revision in the group's configuration when ready to adopt it.

### Start configured workflows manually

Choose **Runs → New run** in your Evolution Space group. You need
current membership, `config.read`, `run.start` and read access to each output Volume.
Captured inputs also require `source.read`. You do not need the platform
administrator or configuration-manager role. Automatic optimization may remain off.

Select bindings and review their prerequisite workflows, source scope and spending
ceilings. **Start manual batch** freezes each output's current base and the next
bounded captured input delta, or the binding's configured immutable artifacts.
It does not capture past work: a source owner must first capture approved evidence,
or an administrator can use historical backfill separately. Each source contributes
at most its share of 32 episodes per binding; later input waits for another run.
Successful runs advance their own input frontiers. A busy binding must settle before
another batch can start. Current group, source and output grants are checked again
at dispatch and subsequent effects.

If the response is uncertain, **Check original request** recovers the same batch,
including after reload. Do not start a replacement while the original request is
unconfirmed. If local recovery storage is unavailable, no request is sent. Discarding
a recovery record does not cancel work already queued. Dispatch, execution and
Studio handoff remain separate; a run link becomes available after dispatch.

### Edit and activate group optimization

Open **Settings → Configuration and activation** in the group's Evolution Space.
Platform administrators and delegated managers use the same page; delegated
managers need current membership plus `config.read` and `config.manage`, and
unrelated administration remains unavailable. Earlier administration links for
group optimization, corpus, manual runs and evolution permissions forward to the
matching Evolution Space pages. Configuration access does not grant source evidence,
source ownership, permission management or historical-backfill authority.

A configuration draft
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
then choose permitted derivative publication and exact exclusions. The audience
picker shows current memberships for ordinary accounts and the group catalog for
platform administrators; it never chooses or grants an audience automatically.
Configuration-manager or administrator status alone
does not authorize someone else's source. Selecting a newer policy revision is explicit;
existing draft references are not silently upgraded.

Choose a default evolution service and evaluation policy. Defaults and per-binding
inheritance choices are saved centrally with the draft and are available in a new tab
or another authorized configuration manager's session. New bindings inherit both group defaults.
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
reservations are flagged; **Use next reservation generation** selects the next
generation for a new activation of an active or released reservation. A pending
reservation must be resolved before another operation can claim it. **Refresh output
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
and the exact resolved configuration. Central source/schedule checks and Evolution
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

Open **Settings → Sources and corpus** in the group's Evolution Space to inspect the active
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
Evolution Space and Studio participant connections.

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
Evolution Space and granted for generation. A dependency port instead uses
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

On the group's **Sources and corpus** page, select local start/end times,
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
