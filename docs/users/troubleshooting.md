# Troubleshooting

[Documentation](../README.md) · User guide

Keep the Project/conversation/task identifier, approximate time, action and visible
error when requesting help. Share diagnostic details privately; do not post passwords,
public-share tokens or confidential prompts in a public issue.

| Symptom | What to do |
| --- | --- |
| Temporary password or setup session expired | Ask an administrator for another password reset |
| Sign-in or a feature is denied | Check the account with your administrator; refreshing does not restore a revoked permission |
| Project will not start or resume | Read the stated quota, permission, environment or maintenance reason; ask an administrator if you cannot correct it |
| Project remains paused after account reactivation | Use explicit Resume Project after suspension finishes; release held prompts and resume schedules separately |
| Prompt stays waiting | Inspect whether it is held, another run is active, or the Project is blocked; release it only when you intend to run it |
| Scheduled task is late | Check status, timezone, history and Project availability; due time is not a guaranteed start time |
| Terminal reconnect shows missing output | Inspect the running process and saved output files before repeating the command |
| File search has no match | Continue an incomplete search, verify the path, or refresh after changes; search does not read file bodies |
| Preview fails or looks incomplete | Try Source or Download and open the original in its authoring application |
| Save conflicts with another writer | Preserve your text, reload the current file and reconcile changes before saving |
| Shared conversation or link is unavailable | Ask the owner to check revocation, expiry and account suspension |
| Service is in maintenance/draining | Wait for the operator to reopen admission; keep drafts and existing request retries |

## When an outcome is unknown

A lost connection can happen after work was accepted or a file changed. **Unknown**
means the application cannot yet establish the result; it does not mean nothing happened.

1. Reconnect and inspect the existing conversation, operation or destination file.
2. Use the original Retry or recovery action when offered. Avoid submitting the
   same task as a new request until you know whether the first one took effect.
3. If the outcome remains unknown, ask an operator to inspect it. Do not delete
   the Project or restart it merely to hide the error.

Acknowledging an unknown result is not proof of success or failure and cannot undo
external side effects. An Agent may have already changed a file or contacted a service.

## Browser and preview issues

Refresh only after protecting unsaved edits. Very large or complex documents can
exceed preview limits even when their file size is allowed. If an Office viewer
fails to start in Safari, try another browser or download the original; rendering
support depends on the document and browser. There is no guarantee of exact Office
layout. See [file preview limits](../reference/file-previews.md).

Operators: use the [recovery runbook](../operators/recovery.md) for persistent failures.
