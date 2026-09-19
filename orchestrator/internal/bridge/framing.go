package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const StreamProtocolVersion = 1
const MaxStreamFrameBytes = 8 << 20
const recordSeparator byte = 0x1e

// StreamFrame separates durable application record positions from the engine's
// bounded process-output cursor. Every attachment begins with sync/state,
// including cursor-zero attachment when the engine reports no replay gap.
type StreamFrame struct {
	Version   int             `json:"version"`
	Kind      string          `json:"kind"`
	Execution string          `json:"execution"`
	Sequence  uint64          `json:"sequence,omitempty"`
	After     uint64          `json:"after,omitempty"`
	Head      uint64          `json:"head,omitempty"`
	CommandID string          `json:"command_id,omitempty"`
	Envelope  *Envelope       `json:"envelope,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func (frame StreamFrame) validate() error {
	if frame.Version != StreamProtocolVersion || frame.Execution == "" {
		return fmt.Errorf("unsupported stream protocol or missing execution")
	}
	switch frame.Kind {
	case "record":
		if frame.Sequence == 0 || frame.Envelope == nil {
			return fmt.Errorf("journal record requires sequence and envelope")
		}
	case "command":
		if frame.CommandID == "" || frame.Envelope == nil {
			return fmt.Errorf("command requires durable identity and envelope")
		}
	case "command_ack":
		if frame.CommandID == "" {
			return fmt.Errorf("command acknowledgment requires identity")
		}
	case "ack", "sync", "state", "error":
	default:
		return fmt.Errorf("unsupported bridge stream frame %q", frame.Kind)
	}
	return nil
}

func EncodeStreamFrame(frame StreamFrame) ([]byte, error) {
	if err := frame.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxStreamFrameBytes {
		return nil, fmt.Errorf("bridge stream frame exceeds %d bytes", MaxStreamFrameBytes)
	}
	result := make([]byte, 0, len(raw)+2)
	result = append(result, recordSeparator)
	result = append(result, raw...)
	result = append(result, '\n')
	return result, nil
}

// StreamDecoder accepts arbitrary process frame boundaries. JSON encoding
// escapes literal record separators and newlines, so a new separator safely
// abandons an incomplete send rather than concatenating it with its retry.
// Oversize input fails explicitly and never grows an unbounded buffer.
type StreamDecoder struct {
	buffer []byte
	active bool
}

func (d *StreamDecoder) Feed(chunk []byte) ([]StreamFrame, error) {
	var frames []StreamFrame
	for len(chunk) > 0 {
		if !d.active {
			start := bytes.IndexByte(chunk, recordSeparator)
			if start < 0 {
				return frames, nil
			}
			d.active = true
			d.buffer = d.buffer[:0]
			chunk = chunk[start+1:]
		}
		boundary := bytes.IndexAny(chunk, "\x1e\n")
		end := len(chunk)
		if boundary >= 0 {
			end = boundary
		}
		if len(d.buffer)+end > MaxStreamFrameBytes {
			d.active = false
			d.buffer = nil
			return frames, fmt.Errorf("bridge stream frame exceeds %d bytes", MaxStreamFrameBytes)
		}
		d.buffer = append(d.buffer, chunk[:end]...)
		chunk = chunk[end:]
		if boundary < 0 {
			return frames, nil
		}
		marker := chunk[0]
		chunk = chunk[1:]
		if marker == recordSeparator {
			d.buffer = d.buffer[:0]
			continue
		}
		var frame StreamFrame
		err := json.Unmarshal(d.buffer, &frame)
		d.active = false
		d.buffer = d.buffer[:0]
		if err != nil {
			return frames, fmt.Errorf("decode bridge stream frame: %w", err)
		}
		if err := frame.validate(); err != nil {
			return frames, err
		}
		frames = append(frames, frame)
	}
	return frames, nil
}
func (d *StreamDecoder) Reset() { d.buffer = nil; d.active = false }
