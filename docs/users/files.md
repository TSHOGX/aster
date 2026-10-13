# Files

[Documentation](../README.md) · User guide

Open **Files** in a Project to browse its workspace. Listing, previews, uploads,
edits and downloads work while compute is paused, and before the first Agent or
terminal starts. Available actions depend on your file permissions and storage allowance.

## Browse and find

Expand folders in the file tree and select a file to preview it. Large folders
load in pages; use the additional-page controls when needed. Links to workspace
files in conversation replies open the corresponding file.

Search matches filenames and relative paths across the Project, including folders
that are not expanded. It ignores letter case and surrounding query whitespace;
it does not search file contents. Use **Next results** or **Continue search** to
look beyond the current result window. A partial or resource-limited search is
not a complete no-match result. Refresh results after files change.

## Upload and download

1. Open the intended folder and choose Upload.
2. Select the files and review any replacement action.
3. Wait for a confirmed result, then refresh the listing if necessary.
4. Use Download to retain an original file locally, including formats without a preview.

The installation can impose transfer and storage limits. Preview limits are
separate from upload/download limits. If an upload result is unknown, inspect the
destination before trying a fresh upload; a disconnect does not establish failure.

## Preview and edit

**Preview** displays supported formats; **Source** shows supported text without
editing it. Use **Edit text** for supported text files up to 1 MiB. Binary files,
unsupported encodings and larger files do not offer text editing.

Save explicitly. Switching files or Projects with unsaved edits offers Save,
Discard or Cancel. Hiding Files or changing conversations retains the open editor.
If another process renames or deletes the file, the editor preserves unsaved text
and shows that the original target is unavailable. Copy that text somewhere safe
before discarding it.

The editor does not merge simultaneous changes by other Agents or users. Coordinate
writes to shared files and reload before editing when another writer has changed them.

A preview can fall back to Source or Download when the document is too large,
unsupported or too complex. Office previews may differ from the authoring
application. See [formats and limits](../reference/file-previews.md).

## Shared directories

If your environment includes shared directories, **Settings → Advanced / Developer**
can make them visible in Files. Visibility changes the browser presentation, not
the Agent's access. Read-only mounts cannot be edited; writable shared directories
can be changed by other authorized users. They are not private copies.

Project-owned initial content is a separate copy. Editing it does not change its
published source, and restarting a Project does not reset it to that source.

If reads fail, use Retry or Refresh. For an uncertain write, inspect the current
file and [ask for recovery help](troubleshooting.md) before repeating the mutation.
