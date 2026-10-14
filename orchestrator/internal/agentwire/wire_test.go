package agentwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type byteReader struct{ io.Reader }

func (r byteReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:1]) }
func TestPartialCoalescedAndTruncatedFrames(t *testing.T) {
	var buf bytes.Buffer
	f := Frame{Kind: "sync", Session: "s", Incarnation: "i", Owner: 1}
	for i := 0; i < 3; i++ {
		if err := Encode(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for _, reader := range []io.Reader{bytes.NewReader(buf.Bytes()), byteReader{bytes.NewReader(buf.Bytes())}} {
		d := NewDecoder(reader)
		for i := 0; i < 3; i++ {
			got, err := d.Next()
			if err != nil || got.Kind != "sync" {
				t.Fatal(got, err)
			}
		}
		if _, err := d.Next(); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"version":1,"kind":"sync","session":"s","incarnation":"i","owner":1}`, `{"version":2,"kind":"sync","session":"s","incarnation":"i","owner":1}` + "\n", "{}\n", "bad\n", strings.Repeat("x", MaxFrame) + "\n"} {
		if _, err := NewDecoder(strings.NewReader(raw)).Next(); !errors.Is(err, ErrFrame) {
			t.Fatalf("bad frame accepted: %v", err)
		}
	}
}

func TestHTMLPayloadDoesNotExpandPastWireBudget(t *testing.T) {
	payload := json.RawMessage(`{"text":"` + strings.Repeat("<", 1<<20) + `"}`)
	frame := Frame{Kind: "record", Session: "session", Incarnation: "incarnation", Owner: 1, Sequence: 1, Payload: payload}
	var buffer bytes.Buffer
	if err := Encode(&buffer, frame); err != nil {
		t.Fatal("HTML escaping exhausted the frame budget", err)
	}
	if buffer.Len() > 2<<20 {
		t.Fatal("raw native payload expanded", buffer.Len())
	}
	decoded, err := NewDecoder(&buffer).Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Fatal("native payload changed")
	}
}
