/**
 * English message catalog — the source of truth for the key set.
 *
 * `MessageKey` is derived from this object, so every other catalog is required by
 * the compiler to define exactly these keys. Adding a string here and forgetting
 * to translate it is a type error, not a runtime fallback to English.
 *
 * Deliberately NOT in here (they stay English in both locales):
 *   - the product name, BatteryGPT
 *   - session status labels (Ready, Running, Idle …) — see lib/format.ts
 *   - keyboard key names, file paths like /workspace, units, and language names
 *
 * `{name}` placeholders are substituted by format() in ./index.ts.
 */
export const EN = {
  "perf.readFull": "Read full message · {count} characters",
  "perf.fullMessage": "Full message",
  "perf.previous": "Previous",
  "perf.next": "Next",
  "perf.trace": "Execution trace",
  "perf.fullTrace": "Read full execution detail",
  "perf.latest": "Latest",
  "perf.more": "Load more",
  "perf.retry": "Retry",
  "perf.preview": "Live preview · full reply available when complete",
  "perf.loading": "Loading…",
  "perf.close": "Close",
  "perf.download": "Download",

  // ── Sidebar ────────────────────────────────────────────────────────────────
  "sidebar.newSession": "New conversation",
  "sidebar.schedules": "Scheduled",
  "sidebar.primaryNavigation": "Primary navigation",
  "sidebar.creating": "Creating…",
  "sidebar.history": "History",
  "sidebar.sharedWithMe": "Shared with me",
  "sidebar.noSessions": "No sessions yet. Create one to get started.",
  "sidebar.collapse": "Collapse sidebar",
  "sidebar.expand": "Expand sidebar",
  "sidebar.search": "Search",
  "sidebar.searchTitle": "Search (⌘P / Ctrl+P)",
  "sidebar.searchSessions": "Search sessions and messages",
  "sidebar.signOut": "Sign out",
  "sidebar.sessionActions": "Session actions for {title}",
  "sidebar.sessionTitle": "Session title",
  "sidebar.rename": "Rename",
  "sidebar.share": "Share",
  "sidebar.delete": "Delete",
  "sidebar.collapseSection": "Collapse {label}",
  "sidebar.expandSection": "Expand {label}",
  "sidebar.activeSessions": "{count} active sessions",
  "sidebar.activeSession": "{count} active session",
  "sidebar.confirmClose":
    "Close session {id}? History and workspace files are retained.",
  "sidebar.createFailed": "Failed to create session",
  "sidebar.closeFailed": "Failed to close session",
  "sidebar.sharedReadOnly": "{title} · shared by {owner} · read-only",
  "sidebar.someoneElse": "another user",
  "sidebar.rowTitle": "{title} · {status} · {when}",

  // ── Connection status ──────────────────────────────────────────────────────
  "connection.connecting": "Connecting to event stream…",
  "connection.reconnecting": "Reconnecting…",
  "connection.disconnected": "Disconnected. Retrying.",

  // ── Conversation ───────────────────────────────────────────────────────────
  "conversation.noSessionTitle": "No session selected",
  "conversation.noSessionBody":
    "Pick a session from the list, or create a new one to start a conversation.",
  "conversation.emptyTitle": "What are we looking at today?",
  "conversation.emptyBody":
    "Describe the analysis you need, or start from one of these. The agent works in {path} inside its sandbox and streams results back here.",
  "conversation.sharedNoLive": "Shared sessions do not update live",
  "conversation.jumpToLatest": "Jump to latest",
  "conversation.interrupted": "This reply was cut short before it finished.",
  "conversation.continueTurn": "Continue",
  // Shown instead of the above when the sandbox itself was terminated. Says the
  // context is gone, so the user is not invited to trust state that no longer
  // exists.
  "conversation.interruptedSandbox":
    "The sandbox was terminated, so this task stopped. Workspace files were kept, but the execution context was lost.",
  "conversation.restartTurn": "Start over",
  // Sent as the user's message when they click Continue, so it reads naturally
  // in the transcript and tells the agent to resume rather than restart.
  "conversation.continuePrompt": "Please continue from where you left off.",
  // The sandbox-death counterpart: the agent has no memory of the previous run,
  // so it must inspect the workspace before doing anything else.
  "conversation.continuePromptAfterSandboxDeath":
    "The previous sandbox was terminated, so you no longer have your earlier execution context. Start by checking which files already exist in the workspace to work out how far the last run got, then carry on with what is left. Keep memory use down: process in batches and write results to disk as you go.",

  // ── Composer ───────────────────────────────────────────────────────────────
  "composer.placeholder": "Send a message…",
  "composer.sessionEnded": "Session ended. Start a new one to continue.",
  "composer.thinking": "Agent is thinking…",
  "composer.send": "Send",
  "composer.stop": "Stop",
  "composer.stopping": "Stopping…",
  "composer.stopFailed": "Unable to stop this turn",
  "composer.queuePlaceholder": "Send a follow-up — it runs after the current turn",
  "composer.queuedCount": "{count} queued",
  // generation.error toasts for faults the platform diagnosed itself. The raw
  // server text is an exit status or a recovery class; users need the consequence.
  "runtime.fault.sandboxLost":
    "The sandbox behind this session stopped. Your files are safe; the next message will start a fresh one (this takes a little longer).",
  "runtime.fault.sandboxOOM":
    "The sandbox ran out of memory and was stopped. Your files are safe; the next message will start a fresh one.",
  // Stop ends the current turn only. These say what happened to the follow-ups the
  // user had queued behind it: nothing. They stay queued and wait to be sent, so
  // the message has to rule out both "dropped" and "about to run on its own".
  "runtime.stopped.queueKeptOne": "Stopped. Your queued message is still waiting — send it when you are ready.",
  "runtime.stopped.queueKeptMany": "Stopped. Your {count} queued messages are still waiting — send them when you are ready.",
  "composer.wakingSandbox": "Waking the sandbox — the first reply after a pause takes a little longer…",
  "composer.queueCancel": "Remove from queue",
  "composer.queueCancelFailed": "That message is already running — use Stop instead",
  // The ↑ button on a queue row. Two labels because the same press does two
  // different things, and the difference is worth telling the user before they
  // click: with a turn running the text joins that turn (the work so far survives);
  // with nothing running it starts as a turn of its own.
  "composer.queueSteer": "Send into the current turn",
  "composer.queueSend": "Send now",
  "composer.queueSendFailed": "That message is no longer waiting — reloading the queue",
  // Shown on rows Stop parked, which are waiting on the user and not on the agent.
  // Without this they look like they are about to run, and the user waits forever.
  "composer.queueParked": "Waiting for you to send",
  "composer.queueSteeringPending": "Confirming delivery to the active turn",
  "composer.unavailable": "This session cannot accept messages.",
  "composer.hint": "Enter to send · Shift+Enter for newline",

  // ── Message actions ────────────────────────────────────────────────────────
  "message.goodResponse": "Good response",
  "message.badResponse": "Bad response",
  "message.copy": "Copy message",
  "message.copied": "Copied",
  "message.more": "More",
  "message.moreActions": "More actions",

  // ── Trace / steps ──────────────────────────────────────────────────────────
  "trace.steps": "{count} steps",
  "trace.step": "{count} step",
  "trace.showSteps": "Show steps",
  "trace.hideSteps": "Hide steps",
  "trace.input": "Input",
  "trace.output": "Output",
  "trace.error": "Error",

  // ── Workspace panel ────────────────────────────────────────────────────────
  "workspace.title": "Workspace",
  "workspace.noFilesTitle": "No files yet",
  "workspace.noFilesBody": "Files will appear here once a session is running.",
  "workspace.empty": "No artifacts yet. {brand} writes files to the workspace.",
  "workspace.noMatches": "No matching files.",
  "workspace.filter": "Filter files",
  "workspace.uploadFiles": "Upload files",
  "workspace.uploadFolder": "Upload folder",
  "workspace.uploadPaused": "Uploads are paused while the agent is working",
  "workspace.uploading": "Uploading {count} files…",
  "workspace.uploadingOne": "Uploading 1 file…",
  "workspace.uploadFailed": "Upload failed",
  "workspace.download": "Download",
  "workspace.resize": "Resize workspace panel",
  "workspace.resizeHint": "Drag to resize · double-click to reset",

  // ── Artifact viewer ────────────────────────────────────────────────────────
  "viewer.close": "Close preview",
  "viewer.fullScreen": "Full screen",
  "viewer.exitFullScreen": "Exit full screen",
  "viewer.edit": "Edit",
  "viewer.save": "Save",
  "viewer.saving": "Saving…",
  "viewer.cancel": "Cancel",
  "viewer.saved": "Saved",
  "viewer.saveFailed": "Save failed",
  "viewer.viewSource": "View source",
  "viewer.viewRendered": "View rendered",
  "viewer.loadFailed": "Could not load this file.",
  "viewer.tooLarge": "This file is too large to preview.",
  "viewer.binary": "This file cannot be previewed.",
  "viewer.downloadInstead": "Download it instead.",
  "viewer.loading": "Loading…",

  // ── Search palette ─────────────────────────────────────────────────────────
  "search.placeholder": "Search sessions and messages…",
  "search.empty": "Type to search.",
  "search.noResults": "No results.",
  "search.searching": "Searching…",
  "search.sessions": "Sessions",
  "search.messages": "Messages",
  "search.close": "Close search",

  // ── Notifications ──────────────────────────────────────────────────────────
  "notifications.title": "Notifications",
  "notifications.empty": "No notifications.",
  "notifications.markAllRead": "Mark all as read",
  "notifications.unread": "{count} unread",
  "notifications.close": "Close",

  // ── Share dialog ───────────────────────────────────────────────────────────
  "share.title": "Share session",
  "share.description":
    "Anyone with the link can read this session. They cannot send messages or change files.",
  "share.createLink": "Create link",
  "share.creating": "Creating…",
  "share.copyLink": "Copy link",
  "share.copied": "Link copied",
  "share.revoke": "Revoke",
  "share.revoking": "Revoking…",
  "share.revoked": "Link revoked",
  "share.noLink": "This session is not shared.",
  "share.close": "Close",
  "share.failed": "Something went wrong. Try again.",

  // ── Login ──────────────────────────────────────────────────────────────────
  "login.title": "Sign in",
  "login.user": "User",
  "login.password": "Password",
  "login.submit": "Sign in",
  "login.submitting": "Signing in…",
  "login.failed": "Login failed",
  "login.rateLimited": "Too many attempts. Try again later.",

  // ── Shell / boot errors ────────────────────────────────────────────────────
  "boot.title": "Backend unreachable",
  "boot.hint":
    "Start the orchestrator (./orchestrator) and the page will recover automatically.",
  "nav.sessions": "Sessions",
  "nav.files": "Files",

  // ── Shared (public read-only) view ─────────────────────────────────────────
  "shared.readOnly": "Read-only shared session",
  "shared.files": "Files",
  "shared.notFound": "This link is no longer valid.",
  "shared.expired": "This share link has expired or been revoked.",

  // ── Code blocks ────────────────────────────────────────────────────────────
  "code.copy": "Copy",
  "code.copied": "Copied",
  "code.copyCode": "Copy code",
  // ── Upload / save toasts ───────────────────────────────────────────────────
  "workspace.searchFiles": "Search files",
  "workspace.uploadBusy":
    "Can't upload while the agent is working. Try again once the turn finishes.",
  "workspace.tooManyFiles":
    "Too many files (limit {limit}). Select fewer files.",
  "workspace.fileTooLarge": "{name} is larger than 100 MiB.",
  "workspace.uploadedOne": "Uploaded {name}",
  "workspace.uploadedMany": "Uploaded {count} files",
  "workspace.uploadLimit": "Upload exceeds the size or file-count limit.",
  "workspace.uploadFailedReason": "Upload failed: {error}",
  "workspace.readOnlyShared":
    "This session was shared with you and is read-only.",
  "workspace.dropReadFailed": "Couldn't read the dropped files.",
  "viewer.backToFiles": "Back to files",
  "viewer.cancelEdit": "Cancel edit",
  "viewer.fullScreenPreview": "Full screen preview",
  "viewer.exitFullScreenDock": "Exit full screen (dock to sidebar)",
  "viewer.openRaw": "Open raw",
  "viewer.savedFile": "Saved {name}",
  "viewer.saveBusy":
    "Can't save while the agent is working. Try again once the turn finishes.",
  "viewer.saveTooLarge": "File is too large to save (limit 1 MiB).",
  "viewer.saveFailedReason": "Save failed: {error}",
  "trace.executionTrace": "Execution trace",
  "trace.stepError": "A step reported an error",
  "trace.tools": "{count} tools",
  "trace.tool": "{count} tool",
  "search.recent": "Recent",
  "search.noMatches": "No matches found.",
  "search.noSessionsYet": "No sessions yet.",
  "login.signingIn": "Signing in…",
  "share.people": "People",
  "share.add": "Add",
  "share.username": "Username",
  "share.noOtherUsers": "No other users configured",
  "share.visibility": "Visibility",
  "share.linkVisibility": "Link visibility",
  "share.summary": "Summary",
  "share.full": "Full",
  "share.publicLink": "Public link",
  "share.publicLinkBody":
    "Anyone with the link can read this conversation without logging in. The link expires, and shows the conversation as it stands now — later messages are not included.",
  "share.shellAlwaysFull":
    "Shell sessions are always shared in full: the command output is the content.",
  "share.fullIncludes":
    "Full includes shell commands, file contents and queries from the execution trace.",
  "share.filesNote":
    "Only what you check is reachable through the link, and a copy is frozen now — later changes to these files will not appear. Nothing checked means the conversation only.",
  "share.replaceLink": "Replace link",
  "share.replaceLinkTitle": "Issue a new link and invalidate the old one",
  "share.revokeLink": "Revoke link",
  "share.oldLinkInvalidated": "Old link invalidated",
  "share.copyFailed": "Could not copy the link",
  "common.close": "Close",
  "common.closeTab": "Close tab",
  "common.closePanel": "Close panel",
  "common.loadingConversation": "Loading conversation",
  "common.loadingSessions": "Loading sessions",
  "search.results": "Search results",
  "trace.toolError": "Tool reported an error",
  "message.copiedSessionId": "Copied session id",
  "message.copiedMarkdown": "Copied as Markdown",
  "prompt.acdc.title": "Compare datasets",
  "prompt.acdc.body":
    "Compare two uploaded datasets using their actual columns.",
  "prompt.acdc.prompt":
    "Compare the two datasets in my workspace. Identify common columns, summarize meaningful differences, and state any assumptions before calculating.",
  "prompt.startsoc.title": "Explore a distribution",
  "prompt.startsoc.body":
    "Summarize a numeric column and explain missing values.",
  "prompt.startsoc.prompt":
    "Help me explore a numeric column in my dataset. Ask which file and column to use, then summarize its distribution, outliers, and missing values.",
  "prompt.depth.title": "Create a comparison report",
  "prompt.depth.body":
    "Summarize selected metrics across groups.",
  "prompt.depth.prompt":
    "Create a comparison report from the data I provide. Confirm the grouping and metrics, produce a table and chart, and save the outputs in the workspace.",
  "prompt.faults.title": "Investigate errors",
  "prompt.faults.body":
    "Find recurring errors in the logs I provide.",
  "prompt.faults.prompt":
    "Review the logs in my workspace, group recurring errors, and explain the evidence for the most frequent causes. Do not assume access to external systems.",
  "login.subtitle": "Enter your workbench credentials.",
  "workspace.uploadsPausedNote": "Uploads paused while the agent is working.",
  "viewer.binaryTitle": "Binary file preview is not supported.",
  "viewer.binaryHint":
    "Use the header actions to open or download the raw bytes.",
  "viewer.noRows": "No rows to preview.",
  "mentioned.title": "In this reply",
  "mentioned.allFiles": "All files",
  "mentioned.collapse": "Hide the files this reply mentions",
  "mentioned.expand": "Show the files this reply mentions",
  // ── Scheduled tasks ───────────────────────────────────────────────────────
  "schedule.title": "Automation",
  "schedule.subtitle": "Run agent workflows on a reliable cadence.",
  "schedule.pageTitle": "Scheduled tasks",
  "schedule.pageSubtitle":
    "Let {brand} schedule tasks, set reminders or monitor updates.",
  "schedule.create": "Create",
  "schedule.createBlank": "Blank task",
  "schedule.createFromSuggestion": "Start from a suggestion",
  "schedule.new": "New scheduled task",
  "schedule.active": "Active",
  "schedule.archived": "Archived",
  "schedule.scheduled": "Scheduled",
  "schedule.suggestions": "Suggestions",
  "schedule.filterAll": "All",
  "schedule.filterEnabled": "Enabled",
  "schedule.filterPaused": "Paused",
  "schedule.filterCompleted": "Completed",
  "schedule.search": "Search tasks",
  "schedule.searchPlaceholder": "Search by name or prompt…",
  "schedule.noActive": "No active tasks",
  "schedule.noArchived": "No archived tasks",
  "schedule.noScheduled": "No scheduled tasks yet",
  "schedule.createFirst": "Create your first task",
  "schedule.templateTitle": "Daily briefing",
  "schedule.templateBody":
    "Start with a morning summary of the things that matter today.",
  "schedule.templatePrompt":
    "Give me a morning briefing with my calendar, important unread mail, and anything I should pay attention to today.",
  "schedule.suggestionDaily": "Daily briefing",
  "schedule.suggestionDailyBody":
    "Summarize the calendar, unread mail and priorities I should know about.",
  "schedule.suggestionDailyMeta": "Weekdays · 8:00",
  "schedule.suggestionWeekly": "Weekly review",
  "schedule.suggestionWeeklyBody":
    "Turn the past week's work into a concise status update.",
  "schedule.suggestionWeeklyMeta": "Friday · 16:00",
  "schedule.suggestionMonitor": "Follow-up monitor",
  "schedule.suggestionMonitorBody":
    "Watch recent activity and flag anything that needs my attention.",
  "schedule.suggestionMonitorMeta": "Weekdays · 9:00",
  "schedule.selectTitle": "Choose a task",
  "schedule.selectBody":
    "Create, edit and monitor automations from one focused workspace.",
  "schedule.closeDetails": "Close task details",
  "schedule.edit": "Edit",
  "schedule.archive": "Archive",
  "schedule.restore": "Restore",
  "schedule.delete": "Delete permanently",
  "schedule.confirmArchive": "Archive “{name}”?",
  "schedule.confirmRestore": "Restore “{name}”?",
  "schedule.confirmDelete": "Permanently delete “{name}”?",
  "schedule.enabled": "Enabled",
  "schedule.paused": "Paused",
  "schedule.archivedStatus": "Archived",
  "schedule.manual": "Manual only",
  "schedule.agent": "Agent",
  "schedule.successRate": "Success rate",
  "schedule.nextRun": "Next run",
  "schedule.nextRunToday": "Today",
  "schedule.nextRunTomorrow": "Tomorrow",
  "schedule.nextRunInHours": "In {count} hr",
  "schedule.nextRunInMinutes": "In {count} min",
  "schedule.nextRunInDays": "In {count} days",
  "schedule.nextRunUnavailable": "No next run",
  "schedule.lastRun": "Last run",
  "schedule.never": "Not run yet",
  "schedule.timezone": "Timezone",
  "schedule.result": "Output",
  "schedule.newSession": "New session each run",
  "schedule.existingSession": "Existing session",
  "schedule.timeout": "Timeout",
  "schedule.retries": "Retries",
  "schedule.retryBackoff": "Retry delay",
  "schedule.prompt": "Task prompt",
  "schedule.runHistory": "Run history",
  "schedule.runCount": "{count} runs",
  "schedule.noRuns": "No runs recorded yet.",
  "schedule.openSession": "Open linked session",
  "schedule.noSession": "No session linked",
  "schedule.refresh": "Refresh",
  "schedule.runNow": "Run now",
  "schedule.running": "Running…",
  "schedule.pause": "Pause",
  "schedule.enable": "Enable",
  "schedule.validationRequired": "Add a name and prompt to continue.",
  "schedule.validationTarget":
    "Choose the session that should receive the output.",
  "schedule.error": "Something went wrong. Try again.",
  "schedule.editor.new": "New task",
  "schedule.editor.edit": "Edit task",
  "schedule.editor.configuration": "Task configuration",
  "schedule.editor.name": "Name",
  "schedule.editor.namePlaceholder": "e.g. Daily briefing",
  "schedule.editor.promptPlaceholder":
    "Tell the agent what to do each time it runs",
  "schedule.editor.frequency": "Frequency",
  "schedule.editor.manual": "Manual",
  "schedule.editor.manualDescription": "Run on demand from the task details.",
  "schedule.editor.interval": "Interval",
  "schedule.editor.intervalDescription": "Repeat every few minutes.",
  "schedule.editor.daily": "Daily",
  "schedule.editor.dailyDescription": "Run at a fixed time every day.",
  "schedule.editor.weekdays": "Weekdays",
  "schedule.editor.weekdaysDescription": "Run Monday through Friday.",
  "schedule.editor.weekly": "Weekly",
  "schedule.editor.weeklyDescription": "Run on one day each week.",
  "schedule.editor.custom": "Custom",
  "schedule.editor.customDescription": "Fine-tune hours, days or months.",
  "schedule.editor.manualHint":
    "This task will not run automatically. You can always run it from the details view.",
  "schedule.editor.everyMinutes": "Repeat every (minutes)",
  "schedule.editor.intervalHint": "Use 1–59 minutes for polling and reminders.",
  "schedule.editor.runTime": "Run time",
  "schedule.editor.runDay": "Run day",
  "schedule.editor.repeatType": "Repeat type",
  "schedule.editor.hourly": "Hourly",
  "schedule.editor.dailyRepeat": "Every N days",
  "schedule.editor.weeklyRepeat": "Selected weekdays",
  "schedule.editor.monthly": "Monthly",
  "schedule.editor.yearly": "Yearly",
  "schedule.editor.everyHours": "Repeat every (hours)",
  "schedule.editor.minuteOfHour": "At minute",
  "schedule.editor.everyDays": "Repeat every (days)",
  "schedule.editor.everyMonths": "Repeat every (months)",
  "schedule.editor.dayOfMonth": "Day of month",
  "schedule.editor.month": "Month",
  "schedule.editor.day": "Day",
  "schedule.editor.atLeastOneDay": "Select at least one day.",
  "schedule.editor.customHint":
    "Dates that do not exist in a month are skipped. Times use the selected timezone.",
  "schedule.editor.timeout": "Timeout (seconds)",
  "schedule.editor.maxRetries": "Maximum retries",
  "schedule.editor.retryBackoff": "Retry delay (seconds)",
  "schedule.editor.output": "Write results to",
  "schedule.editor.outputNew": "New session",
  "schedule.editor.outputNewDescription":
    "Keep an independent context for every run.",
  "schedule.editor.outputExisting": "Existing session",
  "schedule.editor.outputExistingDescription":
    "Reuse a session's context and workspace files.",
  "schedule.editor.targetSession": "Target session",
  "schedule.editor.chooseSession": "Choose a session",
  "schedule.editor.timezone": "Timezone",
  "schedule.editor.timezoneHint": "Times are evaluated in this IANA timezone.",
  "schedule.editor.cronPreview": "Cron preview",
  "schedule.editor.rawCron": "Advanced cron expression",
  "schedule.editor.rawCronHint":
    "An unsupported expression is preserved until you change the frequency.",
  "schedule.editor.cancel": "Cancel",
  "schedule.editor.save": "Save changes",
  "schedule.editor.create": "Create task",
  "schedule.status.queued": "Queued",
  "schedule.status.running": "Running",
  "schedule.status.succeeded": "Succeeded",
  "schedule.status.failed": "Failed",
  "schedule.status.timed_out": "Timed out",
  "schedule.status.canceled": "Canceled",
  "schedule.status.skipped_busy": "Skipped because previous run is still active",
  "schedule.weekday.mon": "Mon",
  "schedule.weekday.tue": "Tue",
  "schedule.weekday.wed": "Wed",
  "schedule.weekday.thu": "Thu",
  "schedule.weekday.fri": "Fri",
  "schedule.weekday.sat": "Sat",
  "schedule.weekday.sun": "Sun",
  "schedule.monthName": "Month {value}",
  "schedule.dayName": "Day {value}",
  "schedule.minutesAgo": "{count} min ago",
  "schedule.minuteAgo": "{count} min ago",
  "schedule.hoursAgo": "{count} hr ago",
  "schedule.hourAgo": "{count} hr ago",
  "schedule.daysAgo": "{count} days ago",
  "schedule.dayAgo": "{count} day ago",
  "schedule.justNow": "Just now",
} as const;

/** Every translatable key in the app. */
export type MessageKey = keyof typeof EN;
