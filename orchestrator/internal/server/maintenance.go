package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/events"
)

func (s *Server) touchMaintenanceHeartbeat(now time.Time) {
	root := strings.TrimSpace(s.cfg.Harness.RunDir)
	if root == "" {
		return
	}
	path := filepath.Join(root, "maintenance-heartbeat")
	if err := os.MkdirAll(root, 0o755); err != nil {
		s.log.Warn("maintenance heartbeat directory failed", "path", root, "error", err)
		return
	}
	if err := os.WriteFile(path, []byte(now.UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
		s.log.Warn("maintenance heartbeat write failed", "path", path, "error", err)
	}
}

func (s *Server) handleBridgeCommittedEnvelope(ctx context.Context, envelope bridge.Envelope, eventID int64) {
	s.publishDurableEvent(ctx, eventID)
	switch envelope.Type {
	case "turn_control_result":
		s.publishSessionRuntimeFromStore(ctx, envelope.SessionID)
	case bridge.TypeEmitOutput:
		if err := s.projectBridgeRecords(ctx, 128); err != nil {
			s.log.Warn("bridge projection pending", "error", err)
		}
	case bridge.TypeAckTurnStarted:
		// The agent has the turn: it leaves the queue and enters the transcript here.
		// AckTurnStarted committed the message row in the same transaction that
		// marked the turn running, so publish that message and the new projection
		// together — this is the one moment a queued prompt becomes a conversation
		// entry, and its timestamp is when the model actually received it.
		s.publishTurnUserMessage(ctx, envelope)
		s.publishSessionRuntimeFromStore(ctx, envelope.SessionID)
	case bridge.TypeAckTurnCompleted:
		s.handleBridgeCompletion(ctx, envelope)
		// See turn_resume.go: continue a turn whose model call died on a
		// transient upstream fault, rather than leaving the reply truncated.
		s.maybeResumeFailedTurn(ctx, envelope)
	}
}

func (s *Server) publishTurnUserMessage(ctx context.Context, envelope bridge.Envelope) {
	if envelope.TurnID == nil {
		return
	}
	message, ok, err := s.store.TurnUserMessage(ctx, envelope.SessionID, *envelope.TurnID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("failed to load dispatched turn message",
				"session_id", envelope.SessionID, "turn_id", *envelope.TurnID, "error", err)
		}
		return
	}
	if !ok {
		return
	}
	s.hub.Publish(events.Event{Type: "message.created", SessionID: envelope.SessionID, Payload: message})
}

func (s *Server) publishDurableEvent(ctx context.Context, eventID int64) {
	if eventID == 0 {
		return
	}
	record, ok, err := s.store.GetEvent(ctx, eventID)
	if err != nil {
		s.log.Warn("failed to load durable event", "event_id", eventID, "error", err)
		return
	}
	if !ok {
		return
	}
	s.hub.Publish(eventFromRecord(record))
}

func (s *Server) handleBridgeOutput(ctx context.Context, envelope bridge.Envelope) {
	var payload struct {
		Stream  string          `json:"stream"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		s.log.Warn("failed to decode bridge output payload", "session_id", envelope.SessionID, "generation_id", envelope.GenerationID, "error", err)
		return
	}
	stream := payload.Stream
	if stream == "" {
		stream = "stdout"
	}
	if len(payload.Payload) == 0 {
		return
	}
	parser := s.bridgeStreamParser(ctx, envelope)
	parser.handleBridgeOutput(normalizerBridgeOutput{Stream: stream, Payload: payload.Payload})
}

func (s *Server) handleBridgeCompletion(ctx context.Context, envelope bridge.Envelope) {
	s.completeBridgeStreamParser(envelope)
	// The turn left the active slot and whatever was queued behind it is now next.
	// Clients learn the queue advanced from this push; without it they would hold
	// a projection showing the finished turn as active until their next reload.
	s.publishSessionRuntimeFromStore(ctx, envelope.SessionID)
	if s.workspaceInventory != nil {
		if err := s.scanSessionArtifacts(ctx, envelope.SessionID); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("failed to scan bridge-completed session artifacts", "session_id", envelope.SessionID, "error", err)
		}
	}
}

func (s *Server) bridgeStreamParser(ctx context.Context, envelope bridge.Envelope) *streamParser {
	key, ok := bridgeParserKey(envelope)
	if !ok {
		return newStreamParser(s, envelope.SessionID, s.sessionAgent(ctx, envelope.SessionID))
	}
	s.bridgeParserMu.Lock()
	defer s.bridgeParserMu.Unlock()
	if s.bridgeParsers == nil {
		s.bridgeParsers = make(map[bridgeStreamParserKey]*streamParser)
	}
	parser := s.bridgeParsers[key]
	if parser == nil {
		parser = newStreamParser(s, envelope.SessionID, s.sessionAgent(ctx, envelope.SessionID))
		parser.turnID = key.TurnID
		s.bridgeParsers[key] = parser
	}
	return parser
}

func (s *Server) sessionAgent(ctx context.Context, sessionID string) string {
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		s.log.Warn("failed to load session for bridge output", "session_id", sessionID, "error", err)
		return ""
	}
	return session.Agent
}

func (s *Server) completeBridgeStreamParser(envelope bridge.Envelope) {
	key, ok := bridgeParserKey(envelope)
	if !ok {
		return
	}
	s.bridgeParserMu.Lock()
	parser := s.bridgeParsers[key]
	delete(s.bridgeParsers, key)
	s.bridgeParserMu.Unlock()
	if parser == nil {
		return
	}
	// Teardown flush on bridge disconnect: no turn-end event was seen, so the
	// terminator is unknown.
	parser.flush("")
	parser.complete()
}

func bridgeParserKey(envelope bridge.Envelope) (bridgeStreamParserKey, bool) {
	if envelope.TurnID == nil {
		return bridgeStreamParserKey{}, false
	}
	return bridgeStreamParserKey{
		SessionID:    envelope.SessionID,
		GenerationID: envelope.GenerationID,
		TurnID:       *envelope.TurnID,
	}, true
}

func (s *Server) sweepExpiredShareLinks(ctx context.Context, now time.Time) {
	orphaned, err := s.store.SweepExpiredShareLinks(ctx, now)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("expired share-link sweep failed", "error", err)
		}
		return
	}
	if len(orphaned) == 0 {
		return
	}
	if s.shareBlobs == nil {
		// Rows referenced blobs but no store is configured (file sharing was turned
		// off after links were made). Say so rather than dropping it silently: the
		// bytes are on disk and nothing else will report them.
		s.log.Warn("share link sweep reported unreferenced blobs with no blob store configured",
			"blob_count", len(orphaned))
		return
	}
	s.reclaimShareBlobs(orphaned)
}
