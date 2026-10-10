# Notifications

The inbox is an authenticated product projection. Conversation shares and
operator notices are stored with recipient scope. Agent run outcome notices are
optional and disabled by default. The application `notifications` configuration
controls each source independently; changes take effect after restart and only
affect future writes. Reading a notification updates only that actor's record.

When run outcome notices are enabled, an unknown turn outcome that is later
resolved updates the existing notice to reflect the evidence. Disabling a source
does not remove notifications already stored.

Operator notices go through the running application's protected Unix socket:
`orchestrator notify --socket PATH --audience ACCOUNT_ID --title TITLE --body BODY`.
An omitted audience is an explicit broadcast. The command does not open a second
product writer. Notifications do not grant access to the Project or conversation
they reference; opening a target performs its normal authorization check.

Directed recipients and read acknowledgments use immutable database account IDs.
Usernames are labels, not notification authority. Inbox reads and acknowledgments
require a current active account; operator delivery may retain notices for a
disabled account, but cannot address a nonexistent recipient.
