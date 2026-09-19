package bridge

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func journalFrame(sequence uint64) StreamFrame {
	return StreamFrame{Version: 1, Kind: "record", Execution: "execution", Sequence: sequence, Envelope: &Envelope{MessageID: "message", SessionID: "session", GenerationID: "execution", Type: TypeEmitOutput, Payload: json.RawMessage(`{"output_sequence":1,"payload":{"line":"text\\nwith separator \\u001e"}}`)}}
}

func TestStreamFramingHandlesEverySplitAndCoalescedRecords(t *testing.T) {
	first, err := EncodeStreamFrame(journalFrame(1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeStreamFrame(journalFrame(2))
	if err != nil {
		t.Fatal(err)
	}
	joined := append(first, second...)
	for split := 0; split <= len(joined); split++ {
		decoder := &StreamDecoder{}
		a, err := decoder.Feed(joined[:split])
		if err != nil {
			t.Fatal(err)
		}
		b, err := decoder.Feed(joined[split:])
		if err != nil {
			t.Fatal(err)
		}
		frames := append(a, b...)
		if len(frames) != 2 || frames[0].Sequence != 1 || frames[1].Sequence != 2 {
			t.Fatalf("split %d changed frames: %+v", split, frames)
		}
	}
}
func TestStreamFramingResynchronizesPartialSendWithoutDispatchingIt(t *testing.T) {
	complete, err := EncodeStreamFrame(journalFrame(1))
	if err != nil {
		t.Fatal(err)
	}
	for split := 1; split < len(complete); split++ {
		decoder := &StreamDecoder{}
		partial, err := decoder.Feed(complete[:split])
		if err != nil || len(partial) != 0 {
			t.Fatalf("partial frame dispatched: %+v %v", partial, err)
		}
		frames, err := decoder.Feed(complete)
		if err != nil || len(frames) != 1 {
			t.Fatalf("retry split %d: %+v %v", split, frames, err)
		}
	}
}
func TestStreamFramingRetainsPayloadBeyondEngineReplayTail(t *testing.T) {
	frame := journalFrame(1)
	payload, _ := json.Marshal(map[string]string{"line": strings.Repeat("x", 256<<10)})
	frame.Envelope.Payload = payload
	encoded, err := EncodeStreamFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoder := &StreamDecoder{}
	var got []StreamFrame
	for len(encoded) > 0 {
		n := 997
		if n > len(encoded) {
			n = len(encoded)
		}
		frames, err := decoder.Feed(encoded[:n])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, frames...)
		encoded = encoded[n:]
	}
	if len(got) != 1 || !bytes.Equal(got[0].Envelope.Payload, payload) {
		t.Fatal("large durable record was truncated")
	}
}
func TestStreamFramingFailsOnOversizeOrUnsupportedCompleteFrames(t *testing.T) {
	decoder := &StreamDecoder{}
	if _, err := decoder.Feed(append([]byte{recordSeparator}, bytes.Repeat([]byte{'x'}, MaxStreamFrameBytes+1)...)); err == nil {
		t.Fatal("unbounded frame accepted")
	}
	for _, input := range []string{"\x1e{bad}\n", "\x1e{\"version\":2,\"kind\":\"sync\",\"execution\":\"execution\"}\n", "\x1e{\"version\":1,\"kind\":\"record\",\"execution\":\"execution\"}\n"} {
		decoder.Reset()
		if _, err := decoder.Feed([]byte(input)); err == nil {
			t.Fatalf("invalid complete frame accepted: %q", input)
		}
	}
}

func TestBridgePayloadDigestInputPreservesLargeIntegers(t *testing.T) {
	first, err := json.Marshal(jsonPayload(json.RawMessage(`{"value":9007199254740992}`)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(jsonPayload(json.RawMessage(`{"value":9007199254740993}`)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) || !bytes.Contains(second, []byte("9007199254740993")) {
		t.Fatalf("distinct journal payloads collapsed through float64: %s %s", first, second)
	}
}
