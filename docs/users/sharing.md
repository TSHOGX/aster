# Sharing and Inbox

[Documentation](../README.md) · User guide

Sharing controls appear when your account has the required permissions. Sharing
a conversation does not give the recipient access to live workspace files or terminals.

## Share with another account

Open the conversation's Share panel and select an active recipient. The recipient
opens it through **Shared with me**. Revoke the reader grant from the Share panel
when access is no longer needed. A received conversation becoming unavailable can
mean the grant was revoked or the owner was suspended.

## Publish a public snapshot

If public sharing is enabled for your account, select the conversation content and
files to publish, then create a public link. Review the selected material before
publishing: anyone holding the link can read the frozen snapshot until it expires
or is revoked. It does not expose the live workspace or automatically include later edits.

Revoke a link to end access. Rotating the token invalidates the previous link.
Removing your permission to create new links does not itself revoke existing
published links; manage those links explicitly. Suspending the owning account
blocks access to its shares.

After account reactivation, sharing stays suspended until explicitly reactivated.
**Reactivate reader** restores a reader grant. **Reactivate public link** restores
only an unexpired, still-published snapshot; it does not refresh its files. Expired
or revoked links need a new sharing action.

## Use Inbox

Inbox contains conversation-sharing notices, operator messages and any enabled
Agent or scheduled-work outcomes. Opening a notice still requires permission to
its target; receiving a notice does not grant access.

Agent run outcome notices are disabled by default and depend on installation
configuration. Scheduled tasks have their own result choices. Reading a notice
changes only your read state. An unknown outcome that is later resolved may update
the existing notice. Disabling a notification source does not erase earlier notices.

See [security and privacy](../reference/security.md) before publishing sensitive work.
