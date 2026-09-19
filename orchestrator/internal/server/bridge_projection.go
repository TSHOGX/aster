package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/store"
)

// parserCheckpoint contains only deterministic normalizer state. Runtime
// handles, callbacks and published events never become recovery authority.
type parserCheckpoint struct {
	Version      int                  `json:"version"`
	DriverID     string               `json:"driver_id"`
	Pending      map[string]string    `json:"pending"`
	PendingOrder []string             `json:"pending_order"`
	Last         string               `json:"last"`
	LastPhase    string               `json:"last_phase"`
	Finished     []traceNode          `json:"finished"`
	Tools        map[string]traceNode `json:"tools"`
	LastEventAt  time.Time            `json:"last_event_at"`
}

type projectionFrame struct {
	messages []store.ProjectedMessage
	events   []events.Event
	phases   []store.SetTurnPhaseParams
}

func (p *streamParser) checkpoint() (json.RawMessage, error) {
	state := parserCheckpoint{Version: 1, DriverID: p.driverID, Pending: map[string]string{}, PendingOrder: p.pendingOrder, Last: p.last, LastPhase: p.lastPhase, Finished: p.trace.finished, Tools: p.trace.pending, LastEventAt: p.trace.lastEventAt}
	for id, value := range p.pending {
		state.Pending[id] = value.String()
	}
	return json.Marshal(state)
}

func (p *streamParser) restoreCheckpoint(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var state parserCheckpoint
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Version != 1 || state.DriverID != p.driverID {
		return fmt.Errorf("unsupported bridge parser checkpoint version or driver")
	}
	for id, value := range state.Pending {
		var b strings.Builder
		b.WriteString(value)
		p.pending[id] = &b
	}
	p.pendingOrder = state.PendingOrder
	p.last = state.Last
	p.lastPhase = state.LastPhase
	p.trace.finished = state.Finished
	p.trace.lastEventAt = state.LastEventAt
	if state.Tools != nil {
		p.trace.pending = state.Tools
	}
	return nil
}

// projectBridgeRecords can run after a commit or after a process restart. All
// durable effects share the source checkpoint transaction. A failed frame is
// reconstructed from the previous checkpoint on retry, including partial text
// and tool traces. The caller bounds the work; later polls continue the backlog.
func (s *Server) projectBridgeRecords(ctx context.Context, limit int) error {
	s.bridgeProjectionMu.Lock()
	defer s.bridgeProjectionMu.Unlock()
	records, err := s.store.PendingBridgeRecords(ctx, limit)
	if err != nil {
		return err
	}
	var firstError error
	failed := map[string]bool{}
	for _, record := range records {
		key := fmt.Sprintf("%s:%d:%d", record.GenerationID, record.TurnID, record.Attempt)
		if failed[key] {
			continue
		}
		if err := s.projectBridgeRecord(ctx, record); err != nil {
			failed[key] = true
			if firstError == nil {
				firstError = fmt.Errorf("project bridge record %d: %w", record.ID, err)
			}
		}
	}
	return firstError
}

func (s *Server) projectBridgeRecord(ctx context.Context, record store.BridgeRecord) error {
	checkpoint, err := s.store.LoadBridgeProjection(ctx, record)
	if err != nil {
		return err
	}
	if record.ID <= checkpoint.LastRecordID {
		return nil
	}
	session, err := s.store.GetSession(ctx, record.SessionID)
	if err != nil {
		return err
	}
	parser := newStreamParser(s, record.SessionID, session.Agent)
	parser.turnID = record.TurnID
	if err := parser.restoreCheckpoint(checkpoint.State); err != nil {
		return err
	}
	parser.trace.now = func() time.Time { return record.CreatedAt }
	frame := &projectionFrame{}
	parser.projection = frame
	switch record.Type {
	case bridge.TypeEmitOutput:
		parser.handleBridgeOutput(normalizerBridgeOutput{Stream: record.Stream, Payload: record.Payload})
	case bridge.TypeAckTurnCompleted:
		parser.flush("")
	default:
		return fmt.Errorf("unsupported bridge source type %q", record.Type)
	}
	if parser.projectionErr != nil {
		return parser.projectionErr
	}
	state, err := parser.checkpoint()
	if err != nil {
		return err
	}
	messages, err := s.store.CommitBridgeProjection(ctx, record, checkpoint.LastRecordID, state, frame.messages)
	if err != nil {
		return err
	}
	for _, event := range frame.events {
		s.hub.Publish(event)
	}
	for _, message := range messages {
		s.publishDurableEvent(ctx, message.EventID)
	}

	for _, phase := range frame.phases {
		if snapshot, err := s.store.SetTurnPhase(ctx, phase); err == nil {
			s.publishSessionRuntime(record.SessionID, snapshot)
		}
	}
	return nil
}

func (s *Server) projectBeforeBridgeCompletion(ctx context.Context, p store.CompleteTurnParams) error {
	if err := s.store.PrepareBridgeCompletion(ctx, p); err != nil {
		return err
	}
	// Do not permit successful completion (or driver-state advancement) while
	// accepted producer input still lacks a committed message/trace projection.
	s.bridgeProjectionMu.Lock()
	defer s.bridgeProjectionMu.Unlock()
	for {
		records, err := s.store.PendingBridgeTurnRecords(ctx, p.SessionID, p.GenerationID, p.TurnID, 128)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		for _, record := range records {
			if err := s.projectBridgeRecord(ctx, record); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
