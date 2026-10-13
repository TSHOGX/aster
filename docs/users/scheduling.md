# Scheduled work

[Documentation](../README.md) · User guide

Scheduled tasks save instructions to run later in a Project. They survive browser
closure and idle compute suspension. You need scheduling and Agent permissions,
plus enough resources when the task becomes due.

## Create a task in Scheduled

1. Open **Scheduled** and create a task.
2. Choose its Project, name and self-contained instructions.
3. Select an existing conversation or a new conversation for each run.
4. Choose the recurrence and timezone, review the upcoming times, then save.
5. Reopen the saved task to confirm its status and next occurrence.

The creation form starts at Daily 09:00 in the detected timezone and uses a new
conversation per run. Review these defaults. Recurrence options include daily,
weekdays, weekly, monthly, multiple monthly dates, minute intervals, manual-only
and advanced five-field Cron. The interval range is 1–10,080 whole minutes.
Its first run is one interval after creation or enabling.

Use an IANA timezone such as `Asia/Shanghai` or `America/New_York`. The preview
shows occurrence offsets so daylight-saving changes are visible. Choosing another
zone preserves the entered wall-clock time. Interval tasks use elapsed minutes
and do not change their interval at daylight-saving transitions.

## Ask the Agent to schedule

When your installation enables scheduling tools, you can ask in conversation:

- “At 23:00 Asia/Shanghai, check the final score and report a source here.”
- “Every weekday at 09:00, summarize these files in a new conversation.”
- “Starting at 22:00, check every five minutes until the match finishes; stop by
  midnight or after 12 checks, and notify me when confirmed.”

Clarify the timezone, event and deadline when asked. A successful saved-task
acknowledgment confirms creation; natural-language agreement alone does not.
If tools are unavailable, use Scheduled. Conversational requests default to the
current conversation; a new conversation needs its own complete instructions.

Bounded checks stop admitting further checks at their deadline or count limit.
The tool's default is 12 checks and its maximum is 100. The Agent checks the
external source; there is no continuous event subscription. With finish-only
notifications, pending checks remain quiet, while failures, unknown outcomes and
exhausted budgets need attention.

## Run, edit and pause

**Run now** on a recurring task adds a run without moving its cadence. On an active
one-shot, it consumes that one-shot. An exhausted one-shot must be replaced by a
new task for a new intention.

Editing instructions preserves the next due time. Changing the trigger or resuming
a task recalculates it; changing an interval restarts its timer. Review the preview
before saving an advanced rule, especially if replacing it with a simple recurrence.

Pause, cancel and delete stop future runs. They do not stop prompts already queued
or running: open the linked conversation and use its controls. Deleting a fixed
target conversation cancels future work aimed at it. Deleting a conversation that
only supplied the original request does not necessarily delete the task.

## Read history and notifications

Task status and execution outcome are separate. **Exhausted** means a one-shot
trigger was consumed, not that its work succeeded. Open run history and follow
the conversation link to inspect queued, running, completed, failed or unknown work.

Due time means eligible to run, not a guaranteed start time. A task admits at most
one unsettled automatic occurrence at a time and coalesces missed times. An overdue
one-shot can run once after recovery unless its deadline passed. Unknown work is
not automatically retried.

Result notices appear in Inbox and link to the conversation. Notification edits
affect future occurrences. Aster does not send scheduled results through email,
mobile push or third-party messaging by itself.

If a save response is lost, inspect the existing task or retry its original request;
creating another task may duplicate the work. See [Troubleshooting](troubleshooting.md).
