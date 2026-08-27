export type SessionStatus =
  | "created"
  | "running_active"
  | "running_idle"
  | "checkpointing"
  | "checkpointed"
  | "failed"
  | "destroyed";

/** How the caller reaches a session: their own, or shared read-only with them. */
export type SessionAccess = "owner" | "reader";

export type ApiSession = {
  id: string;
  /** Absent on sessions shared with the caller: the owner's identity is not exposed. */
  user_id?: string;
  /** Present on /api/sessions listings. Absent means treat as "owner". */
  access?: SessionAccess;
  /** Who shared the session, on reader rows only. */
  shared_by?: string;
  /** The grant's id, on reader rows only. */
  share_id?: string;
  title?: string;
  status: SessionStatus;
  mode: SessionMode;
  mode_label: string;
  restore_ms?: number | null;
  created_at: string;
  updated_at: string;
  expires_at?: string | null;
  ended_at?: string | null;
  last_activity_at?: string | null;
  auto_checkpoint_enabled?: boolean;
  failure_reason?: string;
  error_class?: string;
};

export type SessionMode = "agent" | "shell";

/**
 * How much of a session a share exposes. `summary` keeps the conversation but
 * drops execution traces and intermediate tool steps; `full` includes them.
 */
export type ShareVisibility = "summary" | "full";

export type ApiSessionShare = {
  id: string;
  session_id: string;
  grantee: string;
  role: "reader";
  visibility: ShareVisibility;
  created_by: string;
  created_at: string;
};

/** Owner-facing view of a public link. The token is never included here. */
export type ApiShareLink = {
  id: string;
  session_id: string;
  visibility: ShareVisibility;
  created_by: string;
  created_at: string;
  expires_at: string;
  expired: boolean;
  last_accessed_at?: string | null;
  access_count: number;
  max_message_id?: number | null;
  file_count: number;
};

/**
 * The one and only time a raw link token is returned. Show it, let the user copy
 * it, and do not persist it anywhere you cannot clear: it is a bearer credential
 * and the server keeps only its hash.
 */
export type ShareLinkToken = {
  link?: ApiShareLink;
  token: string;
  path: string;
};

/** The trimmed session a public link exposes. Note there is no `user_id`. */
export type SharedLinkSession = {
  id: string;
  title: string;
  mode: SessionMode;
  mode_label: string;
  /** Coarsened on the server: "active" | "idle" | "ended". */
  status: "active" | "idle" | "ended";
  created_at: string;
  updated_at: string;
  visibility: ShareVisibility;
  link_expires_at: string;
  shared_at: string;
  /** True when the link is pinned to the conversation as of `frozen_at`. */
  frozen: boolean;
  frozen_at?: string | null;
};

export type SharedLinkMessage = {
  id: number;
  role: "user" | "assistant" | string;
  content: string;
  created_at: string;
  /** Same values as ApiMessage.stop_reason; "toolUse" never appears at summary visibility. */
  stop_reason?: "stop" | "toolUse" | "error" | "sandbox_died" | "";
  trace?: TraceNode[];
};

export type SharedLinkFile = {
  path: string;
  size: number;
};

export type DeploymentSessionMode = {
  mode: SessionMode;
  label: string;
  visible: boolean;
  create_enabled: boolean;
  disabled_reason: string | null;
};

export type DeploymentCapabilities = {
  schema_version: 1;
  default_mode: SessionMode;
  session_modes: DeploymentSessionMode[];
};

export type ApiArtifact = {
  session_id: string;
  path: string;
  size: number;
  mod_time: string;
  created_at: string;
  updated_at: string;
};

export type TraceDiff = {
  path?: string;
  content?: string;
  edits?: { oldText: string; newText: string }[];
};

export type TraceNode = {
  kind: "thinking" | "tool";
  toolName?: string;
  label?: string;
  args?: Record<string, unknown>;
  resultSummary?: string;
  isError?: boolean;
  startedAt?: string;
  durationMs?: number;
  diff?: TraceDiff;
};

export type ApiMessage = {
  id: number;
  session_id: string;
  role: "user" | "assistant" | string;
  content: string;
  created_at: string;
  // pi's terminator for an assistant message: "stop" = a turn's final reply,
  // "toolUse" = an intermediate tool-calling step, "error" = the model call
  // failed and the reply was cut short, "sandbox_died" = the sandbox itself was
  // terminated (e.g. memory limit) so the agent's context did not survive,
  // "" / absent = unknown (user messages, salvaged text, pre-migration history).
  stop_reason?: "stop" | "toolUse" | "error" | "sandbox_died" | "";
  trace?: TraceNode[];
  feedback?: "up" | "down" | "";
};

export type HarnessEvent = {
  event_id?: number;
  type: string;
  session_id?: string;
  turn_id?: number;
  output_sequence?: number;
  proxy_request_id?: string;
  stream?: string;
  severity?: string;
  time?: string;
  payload?: unknown;
};

export type ConnectionStatus = "idle" | "connecting" | "live" | "reconnecting" | "down";

export type ApiNotification = {
  id: string;
  title: string;
  body: string;
  created_at: string;
  read: boolean;
};

// Command-palette search (GET /api/search). Contract is frozen; see the
// SearchPalette component for how snippet ⟦…⟧ markers are rendered.
export type SearchHit = {
  kind: "title" | "message";
  session_id: string;
  title: string;
  status: string;
  mode: string;
  message_id?: number;
  role?: "user" | "assistant";
  // For message hits: a clipped excerpt with match markers ⟦ and ⟧ around the
  // matched substring. For title hits: the title itself.
  snippet: string;
  created_at: string;
};

export type SearchResponse = {
  results: SearchHit[];
  truncated: boolean;
};
