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
  /**
   * Whether a sandbox currently backs the session. Independent of `status`:
   * running_idle covers both a warm sandbox and one that died or was released,
   * which differ by a cold start on the next message. Absent on payloads that
   * did not look it up (create, rename) — keep the current value then.
   */
  sandbox?: SandboxState;
};

/**
 * The coarse activity of an executing turn, as recorded on the turn row by the
 * orchestrator. Mirrors store.TurnPhase.
 */
export type TurnPhase = "thinking" | "tool" | "streaming" | "compacting" | "retrying";

export type ApiTurn = {
  id: number;
  sequence: number;
  content: string;
  status: "queued" | "leased" | "running" | "completed" | "failed" | "canceled";
  /** Present on the active turn only; a queued turn has not run. */
  phase?: TurnPhase;
  phase_at?: string;
  tool_name?: string;
  tool_detail?: string;
  started_at?: string;
  created_at: string;
  /**
   * The worker will not claim this turn until the user releases it — the state
   * Stop leaves the rest of the queue in. A parked row is waiting on the user, not
   * on the agent, so it renders differently: presenting it as "up next" would be a
   * promise nothing is going to keep.
   */
  parked?: boolean;
};

/**
 * The session's runtime projection: the single authority on whether the agent is
 * busy, what it is doing, and what the user has queued behind it.
 *
 * Served identically by GET /api/sessions/{id}/runtime and the `session.runtime`
 * event, so there is one shape to apply however it arrives.
 */
export type SandboxState = "live" | "starting" | "released";

export type ApiSessionRuntime = {
  session_id: string;
  session_status: SessionStatus;
  /** See ApiSession.sandbox; carried here because every status change pushes or re-reads this. */
  sandbox?: SandboxState;
  /**
   * Monotonic per session. Apply a snapshot only when its revision exceeds the
   * one already held: an in-flight GET can otherwise land after a newer push and
   * resurrect a drained queue entry, which then never clears.
   */
  revision: number;
  /**
   * The turn the agent is executing, or null when idle. This — not
   * `session_status` — defines "busy": a session stays running_active while a
   * queued turn waits for a generation, and reports running_active before any
   * turn is claimed.
   */
  active_turn: ApiTurn | null;
  /** The user's pending turns, in execution order. */
  queued_turns: ApiTurn[];
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
  message_pages?: boolean;
  artifact_pages?: boolean;
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
  message_pages?: boolean;
  artifact_pages?: boolean;
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
  detail_index?: number;
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
  /**
   * The durable turn this user message belongs to, when it has one (absent for
   * assistant/system messages and pre-migration history). Lets the UI correlate a
   * transcript bubble with turn state by id instead of matching on content.
   */
  turn_id?: number;
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
  /** Present on bounded transcript pages. */
  content_truncated?: boolean;
  content_length?: number;
  has_trace?: boolean;
  is_final_reply?: boolean;
};

export type MessagePage = {
  messages: ApiMessage[] | null;
  has_older: boolean;
  has_newer: boolean;
  latest_id: number;
  before_id: number;
  after_id: number;
  latest_final_id: number;
  interruption: "" | "model" | "sandbox";
};

export type ApiSchedule = {
  id: string; user_id: string; name: string; prompt: string; cron_expr?: string;
  timezone: string; enabled: boolean; mode: "agent"; timeout_seconds: number;
  max_retries: number; retry_backoff_seconds: number; next_run_at?: string | null;
  last_run_at?: string | null; version: number; created_at: string; updated_at: string;
  archived?: boolean;
  result_mode?: "new_session" | "existing_session";
  target_session_id?: string | null;
};
export type ApiScheduleRun = {
  id: string; schedule_id: string; user_id: string; session_id?: string; turn_id?: number;
  scheduled_at: string; started_at?: string; finished_at?: string; status: "queued"|"running"|"succeeded"|"failed"|"timed_out"|"canceled"|"skipped_busy";
  attempt: number; error_class?: string; error?: string; output_summary?: string; created_at: string;
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
