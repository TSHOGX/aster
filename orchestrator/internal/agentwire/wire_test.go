package agentwire

import (
	"bytes"
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
	for _, raw := range []string{`{"version":1,"kind":"sync","session":"s","incarnation":"i","owner":1}`, "{}\n", "bad\n", strings.Repeat("x", MaxFrame) + "\n"} {
		if _, err := NewDecoder(strings.NewReader(raw)).Next(); !errors.Is(err, ErrFrame) {
			t.Fatalf("bad frame accepted: %v", err)
		}
	}
}
