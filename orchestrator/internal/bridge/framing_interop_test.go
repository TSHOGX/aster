package bridge

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStreamFramingInteroperatesWithGuestCodec(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 required for guest protocol contract")
	}
	module, err := filepath.Abs("../../../sandbox-image/files/usr/local/lib/harness_stream.py")
	if err != nil {
		t.Fatal(err)
	}
	frame := journalFrame(1)
	frame.Envelope.Payload = json.RawMessage(`{"line":"unicode 中文 and \u001e newline\n","counter":9007199254740993}`)
	raw, err := EncodeStreamFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	program := `import importlib.util,sys
spec=importlib.util.spec_from_file_location('harness_stream',sys.argv[1])
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
frames=module.Decoder().feed(sys.stdin.buffer.read())
assert len(frames)==1
assert frames[0]['envelope']['payload']['counter']==9007199254740993
sys.stdout.buffer.write(module.encode(frames[0]))
`
	command := exec.Command(python, "-c", program, module)
	command.Stdin = bytes.NewReader(raw)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	frames, err := (&StreamDecoder{}).Feed(output)
	if err != nil || len(frames) != 1 {
		t.Fatalf("guest frame rejected: %+v %v", frames, err)
	}
	var payload struct {
		Line    string      `json:"line"`
		Counter json.Number `json:"counter"`
	}
	if err := json.Unmarshal(frames[0].Envelope.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Counter.String() != "9007199254740993" || payload.Line != "unicode 中文 and \x1e newline\n" {
		t.Fatalf("cross-language payload changed: %+v", payload)
	}
}
