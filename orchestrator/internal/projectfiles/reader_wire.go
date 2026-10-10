package projectfiles

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const ReaderProtocol = 3
const ReaderRequestBytes = 16 << 10
const ReaderFrameBytes = 256 << 10
const ReaderChunkBytes = 32 << 10

type ReadRequest struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Path      string `json:"path,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Name      string `json:"name,omitempty"`
	Seek      string `json:"seek,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Credits   int    `json:"credits,omitempty"`
}
type ReadMetadata struct {
	Size         int64  `json:"size"`
	ModifiedNano int64  `json:"modified_nano,string"`
	Kind         string `json:"kind"`
}
type ReadFrame struct {
	Version  int           `json:"version"`
	ID       string        `json:"id,omitempty"`
	Kind     string        `json:"kind"`
	Code     string        `json:"code,omitempty"`
	Metadata *ReadMetadata `json:"metadata,omitempty"`
	Page     *Page         `json:"page,omitempty"`
	Offset   int64         `json:"offset,string,omitempty"`
	Data     []byte        `json:"data,omitempty"`
	Size     int64         `json:"size,omitempty"`
	Digest   string        `json:"digest,omitempty"`
}

// ReadWire rejects oversized frames before allocating their body. No decoder
// can silently accept concatenated JSON, unknown protocol fields or short data.
func ReadWire(r io.Reader, limit int, out any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	size := int(binary.BigEndian.Uint32(prefix[:]))
	if size < 2 || size > limit {
		return errors.New("workspace reader frame exceeds bound")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing reader protocol data")
	}
	return nil
}
func WriteWire(w io.Writer, limit int, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return errors.New("workspace reader frame exceeds bound")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	for _, part := range [][]byte{prefix[:], raw} {
		for len(part) > 0 {
			n, e := w.Write(part)
			if e != nil {
				return e
			}
			if n <= 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}
func validReadID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (r ReadRequest) Validate() error {
	if r.Version != ReaderProtocol || !validReadID(r.ID) {
		return ErrPath
	}
	switch r.Operation {
	case "credit":
		if r.Path != "" || r.Cursor != "" || r.Name != "" || r.Seek != "" || r.Limit != 0 || r.Credits < 1 || r.Credits > 8 {
			return ErrPath
		}
		return nil
	case "cancel":
		if r.Path != "" || r.Cursor != "" || r.Name != "" || r.Seek != "" || r.Limit != 0 || r.Credits != 0 {
			return ErrPath
		}
		return nil
	case "list":
		if len(r.Cursor) > 2048 || r.Credits != 0 {
			return ErrPath
		}
		return (DirectoryQuery{Path: r.Path, Cursor: r.Cursor, Limit: r.Limit, Name: r.Name, Seek: r.Seek}).Validate()
	case "read", "stat":
		if r.Cursor != "" || r.Name != "" || r.Seek != "" || r.Limit != 0 || r.Credits != 0 {
			return ErrPath
		}
		return Path(r.Path, false)
	default:
		return ErrPath
	}
}
