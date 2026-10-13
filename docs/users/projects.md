# Projects and conversations

[Documentation](../README.md) · User guide

A Project groups conversations, terminals and shared workspace files. Conversations
run independently, but their Agents and terminals can affect the same files and
processes. Separate Projects when work requires separate access or isolation.

## Organize conversations

Expand a Project in the sidebar and select a conversation. **New Chat** starts a
draft in that Project; sending its first message creates the conversation.
Switching conversations or opening Search, Inbox or Scheduled changes your view
without stopping background work.

Name Projects and conversations so you can find them later. Search helps locate
conversation content; the Files search searches filenames and paths instead.
A new conversation does not automatically receive another conversation's history.
Include the context and file names it needs.

## Send and manage prompts

While an Agent is busy, sending another prompt adds it to the queue. Waiting
prompts appear separately from the transcript until consumed. Use the controls
shown for that prompt:

| Control | Effect |
| --- | --- |
| Hold | Prevent ordinary dispatch of a waiting prompt |
| Release | Allow a held prompt to run when eligible |
| Steer | Submit the prompt to an eligible active run; it can still wait for consumption |
| Cancel Prompt | Remove the pending intent; it does not undo work already performed |
| Stop | Request that the active run stop; it does not submit or erase your draft |

A successful submission is not proof that execution has completed. After a lost
response, use the existing retry/recovery control and inspect the queue before
sending a duplicate. Restoring a permission does not automatically release prompts
held because that permission was removed.

Tool and thinking summaries can be expanded. Completed response segments may fold
intermediate activity while leaving the final answer visible. Failed, interrupted
or unknown work remains inspectable. **Worked for** describes execution time;
it excludes queue waiting and may be absent when reliable timing is unavailable.

## Pause, resume and stop

An idle Project can pause after its running work has settled. Merely reading files
or history does not keep compute awake. A paused Project retains its storage.
Starting new work normally requests compute as needed, subject to permissions and
quotas. After an administrative hold or suspension, use explicit **Resume Project**
once the administrator has resolved the restriction.

Pause may be blocked by active work or unresolved outcomes. Inspect the stated
blocker and stop or resolve that work first. A restart can interrupt processes;
it is not needed simply because the browser disconnected.

## Delete work

Deleting a conversation removes its private history and may affect tasks targeting
it. Inspect [scheduled tasks](scheduling.md) first. Deleting a Project removes its
owned conversations, runtime and files after cleanup completes. Shared source
publications are separate resources. Download anything you need before deletion.
A deletion request can remain pending while cleanup is blocked; repeatedly
requesting deletion does not prove that storage has been reclaimed.

See [Sharing](sharing.md) to give someone read access without granting access to
the live workspace, and [Troubleshooting](troubleshooting.md) for unknown outcomes.
