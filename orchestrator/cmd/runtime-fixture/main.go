// runtime-fixture is a deterministic, local-only integration test executable.
// It is never installed into the product template.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		panic("fixture mode required")
	}
	switch os.Args[1] {
	case "model":
		model()
	case "native":
		native()
	default:
		panic("unknown fixture")
	}
}
func model() {
	var mu sync.Mutex
	count := 0
	both := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			fmt.Fprintln(w, "ready")
			return
		}
		var request map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		mu.Lock()
		count++
		if count == 2 {
			close(both)
			fmt.Println("overlapping-model-requests")
		}
		mu.Unlock()
		select {
		case <-both:
		case <-r.Context().Done():
			return
		case <-time.After(20 * time.Second):
			http.Error(w, "overlap timeout", 408)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		records := []string{
			`{"type":"message_start","message":{"id":"fixture","type":"message","role":"assistant","model":"local-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"deterministic-local-reply"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		}
		for _, raw := range records {
			var value map[string]any
			_ = json.Unmarshal([]byte(raw), &value)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", value["type"], raw)
			w.(http.Flusher).Flush()
		}
	})
	fmt.Println("model-ready")
	if err := http.ListenAndServe("127.0.0.1:18889", mux); err != nil {
		panic(err)
	}
}
func native() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var command struct{ ID, Type, Message string }
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			panic(err)
		}
		response := map[string]any{"id": command.ID, "type": "response", "command": command.Type, "success": true}
		raw, _ := json.Marshal(response)
		fmt.Println(string(raw))
		if command.Type == "prompt" {
			root := os.Getenv("PI_CODING_AGENT_DIR")
			history := filepath.Join(root, "native", "session.jsonl")
			if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
				panic(err)
			}
			if err := os.WriteFile(history, []byte(`{"type":"session","id":"fixture"}`+"\n"), 0600); err != nil {
				panic(err)
			}
			child := exec.Command("/bin/sh", "-c", `setsid sh -c 'echo $$ > "$PI_CODING_AGENT_DIR/escaped-pid"; exec sleep 300' & sleep 300 & echo $! > "$PI_CODING_AGENT_DIR/group-pid"; wait`)
			child.Stdout = os.Stderr
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				panic(err)
			}
			for i := 0; i < 100; i++ {
				if _, err := os.Stat(filepath.Join(root, "escaped-pid")); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			fmt.Println(`{"type":"agent_end","messages":[]}`)
		}
	}
}
