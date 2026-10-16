# Author and import files with Volume Studio

[Documentation](../README.md) · Administration

Volume Studio at `/volumes-studio/` prepares reusable file libraries and publishes
immutable versions. Aster imports a version into local storage before Projects
can use it. Studio publication and Aster availability are separate steps.
Studio sign-in requires an active central account with a library read grant, or an
administrator account. Only administrators can create libraries. Read, author,
review, merge and publish permissions are separate grants; administrator status
alone does not grant those actions. Ask your installation administrator to
provision the required library permissions before editing. The catalog lists only
libraries with a current read grant; removing that grant also removes their summaries
and search results.

Shared-assets libraries use the immutable `shared-assets-v1` profile and the same
explicit library grants. A published version must contain root
`AGENTS.md`; skill entries must be directories containing `SKILL.md`. The base
version includes an empty `skills/` directory. These files mount read-only at
`/opt/shared-assets` as system context rather than ordinary Project data.

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
   resolved text, then **Save resolution**. Complete every choice and choose
   **Prepare resolved integration**. The new result needs its own approval.
5. A qualified reviewer chooses **Approve integration**. A merger then chooses
   **Merge approved integration**. A changed main, source head or review policy
   requires preparing and reviewing a new result.

The **Evaluation checks** panel shows the exact profile, evaluator, path policy and
reference commit captured for the integrated result. Off means no benchmark was
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
If an open preparation is stale only because its retained evaluation policy should
be replaced, an authorized reviewer can explicitly choose **Rebind current policy**;
Studio creates a new prepared identity and fresh approval requirement while keeping
the earlier preparation immutable. Main, source-head and policy changes that cannot
be safely rebound still require a new preparation.
Binary and oversized content are identified explicitly when a text preview is
unavailable; selecting a side preserves its file bytes.

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

Archiving a Studio source stops new discovery and exports. Existing bounded exports
can retain access until expiry. Retirement waits for active readers and validation
and removes source references; other versions or drafts can retain the same bytes.
It never recalls copies already imported by Aster.

Aster cleanup is separate. Saved environments, Projects, retained rollback releases
and unsettled operations can prevent retirement. Failed or uncertain imports may
retain capacity until observed cleanup. Correct authorization/capacity problems,
then explicitly retry or cancel and request cleanup. Do not remove transfer files
from the host while they may still be in use.

Operators: [deploy, configure distribution and back up Studio](../../volumes-studio/deploy/README.md),
and configure [trusted sources](../operators/configuration.md#trusted-studio-sources).
