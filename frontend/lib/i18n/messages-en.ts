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
  "prompt.fade.title": "Capacity fade analysis",
  "prompt.fade.body": "Plot capacity retention against cycle number, fit the fade curve, and report the projected cycle count at 80% state of health.",
  "prompt.fade.prompt": "I have cycling data in /workspace. Load it, plot capacity retention vs. cycle number, fit the fade trend, and estimate the cycle count at 80% SOH.",
  "prompt.dqdv.title": "dQ/dV peak tracking",
  "prompt.dqdv.body": "Compute differential capacity curves across cycles and track how the peaks shift to separate loss of lithium inventory from loss of active material.",
  "prompt.dqdv.prompt": "Compute dQ/dV curves from the charge data in /workspace, plot them by cycle, and track the peak positions to identify the dominant degradation mode.",
  "prompt.thermal.title": "Thermal log review",
  "prompt.thermal.body": "Scan a temperature log for excursions, correlate them with C-rate, and summarize which steps ran hottest.",
  "prompt.thermal.prompt": "Review the thermal log in /workspace: find temperature excursions, correlate them with C-rate and step type, and summarize the hottest steps in a table.",
  "prompt.compare.title": "Compare cell variants",
  "prompt.compare.body": "Put several cells or formulations side by side on the metrics that matter and call out where they diverge.",
  "prompt.compare.prompt": "Compare the cells in /workspace on capacity, coulombic efficiency, and internal resistance. Produce a comparison table and flag where the variants diverge.",
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
