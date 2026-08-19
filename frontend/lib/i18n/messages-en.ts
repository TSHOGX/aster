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
  // ── Sidebar ────────────────────────────────────────────────────────────────
  "sidebar.newSession": "New session",
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
  // Sent as the user's message when they click Continue, so it reads naturally
  // in the transcript and tells the agent to resume rather than restart.
  "conversation.continuePrompt": "Please continue from where you left off.",

  // ── Composer ───────────────────────────────────────────────────────────────
  "composer.placeholder": "Send a message…",
  "composer.sessionEnded": "Session ended. Start a new one to continue.",
  "composer.thinking": "Agent is thinking…",
  "composer.send": "Send",
  "composer.hint": "Enter to send · Shift+Enter for newline",
  "composer.busy": "Session is busy or ended.",

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
  "workspace.uploadBusy": "Can't upload while the agent is working. Try again once the turn finishes.",
  "workspace.tooManyFiles": "Too many files (limit {limit}). Select fewer files.",
  "workspace.fileTooLarge": "{name} is larger than 100 MiB.",
  "workspace.uploadedOne": "Uploaded {name}",
  "workspace.uploadedMany": "Uploaded {count} files",
  "workspace.uploadLimit": "Upload exceeds the size or file-count limit.",
  "workspace.uploadFailedReason": "Upload failed: {error}",
  "workspace.readOnlyShared": "This session was shared with you and is read-only.",
  "workspace.dropReadFailed": "Couldn't read the dropped files.",
  "viewer.backToFiles": "Back to files",
  "viewer.cancelEdit": "Cancel edit",
  "viewer.fullScreenPreview": "Full screen preview",
  "viewer.exitFullScreenDock": "Exit full screen (dock to sidebar)",
  "viewer.openRaw": "Open raw",
  "viewer.savedFile": "Saved {name}",
  "viewer.saveBusy": "Can't save while the agent is working. Try again once the turn finishes.",
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
  "share.publicLinkBody": "Anyone with the link can read this conversation without logging in. The link expires, and shows the conversation as it stands now — later messages are not included.",
  "share.shellAlwaysFull": "Shell sessions are always shared in full: the command output is the content.",
  "share.fullIncludes": "Full includes shell commands, file contents and queries from the execution trace.",
  "share.filesNote": "Only what you check is reachable through the link, and a copy is frozen now — later changes to these files will not appear. Nothing checked means the conversation only.",
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
  "prompt.acdc.title": "AC vs. DC session length",
  "prompt.acdc.body": "Compare how long an AC session runs against a DC session, bucketed by duration, to see where each mode actually lands.",
  "prompt.acdc.prompt": "For July, compare the distribution of charging session duration between AC (slow) and DC (fast) charging. Bucket sessions as <10 min, 10–30 min, 30–60 min, 1–2 h, >2 h and report the share of each bucket per mode.",
  "prompt.startsoc.title": "Starting SOC distribution",
  "prompt.startsoc.body": "Bucket the SOC users plug in at, from below 10% up to 50% and above, to tell topping-up apart from running the pack down.",
  "prompt.startsoc.prompt": "Show the distribution of starting SOC across charging sessions, bucketed as <10%, 10–20%, 20–30%, 30–40%, 40–50%, ≥50%, with the session count per bucket. Do users mostly top up shallow or charge from deep discharge?",
  "prompt.depth.title": "Charge depth by platform",
  "prompt.depth.body": "Put the platforms side by side on energy per charge, depth of discharge and peak temperature, and name the one charging deepest.",
  "prompt.depth.prompt": "For July, compare the platforms on average energy added per charging session (kWh), average depth of discharge (DOD), and peak temperature. Summarize them in one table and tell me which platform charges the deepest.",
  "prompt.faults.title": "F2 BMS fault mix",
  "prompt.faults.body": "Rank the BMS faults the F2 fleet reported in the last day and name the one that dominates.",
  "prompt.faults.prompt": "Look at BMS faults reported on the F2 platform over the last day. Break them down by fault, give the count and share of each, and tell me which fault dominates.",
  "login.subtitle": "Enter your workbench credentials.",
  "workspace.uploadsPausedNote": "Uploads paused while the agent is working.",
  "viewer.binaryTitle": "Binary file preview is not supported.",
  "viewer.binaryHint": "Use the header actions to open or download the raw bytes.",
  "viewer.noRows": "No rows to preview.",
  "mentioned.title": "In this reply",
  "mentioned.allFiles": "All files",
  "mentioned.collapse": "Hide the files this reply mentions",
  "mentioned.expand": "Show the files this reply mentions"
} as const;

/** Every translatable key in the app. */
export type MessageKey = keyof typeof EN;
