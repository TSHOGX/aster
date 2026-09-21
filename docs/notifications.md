# Notifications

The inbox is an authenticated product projection. Turn outcomes, schedule work
and operator notices are stored with recipient scope. Reading a notification
updates only that actor's record. If an unknown turn outcome is later resolved,
the notification is updated to reflect the evidence.

Operator notices go through the running application's protected Unix socket:
`orchestrator notify --socket PATH --audience USER --title TITLE --body BODY`.
An omitted audience is an explicit broadcast. The command does not open a second
product writer. Notifications do not grant access to the Project or conversation
they reference; opening a target performs its normal authorization check.
