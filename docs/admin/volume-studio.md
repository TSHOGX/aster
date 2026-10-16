# Author and import files with Volume Studio

[Documentation](../README.md) · Administration

Volume Studio at `/volumes-studio/` prepares reusable file libraries and publishes
immutable versions. Aster imports a version into local storage before Projects
can use it. Studio publication and Aster availability are separate steps.
Studio sign-in requires an active central account with a library read grant, or an
administrator account. Only administrators can create libraries. Read, author,
review, merge, publish and configure permissions are separate grants; administrator status
alone does not grant those actions. Ask your installation administrator to
provision the required library permissions before editing. The catalog lists only
libraries with a current read grant; removing that grant also removes their summaries
and search results.

Shared-assets libraries use the immutable `shared-assets-v1` profile and the same
explicit library grants. A published version must contain root
`AGENTS.md`; skill entries must be directories containing `SKILL.md`. The base
version includes an empty `skills/` directory. These files mount read-only at
`/opt/shared-assets` as system context rather than ordinary Project data.

## Assign Studio permissions

In **Administration → Users** or **Groups**, open the intended account or group
and choose **Studio permissions**. Enter the library ID from its Studio URL and
choose **Refresh permissions**. This policy lookup does not verify that the library
exists or reveal its contents; you can provision access before the first content
read. For the built-in shared-assets library, use `aster-shared-assets`.

Review each capability separately: read, author, review, merge, publish and
configure. Choose **Allow** or **Deny**, review the named account/group and Volume,
then choose **Apply reviewed permission**. Read access is needed to discover and
open the Volume. A reviewer, merger or publisher also needs their corresponding
action; configure permits merge and evaluation policy management.

Account rows show effective access from current account status and all group
grants. An explicit denial overrides any allowance, including for administrators.
Group rows show the group's rules; inspect individual accounts to check their
effective access. Membership removal stops applying that group's grants. These
permissions do not grant source evidence access, change saved Project mounts or
deploy a version.

If a response is lost, **Check original request** confirms the same change; reload
preserves it. A revision conflict requires **Refresh permissions** and a newly
reviewed change. Grant updates are audited. Revocation stops new authorized reads
and actions; it does not erase earlier downloads or exports.

## Create and edit a library

1. Open Studio and create a library. Choose **Ordinary data** or **Shared assets**;
   the content profile cannot change after creation. Filter the catalog by profile.
2. Select a **Working branch**, or enter a **New branch name** and choose
   **Create branch from main**. Each branch has its own saved draft and revision; publishing a version does not
   change that branch's comparison head or saved revision.
3. Add directories, create text files or upload files/folders. Edit files in tabs
   and save changes to the selected branch.
4. Review **Changes** against the branch's committed head. Save persists draft
   content; **Commit saved draft** creates an immutable checkpoint.

Text and Markdown editing supports UTF-8 files up to 1 MiB. Raster previews support
PNG/JPEG/GIF/WebP up to 16 MiB. Other formats are available as authenticated downloads.
Markdown does not execute raw HTML or fetch remote images. A uniform CRLF line
ending and a byte-order mark are preserved; mixed endings are disclosed and saved as LF.

Empty files and directories retain their paths. Symlinks, special files, path
traversal, invalid parents and directory names with leading/trailing whitespace
are refused. Ordinary internal spaces are supported. A library version supports
up to 256 MiB per file, 1 GiB total and 10,000 entries, subject to deployment capacity.
Shared assets are limited to 8 MiB per file, 64 MiB total and 4,096 entries.
Each skill may include supporting reference files alongside its `SKILL.md`.
Runtime state and secret paths, including `.env`, `.ssh` and `.aws`, are refused.

For shared assets, the validation panel reports path-specific problems, skill count,
reference-file warnings and an `AGENTS.md` text preview. Drafts may be incomplete;
publication repeats structure, encoding and content-integrity checks. The built-in
Aster library starts with published `v1`, containing only `AGENTS.md` and an empty
`skills/` directory. Create a working branch to extend it, then review and merge the changes before
publishing a new version. The original version remains immutable. Scheduling is supplied by runtime tools, not a skill.

## Handle concurrent edits and recovery

Before saving, Studio checks whether the target changed since editing began.
A save conflict retains your text for review. Changes elsewhere in the branch do not
automatically authorize overwriting this file. Save all stops at its first failure;
a save acknowledges the submitted text, not additional typing made while it runs.

The browser keeps local recovery records when its storage is available. Restoring
is explicit and checks the recorded branch and current target. A recovery record
without branch identity cannot be restored into an arbitrary branch. Recovery has a 50 MiB per-account budget;
storage failure or exhaustion is reported. It is not a backup: a sudden crash may
lose the most recent edits, and explicit logout clears local recovery. Protect
unsaved edits before clearing browser data or changing devices.

## Upload files and folders

Uploads can resume acknowledged data: after refresh, select the same file again.
Changing an upload target requires confirmation of its current contents. Folder
uploads report individual file outcomes, so check for partial failures.

The Tasks drawer covers this browser session and visited libraries, not every
upload on the server. Navigating within Studio keeps transfers alive; closing the
browser stops active transfer. Unfinished uploads block checkpointing their own
branch until completed or canceled; another branch can still be committed.

## Review and merge committed changes

1. Save the content you want to include and complete or cancel that branch's uploads.
2. Enter a **Commit message** and choose **Commit saved draft**. Unsaved editor
   changes are explicitly excluded from the checkpoint.
3. Choose **Prepare integration** to combine the committed branch head with main.
   Inspect the changed files and the **Integrated result** before approving.
   When several branches contain independent work, **Compose selected changes**
   lists each immutable branch head. Select only the paths to review together;
   omitted paths are shown as deferred and remain on their source branch. The
   prepared result freezes every selected head and path, so later branch edits do
   not change the review.
4. If conflicts appear, open each path in **Resolve merge conflicts**. Compare the
   common ancestor, main and branch. Choose a side, delete the path or write
   resolved text, then **Save resolution**. **Resolution result** previews the
   selected choice. Each side identifies its exact commit; files show size/digest
   and **Download exact file**, including binary files and text above the 1 MiB
   preview limit. Choosing a directory includes its complete subtree; nested
   conflict choices must agree. Deleting a path removes its descendants.
   To supply new bytes, select a **Replacement file**. Studio uploads it to the
   displayed path in the working branch draft, regardless of its local filename.
   Confirm replacement of the existing file. After it is saved, **Commit saved
   draft** and **Prepare integration** again; the new conflict workspace includes
   those bytes. The earlier workspace preserves its frozen inputs and choices.
   A source directory must be replaced explicitly in branch authoring first.
   Incomplete uploads stay in **Upload tasks** for recovery and block committing.
   Complete every choice and choose **Prepare resolved integration**. The new
   result needs its own approval.
5. A qualified reviewer chooses **Approve integration**. A merger then chooses
   **Merge approved integration**. A changed main, source head or review policy
   requires preparing and reviewing a new result.

The **Evaluation checks** panel shows the exact profile, evaluator, path policy and
reference commit captured for the integrated result. A target-main comparison pins
the main commit at preparation time. If another branch merges first, prepare again:
the new integrated tree and reference need their own passing check and approval.
A generation-base comparison keeps the branch's initial base; a pinned-release
comparison keeps the accepted commit of the selected published version. Those
references stay fixed when main advances. A changed integrated tree still needs
its own passing evidence and approval even when the comparison reference and
policy are unchanged. The earlier report keeps its original identities for
historical inspection.
Off means no benchmark was
requested. Informational evaluation remains mergeable under human approval while
pending or after failure, error or inconclusive results. A report arriving after
merge attaches to the original integration without changing main. Required checks
need passing evidence before approval and merge. Adopted group output policies also apply
to manual changes, deletes and renames in those paths. Tightening a policy requires
repreparing; changing a binding to off does not erase checks already captured by an
open branch's preparation. When your operator has connected Evaluation Space, set the evaluation token, cost
and time limits and choose **Request evaluation**. The group ceiling and exact
dataset/evaluator grant must permit the request. **Retry evaluation admission**
resumes an uncertain request with the same job identity; **Refresh evaluation**
retrieves its result. **Cancel evaluation** records cancellation intent and preserves
uncertain spending until the provider reconciles. A job canceled before dispatch
records zero spending explicitly. After cancellation settles, **Request evaluation**
creates a new attempt without changing the canceled history. Passing evidence enables approval only for the exact prepared
result. Missing, failed, canceled, inconclusive or revoked evidence leaves required
checks blocked. Detailed held-out reports remain in Evaluation Space; Studio shows
check status without exposing dataset contents.

Main advances only after acceptance. Reviewers can also request changes or reject
an integration. Saved conflict choices remain accessible through the workspace URL.
Unsaved conflict text and an unconfirmed save or completion are retained in this
browser tab for the signed-in account and exact workspace. After reloading, choose
**Recover conflict operation** to reconcile the original request. Editing remains
locked until it settles. If another author changed the workspace, **Reload conflict
workspace**, inspect **Current saved resolution**, and choose **Use current conflict
revision** before saving your retained text. Changed main or source inputs require
a new preparation.

Failed or denied reads clear displayed conflict content. Reload after access is
restored to recover the local draft. If local storage is unavailable, keep the tab
open until the operation is confirmed; a full page reload may lose unsaved recovery.
**Discard conflict recovery** removes the local draft and request only. It cannot
undo a server operation that already succeeded. A malformed recovery must be
discarded before starting another operation. If completion succeeds but its result
cannot be loaded, reload the integration URL to retrieve it without completing again.
For an open, single-branch integration, a user with author and reviewer authority
can choose **Rebind current policy** and confirm replacement of retained evaluation
requirements. Studio creates a new prepared identity requiring fresh approval while
keeping the earlier preparation immutable. Rebinding applies to that preparation;
later ordinary preparations still retain historical branch requirements. Accepted
integrations and selected-path compositions cannot be rebound. Reprepare a
composition with its explicit selections when its inputs or policies change.
Requirements captured after a rebind can also invalidate its readiness; a rebind
does not grant an ongoing exemption from later policy changes.
Binary and oversized content are identified explicitly when a text preview is
unavailable; selecting a side preserves its file bytes.

## Configure evaluation for a Volume path

Open a Volume's **Evaluation policies** tab. Reading policies and their revision
history requires Volume read access; saving requires Volume configure access. The
Volume must already belong to a group through an activated output. Evaluation
requests also need an active group configuration containing an output to this Volume,
available group budget and an explicit dataset/evaluator use grant.

Choose an existing policy or **New policy**. Enter a clean relative **Policy path**,
or leave it empty for the whole Volume. A selector covers its descendants, including
manual edits, copied content, deletes and renames. Choose **Off**, **Informational
evaluation** or **Required evaluation**. Enabled policies require the exact published
profile and evaluator IDs, revisions and SHA-256 digests. Choose no comparison,
accepted main, the branch's initial base, or an exact pinned published release as
the comparison reference. **Save policy revision** records an immutable revision;
**Policy revision history** displays earlier revisions. A Volume supports up to
128 distinct selectors. Disable an existing selector by saving an Off revision.

Overlapping Volume, ancestor, nested and group binding checks combine. Disabling
one selector never subtracts another check. Selected-path compositions apply
policies to the actual integrated changes; deferred changes remain on their source
branches. Previously captured requirements remain until an explicit rebind of the
open integration. Policy edits do not start evaluation automatically.

Unsaved edits and unconfirmed requests remain in the authenticated tab's local
recovery. **Recover policy save** retries the exact request after a lost reply.
After a conflict, **Reload policies** retains your edits; inspect **Current policy**
and explicitly choose **Use current policy revision** before saving a replacement.
**Discard policy recovery** removes only the local recovery; an uncertain save may
already have succeeded. If access is denied, the editor clears visible policy
details and restores recovery only after an authorized reload.

## Inspect history and compare snapshots

Open **History** to browse immutable commits and their parent links. **Load current
main** and **Load branch head** resolve the current head once; later edits do not
change the displayed history. Parent links include both sides of a merge.

Choose **Use as comparison base** and **Use as comparison target**, or enter exact
commit IDs, then choose **Compare snapshots**. Open a changed path to inspect its
before/after content. Binary files, directories and files larger than the text
preview limit show metadata; files have authenticated download links. The URL
retains the selected history head and comparison so refresh shows the same snapshots.
Saved draft edits are excluded until committed. Library read grants apply to history,
comparisons and downloads, including subsequent pages.

## Publish a version

1. Open the accepted integration and enter a **Version label** and **Release note**.
2. Choose **Publish accepted commit** and follow the separate version status until
   it is published.
3. Record the version label for selection in Aster.

Publication uses the accepted immutable commit, not the current saved draft.
Subsequent edits do not change that version. Publication failure does not undo the
accepted merge. The worker rechecks current publish authority; revoked authority
holds unfinished publication for an authorized retry. Admitted validation can
continue after browser logout.

To edit a published version, use **Create branch from version**. It creates a
separate working branch from that release's accepted commit and leaves existing
drafts intact. The resulting changes still require review and merge.

## Import into Aster

1. Open **Administration → Resource Center → Volumes**.
2. Select a release from an operator-configured trusted Studio source and import it.
3. Follow acquisition and preparation until the local artifact is ready.
4. Select an ordinary data version as shared data, a workspace seed or independent
   data as permitted by policy. Select a shared-assets version in the environment
   **Shared assets** row. The profiles cannot be interchanged.
5. Save the template for new Projects. To change existing compatible read-only
   data or shared-assets mounts, separately [review a rollout](environment-updates.md).
   Importing or publishing alone never changes an environment or retained Project.

An import may be acquiring bytes, preparing runtime storage, failed or awaiting
cleanup. Source catalog availability is separate from local readiness. Once acquired,
preparation can continue without Studio; ready artifacts remain usable during a
Studio outage. Editing an owned copy never changes the source or causes a restart
to reseed the copy.

## Archive, retire and clean up

Open **Versions** and select a version to review **Source retention**. The details
URL preserves that exact version across reload. Active exports, reader pins and
publication validations block retirement; reload after they settle. Reader pins
are conservative counts and may include more than one pin for a single reader.
Branch draft references retain shared bytes independently and do not block
retirement. The server rechecks current state and pins when you confirm an action.

**Archive source** stops new discovery and exports. Existing bounded exports can
retain access until expiry. **Retire source** is available for archived or failed
versions after blockers clear. It removes distribution references so unreferenced
source blobs can be collected. Accepted Git history, independent branch drafts,
backups and copies already imported by Aster remain separate; retirement does not
erase them.

An unconfirmed archive or retirement keeps its original request in this browser
tab, scoped to the account and exact version. Reload and choose **Recover version
operation** before starting another action, even if the displayed state has
already changed. **Discard version recovery** removes only that local identity
and cannot undo a completed operation. Failed detail reads clear old retention
counts; restore access or connectivity and **Reload version details**. If local
recovery is unavailable, keep the tab open until confirmation.

Aster cleanup is separate. Saved environments, Projects, retained rollback releases
and unsettled operations can prevent retirement. Failed or uncertain imports may
retain capacity until observed cleanup. Correct authorization/capacity problems,
then explicitly retry or cancel and request cleanup. Do not remove transfer files
from the host while they may still be in use.

Operators: [deploy, configure distribution and back up Studio](../../volumes-studio/deploy/README.md),
and configure [trusted sources](../operators/configuration.md#trusted-studio-sources).
