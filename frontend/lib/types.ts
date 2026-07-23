export type SessionStatus =
  | "created"
  | "running_active"
  | "running_idle"
  | "checkpointing"
  | "checkpointed"
  | "failed"
  | "destroyed";

export type ApiSession = {
  id: string;
  user_id: string;
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
  // "toolUse" = an intermediate tool-calling step, "" / absent = unknown
  // (user messages, salvaged text, pre-migration history).
  stop_reason?: "stop" | "toolUse" | "";
  trace?: TraceNode[];
  feedback?: "up" | "down" | "";
};

export type StreamLineKind = "stdout" | "stderr" | "runtime";

export type StreamLine = {
  id: string;
  session_id: string;
  stream: StreamLineKind | string;
  line: string;
  time: string;
};

export type DeltaPayload = {
  message_id: string;
  text: string;
  delta_type?: string;
  index?: number;
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
