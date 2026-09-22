package agentrunner

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
)

// This is a bounded comparison, not a second supported transport. Full native
// JSON remains the protocol until a measured deployment workload needs encoding.
func TestNativeSnapshotEncodingComparison(t *testing.T) {
	var corpus bytes.Buffer
	seed := uint32(23)
	for n := 0; corpus.Len() < 512<<10; n++ {
		seed = 1664525*seed + 1013904223
		fmt.Fprintf(&corpus, "cycle=%06d sample=%03d capacity=%05d resistance=%05d\n", n, n%71, seed%10000, seed/10000%10000)
	}
	var rawBytes, compressedBytes int64
	var encodeTime, decodeTime time.Duration
	frames := 0
	for rawBytes < 80<<20 {
		length := min(corpus.Len(), (frames+1)*8192)
		payload, _ := json.Marshal(map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": corpus.String()[:length]}}}})
		if len(payload) >= agentwire.MaxFrame-1024 {
			t.Fatal("illegal spike frame")
		}
		var compressed bytes.Buffer
		start := time.Now()
		writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		encodeTime += time.Since(start)
		start = time.Now()
		reader, err := gzip.NewReader(bytes.NewReader(compressed.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(io.LimitReader(reader, agentwire.MaxFrame))
		if err != nil {
			t.Fatal(err)
		}
		reader.Close()
		decodeTime += time.Since(start)
		if len(decoded) >= agentwire.MaxFrame || !bytes.Equal(decoded, payload) {
			t.Fatal("encoding changed source bytes")
		}
		rawBytes += int64(len(payload))
		compressedBytes += int64(compressed.Len())
		frames++
	}
	t.Logf(`{"frames":%d,"raw_bytes":%d,"gzip_best_speed_bytes":%d,"encode_ms":%d,"decode_ms":%d,"oracle":"exact decoded byte equality","decision":"defer pending representative transport bottleneck"}`, frames, rawBytes, compressedBytes, encodeTime.Milliseconds(), decodeTime.Milliseconds())
}
