package bridge

import "encoding/json"

const (
	TypeHello            = "hello"
	TypeHelloAck         = "hello_ack"
	TypeHeartbeat        = "heartbeat"
	TypeProbeNetwork     = "probe_network"
	TypeClaimNextTurn    = "claim_next_turn"
	TypeResumeTurn       = "resume_turn"
	TypeGrant            = "grant"
	TypeNoWork           = "no_work"
	TypeAckTurnStarted   = "ack_turn_started"
	TypeEmitOutput       = "emit_output"
	TypeAckTurnCompleted = "ack_turn_completed"
	TypeError            = "error"
)

type Envelope struct {
	MessageID    string          `json:"message_id"`
	RequestID    string          `json:"request_id,omitempty"`
	Type         string          `json:"type"`
	SessionID    string          `json:"session_id"`
	GenerationID string          `json:"generation_id"`
	TurnID       *int64          `json:"turn_id,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}
