// Package agentwire defines the application/runner protocol. Engine transport
// cursors are deliberately absent: Sequence belongs only to the durable journal.
package agentwire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const Version = 1
const MaxFrame = 4 << 20

var ErrFrame = errors.New("invalid or oversized Agent protocol frame")

type Frame struct {
	Version     int             `json:"version"`
	Kind        string          `json:"kind"`
	Session     string          `json:"session"`
	Incarnation string          `json:"incarnation"`
	Owner       uint64          `json:"owner"`
	CommandID   string          `json:"command_id,omitempty"`
	TurnID      string          `json:"turn_id,omitempty"`
	Sequence    uint64          `json:"sequence,omitempty"`
	After       uint64          `json:"after,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	State       string          `json:"state,omitempty"`
	Error       string          `json:"error,omitempty"`
}
type Decoder struct{ scanner *bufio.Scanner }

func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), MaxFrame)
	s.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, ErrFrame
		}
		return 0, nil, nil
	})
	return &Decoder{s}
}
func (d *Decoder) Next() (Frame, error) {
	var f Frame
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return f, errors.Join(ErrFrame, err)
		}
		return f, io.EOF
	}
	raw := d.scanner.Bytes()
	if len(raw) == 0 || len(raw) >= MaxFrame {
		return f, ErrFrame
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, errors.Join(ErrFrame, err)
	}
	if f.Version != Version || f.Kind == "" || f.Session == "" || f.Incarnation == "" || f.Owner == 0 {
		return f, ErrFrame
	}
	return f, nil
}
func Encode(w io.Writer, f Frame) error {
	f.Version = Version
	raw, err := MarshalJSON(f)
	if err != nil {
		return err
	}
	if len(raw)+1 >= MaxFrame {
		return ErrFrame
	}
	raw = append(raw, '\n')

	for len(raw) > 0 {
		n, err := w.Write(raw)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}

// ExitCleanFailure means the worker failed but its independent guardian verified
// descendant settlement. ExitUnsettled never permits automatic replacement.
const ExitCleanFailure = 10
const ExitUnsettled = 11

// Recoverable reports whether a malformed line was consumed without exhausting
// the bounded scanner. Hosts may discard such a prefix only before a new-owner
// synchronization frame; command decoders remain strict throughout.
func (d *Decoder) Recoverable() bool { return d.scanner.Err() == nil }

// MarshalJSON keeps native JSON out of HTML escaping; these bytes travel only
// through JSON protocols, never as inline page markup.
func MarshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
