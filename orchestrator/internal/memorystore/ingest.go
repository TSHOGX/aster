package memorystore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// THE INGEST BOUNDARY
//
// Three places read CONTROL-plane data in order to produce MEMORY data. With one
// database those were single SQL statements; across two files they cannot be, so
// the boundary is made explicit here rather than left implicit in a store method
// that happens to touch two schemas.
//
// Shape: a narrow CONSUMER-SIDE interface, declared by the consumer (this
// package) and describing exactly the control data the pipeline needs — nothing
// more. internal/store does not know this interface exists; an adapter in
// internal/controlsource implements it in terms of *store.Store. Consequences
// that were the point of doing it this way:
//
//   - The dependency is unidirectional, control -> memory. memorystore does not
//     import store, so a control-plane refactor cannot ripple into the memory
//     half, and no memory concept can leak into the control schema.
//   - The memory pipeline never holds a control-store handle, so there is no
//     path by which a distillation sweep can accidentally issue a control-plane
//     write or take the control write lock. Everything it can do to the control
//     plane is the two read methods below.
//   - The interface is trivially fakeable, which is what lets the memorystore
//     tests exercise the projection without opening a control database at all.
//
// The transcript type is a flat value, not a cursor: the control read completes
// and is handed over before any memory write starts. That is not a regression —
// SnapshotSession already performed its control reads outside its write
// transaction — and it is what makes the two databases independent rather than
// interleaved.
// ============================================================================

// SessionSource is the control-plane data the memory pipeline consumes. It is
// the ENTIRE surface the memory half has on the control plane; both methods are
// reads, and there is deliberately no write method and no way to reach the
// underlying control store.
type SessionSource interface {
	// SessionTranscript returns everything needed to project one session into
	// the normalized event stream: the session's identity/status fields, its
	// full message list with traces, and its turn count.
	SessionTranscript(ctx context.Context, sessionID string) (SessionTranscript, error)

	// ListPiSessionsForCapture enumerates pi sessions that have a provisioned
	// driver home, across all users, for full-fidelity native capture. When
	// since is non-zero only sessions that ended at/after it (or never recorded
	// an ended_at) are returned.
	ListPiSessionsForCapture(ctx context.Context, since time.Time) ([]PiSessionForCapture, error)
}

// SessionTranscript is one session's control-plane state at the instant the
// ingest read completed.
//
// Every field is a POINT-IN-TIME COPY across a boundary with no referential
// integrity (see the cross-database note in schema.go). By the time these values
// reach a memory table the session may have advanced, been failed, or been
// destroyed. See the torn-read discussion on SnapshotSession.
type SessionTranscript struct {
	SessionID string
	// Status/UserID/Agent/CreatedAt/EndedAt are copied verbatim into
	// trajectory_snapshots.
	Status    string
	UserID    string
	Agent     string
	CreatedAt time.Time
	EndedAt   *time.Time
	// TurnCount is COUNT(*) over the session's turns, the fingerprint's primary
	// input and the lens watermark's comparison value.
	TurnCount int
	// Messages is the session's full ordered message list, each with its
	// execution trace when one was recorded.
	Messages []TranscriptMessage
}

// TranscriptMessage is one control-plane message as the projection needs it. It
// is a narrowed copy of store.Message: the projection reads role, content,
// created_at, the message id (kept as provenance on each event), and the trace
// node array. Fields the projection does not use (stop_reason, feedback) are
// deliberately absent, so the boundary states its own requirements.
type TranscriptMessage struct {
	ID        int64
	Role      string
	Content   string
	CreatedAt time.Time
	// Trace is the raw execution-trace node array (message_traces.nodes), or nil
	// when the message has none.
	Trace json.RawMessage
}

// PiSessionForCapture is one row of the enumeration a full-fidelity trajectory
// capture walks: everything needed to locate a session's native pi JSONL on the
// host without any further per-session query.
//
// It lives on the memory side even though every field is read from control
// tables, because it exists solely to drive memory writes: it is the shape the
// capture pipeline asks the boundary for, not a control-plane concept. The
// control-side query that populates it (a 3-way join over sessions /
// session_driver_homes / session_driver_states) stays in internal/store, where
// those tables live.
type PiSessionForCapture struct {
	SessionID string
	UserID    string
	Status    string
	// AgentHomeHostPath is the persistent host dir bind-mounted at /agent-home
	// (session_driver_homes.host_path, e.g. <AgentHomesRoot>/<session>/pi). The
	// native pi sessions live under <AgentHomeHostPath>/.pi/agent/sessions/.
	AgentHomeHostPath string
	// SelectedSessionRelpath is the canonical native session file pi reported at
	// the last completed turn (relative to .pi/agent/sessions/). Empty when the
	// session never advanced to pi_session state; callers may glob instead.
	SelectedSessionRelpath string
	// NativeSessionID is pi's own session uuid (selected_session_id), when known.
	NativeSessionID string
	EndedAt         *time.Time
}

// Ingestor is the control -> memory projection. It is the only type that holds
// both a SessionSource and a memory Store, which keeps "reads control, writes
// memory" in exactly one place instead of spread across the pipeline.
type Ingestor struct {
	store *Store
	src   SessionSource
}

// NewIngestor pairs a memory store with a control-plane source. A nil src yields
// a nil Ingestor, so a caller with no control access simply skips projection
// (the native-capture path does not need it).
func NewIngestor(store *Store, src SessionSource) *Ingestor {
	if store == nil || src == nil {
		return nil
	}
	return &Ingestor{store: store, src: src}
}

// SnapshotSession normalizes a session's durable control-plane messages/traces
// into the rebuildable trajectory_events stream and upserts its
// trajectory_snapshots index row. It never touches the distilled memory/skill
// tables.
//
// Normalization applies the 7-primitive taxonomy: each user message becomes one
// "user" event; each assistant message first emits its trace nodes in order (a
// thinking node -> one "thinking" event; a tool node -> a "tool_call" then a
// "tool_result" event sharing a synthesized call_id) then the assistant text as
// an "assistant" event. idx is a 0-based running counter across the session.
//
// TORN READ ACROSS THE BOUNDARY. The control read (SessionTranscript) and the
// memory write are now separated by a database boundary, so a session can change
// — including being destroyed — in between. The window was always nonzero (the
// control reads were already outside the write transaction before the split);
// what changed is that no single transaction could ever have covered it. The
// consequences and why they are tolerable:
//
//   - The copied status/ended_at can be stale by one transition. Nothing reads
//     them as live session state; they are snapshot-time metadata, and the next
//     sweep overwrites them.
//   - A session destroyed mid-ingest leaves a trajectory_snapshots row for a
//     session the control plane considers gone. Because nothing hard-deletes
//     sessions (only status flips), the control row still exists, so this is a
//     stale copy rather than a dangling reference.
//   - Messages added after the read are simply not in this snapshot. The
//     fingerprint (turn count + last message id/time) will differ from the
//     recomputed one on the next sweep, and ListSessionsForDistillation
//     re-surfaces the session, so it converges.
//
// The normalized_at IS NULL incomplete-rebuild marker still works exactly as
// before: it is cleared in the same transaction as the DELETE and re-stamped only
// with the final chunk, and both of those transactions are now on the MEMORY
// database. The marker never depended on control-plane atomicity, so moving the
// event tables to their own file leaves it intact — and it is now strictly
// stronger, since a control-plane crash can no longer roll back a completed
// memory rebuild.
func (i *Ingestor) SnapshotSession(ctx context.Context, sessionID string) error {
	if i == nil {
		return fmt.Errorf("snapshot session %q: no control-plane source configured", sessionID)
	}
	transcript, err := i.src.SessionTranscript(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("snapshot session %q: load transcript: %w", sessionID, err)
	}

	events, err := normalizeEvents(sessionID, transcript.Messages)
	if err != nil {
		return fmt.Errorf("snapshot session %q: normalize: %w", sessionID, err)
	}

	fingerprint := trajectoryFingerprint(transcript.TurnCount, transcript.Messages)
	now := time.Now().UTC()

	// Rebuildable half: wholesale replace the event stream, then publish the index
	// row (fidelity=partial: this is the lossy messages/traces projection).
	if err := i.store.rebuildTrajectoryEvents(ctx, TrajectorySnapshot{
		SessionID:    transcript.SessionID,
		Status:       transcript.Status,
		Agent:        transcript.Agent,
		UserID:       transcript.UserID,
		StartedAt:    &transcript.CreatedAt,
		EndedAt:      transcript.EndedAt,
		TurnCount:    transcript.TurnCount,
		MessageCount: len(transcript.Messages),
		Fingerprint:  fingerprint,
		Fidelity:     "partial",
		NormalizedAt: &now,
	}, events); err != nil {
		return fmt.Errorf("snapshot session %q: %w", sessionID, err)
	}
	return nil
}

// ListPiSessionsForCapture passes the enumeration through to the control plane.
// It is on the Ingestor rather than called directly by the capture path so that
// every control-plane read the memory half performs goes through one type.
func (i *Ingestor) ListPiSessionsForCapture(ctx context.Context, since time.Time) ([]PiSessionForCapture, error) {
	if i == nil {
		return nil, nil
	}
	return i.src.ListPiSessionsForCapture(ctx, since)
}

// trajTraceNode is a local mirror of internal/server.traceNode. We redeclare it
// here rather than import the server package: server imports store, so importing
// server would drag the control plane into the memory half. Only the fields the
// normalizer needs are kept; the JSON tags must stay in lockstep with
// trace_collector.go.
type trajTraceNode struct {
	Kind          string          `json:"kind"` // "thinking" | "tool"
	ToolName      string          `json:"toolName,omitempty"`
	Label         string          `json:"label,omitempty"`
	Args          json.RawMessage `json:"args,omitempty"`
	ResultSummary string          `json:"resultSummary,omitempty"`
	IsError       bool            `json:"isError,omitempty"`
	StartedAt     string          `json:"startedAt,omitempty"`
	DurationMs    int64           `json:"durationMs,omitempty"`
}

// normalizeEvents flattens messages (+ their trace nodes) into the ordered
// 7-primitive event stream. idx is a running 0-based counter across the session.
//
// This lives on the MEMORY side, not the control side, because its output type
// (TrajectoryEvent), its taxonomy, and every consumer of the result are all
// memory concepts; its only control-side input is the flat transcript the
// boundary already hands over. Putting it here means the control store has no
// code that knows what a trajectory event is, and the projection can be tested
// against a fake source with no control database in play.
func normalizeEvents(sessionID string, messages []TranscriptMessage) ([]TrajectoryEvent, error) {
	events := []TrajectoryEvent{}
	idx := 0
	appendEvent := func(ev TrajectoryEvent) {
		ev.SessionID = sessionID
		ev.Idx = idx
		idx++
		events = append(events, ev)
	}

	for _, m := range messages {
		messageID := m.ID
		ts := m.CreatedAt

		if m.Role != "assistant" {
			// user (and any non-assistant role, e.g. shell output) -> one event.
			kind := "user"
			if m.Role != "user" {
				kind = "system"
			}
			appendEvent(TrajectoryEvent{
				Kind:      kind,
				Subtype:   subtypeForRole(m.Role),
				MessageID: &messageID,
				Text:      m.Content,
				TS:        tsPtr(ts),
			})
			continue
		}

		// Assistant: emit trace nodes first, then the assistant text.
		if len(m.Trace) > 0 {
			var nodes []trajTraceNode
			if err := json.Unmarshal(m.Trace, &nodes); err != nil {
				return nil, fmt.Errorf("message %d: parse trace: %w", m.ID, err)
			}
			for nodeIdx, node := range nodes {
				switch node.Kind {
				case "thinking":
					text := node.Label
					if text == "" {
						text = "Thinking"
					}
					appendEvent(TrajectoryEvent{
						Kind:      "thinking",
						MessageID: &messageID,
						Text:      text,
						TS:        tsPtr(parseTraceTime(node.StartedAt, ts)),
					})
				case "tool":
					callID := fmt.Sprintf("%d-%d", m.ID, nodeIdx)
					nodeTS := parseTraceTime(node.StartedAt, ts)
					appendEvent(TrajectoryEvent{
						Kind:      "tool_call",
						Name:      node.ToolName,
						CallID:    callID,
						MessageID: &messageID,
						Text:      string(node.Args),
						TS:        tsPtr(nodeTS),
					})
					appendEvent(TrajectoryEvent{
						Kind:      "tool_result",
						Name:      node.ToolName,
						CallID:    callID,
						IsError:   node.IsError,
						MessageID: &messageID,
						Text:      node.ResultSummary,
						TS:        tsPtr(nodeTS),
					})
				}
			}
		}

		appendEvent(TrajectoryEvent{
			Kind:      "assistant",
			MessageID: &messageID,
			Text:      m.Content,
			TS:        tsPtr(ts),
		})
	}
	return events, nil
}

func subtypeForRole(role string) string {
	if role == "user" || role == "assistant" {
		return ""
	}
	return role
}

// parseTraceTime parses a trace node's RFC3339Nano timestamp, falling back to
// the owning message's created_at when the node has no (or an unparseable) time.
func parseTraceTime(value string, fallback time.Time) time.Time {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	return fallback
}

func tsPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// trajectoryFingerprint is a cheap change token: turn count plus the id and
// created_at of the last message. It moves whenever a session grows, which is
// all the incremental pipeline needs to decide whether to re-process.
func trajectoryFingerprint(turnCount int, messages []TranscriptMessage) string {
	var lastID int64
	var lastAt string
	if n := len(messages); n > 0 {
		lastID = messages[n-1].ID
		lastAt = formatTime(messages[n-1].CreatedAt)
	}
	return fmt.Sprintf("t=%d;m=%d;last=%d@%s", turnCount, len(messages), lastID, lastAt)
}
