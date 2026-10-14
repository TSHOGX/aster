# Author and import files with Volume Studio

[Documentation](../README.md) · Administration

Volume Studio at `/volumes-studio/` prepares reusable file libraries and publishes
immutable versions. Aster imports a version into local storage before Projects
can use it. Studio publication and Aster availability are separate steps.
You need an active central administrator account to author in Studio.

Shared-assets libraries use the immutable `shared-assets-v1` profile and are
available only to administrators. A published version must contain root
`AGENTS.md`; skill entries must be directories containing `SKILL.md`. The base
version includes an empty `skills/` directory. These files mount read-only at
`/opt/shared-assets` as system context rather than ordinary Project data.

## Create and edit a library

1. Open Studio and create a library.
2. Add directories, create text files or upload files/folders.
3. Edit files in tabs and save changes to the draft.
4. Review Changes against the base version before publishing.

Text and Markdown editing supports UTF-8 files up to 1 MiB. Raster previews support
PNG/JPEG/GIF/WebP up to 16 MiB. Other formats are available as authenticated downloads.
Markdown does not execute raw HTML or fetch remote images. A uniform CRLF line
ending and a byte-order mark are preserved; mixed endings are disclosed and saved as LF.

Empty files and directories retain their paths. Symlinks, special files, path
traversal, invalid parents and directory names with leading/trailing whitespace
are refused. Ordinary internal spaces are supported. A library version supports
up to 256 MiB per file, 1 GiB total and 10,000 entries, subject to deployment capacity.

## Handle concurrent edits and recovery

Before saving, Studio checks whether the target changed since editing began.
A conflict retains your text for review. Changes elsewhere in the library do not
automatically authorize overwriting this file. Save all stops at its first failure;
a save acknowledges the submitted text, not additional typing made while it runs.

The browser keeps local recovery records when its storage is available. Restoring
is explicit and checks the current target. Recovery has a 50 MiB per-account budget;
storage failure or exhaustion is reported. It is not a backup: a sudden crash may
lose the most recent edits, and explicit logout clears local recovery. Protect
unsaved edits before clearing browser data or changing devices.

## Upload files and folders

Uploads can resume acknowledged data: after refresh, select the same file again.
Changing an upload target requires confirmation of its current contents. Folder
uploads report individual file outcomes, so check for partial failures.

The Tasks drawer covers this browser session and visited libraries, not every
upload on the server. Navigating within Studio keeps transfers alive; closing the
browser stops active transfer. Unfinished server uploads, including another tab's,
block publication until completed or canceled.

## Publish a version

1. Save the draft and resolve unfinished uploads.
2. Open publication review and inspect the exact revision and changes.
3. Confirm publication and follow its status until published.
4. Record the version label for selection in Aster.

Publication freezes the reviewed draft. Subsequent edits do not change that version.
Admitted validation can continue after browser logout. If the author's account is
disabled or demoted, unfinished work pauses for reassignment by another administrator.

## Import into Aster

1. Open **Administration → Resource Center → Volumes**.
2. Select a release from an operator-configured trusted Studio source and import it.
3. Follow acquisition and preparation until the local artifact is ready.
4. Select that exact version in an environment as read-only shared data, an owned
   workspace seed, or independent owned data, as permitted by the source and policy.
5. Save the template for new Projects. To change existing compatible read-only
   data mounts, separately [publish an environment update](environment-updates.md).

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
