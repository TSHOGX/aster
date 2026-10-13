# Sharing and Inbox

[Documentation](../README.md) · User guide

Sharing controls appear when your account has the required permissions. Shared
conversations include messages and visible tool activity. They never grant access
to the owner's terminal, execution controls or entire project.

## Share with another account

Open the conversation's Share panel and select an active recipient. Choose the
individual workspace files that person may read, then grant access. The conversation
and selected file paths show their latest contents. Moving or deleting a file makes
its old path unavailable; replacing a file at the same path shares the replacement.

Each recipient has an **Allow file downloads** setting, off by default. You can
change their selected files and download setting from their entry in the Share
panel. Turning downloads off preserves preview access but blocks the application's
download and file-import actions. Preview sends file contents to the browser, so
this setting cannot prevent someone from saving information they have already seen.

Recipients expand **Shared with me**, above **Projects**, and then expand your
name to open a conversation. This section starts collapsed on ordinary visits;
direct conversation links reveal the relevant entry. Reading uses the usual message,
tool activity and file preview interface, without editing or execution controls.

Revoke a recipient's access from the Share panel when it is no longer needed. The
conversation disappears from their navigation, and an open reader clears its
contents. Owner suspension or source deletion also makes received content
unavailable. A lost connection temporarily hides content while access is checked.

## Publish a public snapshot

If public sharing is enabled for your account, choose files and create a public
link. Anyone holding the link can read its frozen conversation and files until it
expires or is revoked. Messages from an unfinished turn are excluded. Later source
edits do not change a published snapshot; publish a new link to share new content.

Each link has its own **Allow file downloads** setting, off by default. Changing
this setting does not refresh the snapshot. Revoke a link to end access, or rotate
it to invalidate the previous URL. Removing your permission to create new links
does not itself revoke published links. Suspending the owner blocks access.

After account reactivation, sharing stays suspended until explicitly reactivated.
**Reactivate reader** restores a reader grant. **Reactivate public link** restores
only an unexpired, still-published snapshot; it does not refresh its files. Expired
or revoked links need a new sharing action.

## Copy a shared conversation

Choose **Copy to my project**, select an existing project or create one, and name
the new conversation. Public-link visitors sign in first and return to the link.
Opening a public link does not add it to **Shared with me**.

The copy includes completed turns and becomes an independent conversation that can
be continued using your target project's environment. Select which downloadable
files to include. Files without download permission remain named placeholders;
their contents are not imported. Imported files use a separate **Shared imports**
folder and do not overwrite existing project files. Each file captures the version
available while the copy is prepared.

Copying does not start a model request. After completion, the new conversation opens
with its history available for your next message. Later revocation of the source
does not remove an already committed copy or files previously downloaded.

The copy continues if its progress dialog is closed. Reopen the copy action to
inspect its state, or expand **Copy tasks** in the shared navigation to find recent
copies even after the source is unavailable. A failed task offers **Retry**; before the independent copy has
been committed, **Copy without files** starts a new attempt without attachments.
If creating a project succeeded but copying failed, the project remains available.

## Use Inbox

Inbox contains conversation-sharing notices, operator messages and any enabled
Agent or scheduled-work outcomes. Opening a notice still requires permission to
its target; receiving a notice does not grant access.

Agent run outcome notices are disabled by default and depend on installation
configuration. Scheduled tasks have their own result choices. Reading a notice
changes only your read state. An unknown outcome that is later resolved may update
the existing notice. Disabling a notification source does not erase earlier notices.

See [security and privacy](../reference/security.md) before publishing sensitive work.
