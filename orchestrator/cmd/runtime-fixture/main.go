// runtime-fixture is a deterministic, local-only integration test executable.
// It is never installed into the product template.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		panic("fixture mode required")
	}
	switch os.Args[1] {
	case "model":
		model(true)
	case "model-independent":
		model(false)
	case "corrupt-journal", "restore-journal":
		if len(os.Args) != 4 {
			panic("Session and incarnation required")
		}
		journalFault(os.Args[1], os.Args[2], os.Args[3])
	case "kill-worker":
		if len(os.Args) != 3 {
			panic("Session required")
		}
		killWorker(os.Args[2])
	case "native":
		native()
	case "upstream":
		upstream()
	default:
		panic("unknown fixture")
	}
}

// upstream is the deterministic model service the Project gateway forwards to. It
// answers the native messages protocol so real Pi can complete a turn through it,
// records the host-derived attribution of every request, and holds the first
// request from each Session until both have arrived — so a completed run proves
// concurrent attribution rather than incidental ordering.
//
// It runs under the gateway's UID and is the only holder of the upstream secret.
func upstream() {
	address := os.Getenv("PROJECT_UPSTREAM_ADDR")
	if address == "" {
		address = "127.0.0.1:18890"
	}
	report := os.Getenv("PROJECT_UPSTREAM_REPORT")
	expected := os.Getenv("PROJECT_UPSTREAM_SECRET")
	overlap, _ := strconv.Atoi(os.Getenv("PROJECT_UPSTREAM_OVERLAP"))
	var mu sync.Mutex
	sessions := map[string]bool{}
	together := make(chan struct{})
	if overlap < 1 {
		close(together)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			fmt.Fprintln(w, "ready")
			return
		}
		if expected != "" && r.Header.Get("Authorization") != "Bearer "+expected {
			http.Error(w, "upstream credential missing", 401)
			return
		}
		observed := map[string]string{
			"path":        r.URL.Path,
			"project":     r.Header.Get("X-Project-Id"),
			"session":     r.Header.Get("X-Session-Id"),
			"turn":        r.Header.Get("X-Turn-Id"),
			"incarnation": r.Header.Get("X-Incarnation-Id"),
		}
		if report != "" {
			line, _ := json.Marshal(observed)
			mu.Lock()
			if file, err := os.OpenFile(report, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				file.Write(append(line, '\n'))
				file.Close()
			}
			mu.Unlock()
		}
		requestBody, bodyErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if bodyErr != nil {
			http.Error(w, "unreadable request", 400)
			return
		}
		if os.Getenv("PROJECT_UPSTREAM_REQUIRE_CONTEXT") == "1" {
			for _, marker := range []string{"Project working context", "Reviewed release qualification context.", "Reviewed qualification skill"} {
				if !bytes.Contains(requestBody, []byte(marker)) {
					http.Error(w, "reviewed native context missing", 422)
					return
				}
			}
		}
		mu.Lock()
		if !sessions[observed["session"]] {
			sessions[observed["session"]] = true
			if len(sessions) >= overlap && overlap > 0 {
				select {
				case <-together:
				default:
					close(together)
				}
			}
		}
		mu.Unlock()
		select {
		case <-together:
		case <-r.Context().Done():
			return
		case <-time.After(30 * time.Second):
			http.Error(w, "overlap timeout", 408)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, raw := range []string{
			`{"type":"message_start","message":{"id":"fixture","type":"message","role":"assistant","model":"local-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"deterministic-local-reply"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		} {
			var value map[string]any
			_ = json.Unmarshal([]byte(raw), &value)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", value["type"], raw)
			w.(http.Flusher).Flush()
		}
		if completed := os.Getenv("PROJECT_UPSTREAM_COMPLETIONS"); completed != "" {
			observed["completed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
			line, _ := json.Marshal(observed)
			mu.Lock()
			if file, e := os.OpenFile(completed, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); e == nil {
				_, _ = file.Write(append(line, '\n'))
				_ = file.Close()
			}
			mu.Unlock()
		}
	})
	fmt.Println("upstream-ready")
	if err := http.ListenAndServe(address, mux); err != nil {
		panic(err)
	}
}
func model(coordinated bool) {
	var mu sync.Mutex
	attempts := map[string]int{}
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
		observed := map[string]string{"project": r.Header.Get("X-Project-Id"), "session": r.Header.Get("X-Session-Id"), "turn": r.Header.Get("X-Turn-Id"), "incarnation": r.Header.Get("X-Incarnation-Id")}
		record := func(path string) {
			if path == "" {
				return
			}
			line, _ := json.Marshal(observed)
			mu.Lock()
			defer mu.Unlock()
			if file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
				file.Write(append(line, '\n'))
				file.Close()
			}
		}

		mu.Lock()
		count++
		attempts[observed["turn"]]++
		attempt := attempts[observed["turn"]]
		if count == 2 {
			close(both)
			if coordinated {
				fmt.Println("overlapping-model-requests")
			}
		}
		mu.Unlock()
		observed["attempt"] = strconv.Itoa(attempt)
		record(os.Getenv("PROJECT_UPSTREAM_REPORT"))
		requestMessages, _ := request["messages"].([]any)
		if len(requestMessages) > 0 {
			last, _ := json.Marshal(requestMessages[len(requestMessages)-1])
			if bytes.Contains(last, []byte("native-retry-until-stop")) || bytes.Contains(last, []byte("native-retry-exhaust")) || (attempt == 1 && bytes.Contains(last, []byte("native-retry-once"))) {
				http.Error(w, "503 overloaded native retry fixture", http.StatusServiceUnavailable)
				return
			}
		}
		if coordinated {
			select {
			case <-both:
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Second):
				http.Error(w, "overlap timeout", 408)
				return
			}
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

		slow := false
		streamChunks := 200
		pressure := false
		messages, _ := request["messages"].([]any)
		if len(messages) > 0 {
			last, _ := json.Marshal(messages[len(messages)-1])
			slow = bytes.Contains(last, []byte("long-for-interrupt"))
			if bytes.Contains(last, []byte("long-for-owner-handoff")) {
				slow = true
				streamChunks = 600
			}
			pressure = bytes.Contains(last, []byte("pressure-native"))
			if bytes.Contains(last, []byte("offline-ring")) {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
				}
				delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": strings.Repeat("durable-native-output ", 12000)}})
				records[2] = string(delta)
			}

			if bytes.Contains(last, []byte("crash-descendants")) {
				input, _ := json.Marshal(map[string]string{"command": "setsid sh -c 'echo $$ > /workspace/escaped-fault.pid; exec sleep 300' & echo ready > /workspace/fault-ready; sleep 300"})
				delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(input)}})
				records = []string{records[0], `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"fault-tool","name":"bash","input":{}}}`, string(delta), records[3], `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":4}}`, records[5]}
			}
		}
		if len(requestMessages) > 0 {
			last, _ := requestMessages[len(requestMessages)-1].(map[string]any)
			blocks, _ := last["content"].([]any)
			if len(blocks) == 1 {
				block, _ := blocks[0].(map[string]any)
				switch block["text"] {
				case "native-compaction-seed":
					delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": strings.Repeat("retained context ", 400)}})
					records[2] = string(delta)
				case "native-compaction-trigger":
					records[0] = strings.Replace(records[0], `"input_tokens":1`, `"input_tokens":8150`, 1)
				}
			}
		}
		if os.Getenv("PROJECT_RELEASE_COMPACTION_TEST") == "1" {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(750 * time.Millisecond):
			}
		}
		if slow {
			output := []string{records[0], records[1]}
			for n := 0; n < streamChunks; n++ {
				output = append(output, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streaming "}}`)
			}
			records = append(output, records[3:]...)
		}
		if pressure {
			output := []string{records[0], records[1]}
			for n := 0; n < 204; n++ {
				raw, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": strings.Repeat("capacity-cycle ", 300)}})
				output = append(output, string(raw))
			}
			records = append(output, records[3:]...)
		}
		for _, raw := range records {
			var value map[string]any
			_ = json.Unmarshal([]byte(raw), &value)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", value["type"], raw)
			w.(http.Flusher).Flush()
			if slow || pressure {
				delay := 20 * time.Millisecond
				if pressure {
					delay = 10 * time.Millisecond
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(delay):
				}
			}
		}
		observed["completed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		record(os.Getenv("PROJECT_UPSTREAM_COMPLETIONS"))

	})
	fmt.Println("model-ready")
	if err := http.ListenAndServe(modelAddress(), mux); err != nil {
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
			raw, _ := json.Marshal(map[string]string{"type": "prompt_end", "id": command.ID, "outcome": "completed"})
			fmt.Println(string(raw))
		}
	}
}

func killWorker(session string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(string(raw), "\x00")
		if len(args) == 0 || filepath.Base(args[0]) != "agent-runner" {
			continue
		}
		worker, match := false, false
		for i, arg := range args {
			if arg == "--worker" {
				worker = true
			}
			if arg == "--session" && i+1 < len(args) && args[i+1] == session {
				match = true
			}
		}
		if worker && match {
			if err = syscall.Kill(pid, syscall.SIGKILL); err != nil {
				panic(err)
			}
			fmt.Println("killed worker", pid)
			return
		}
	}
	panic("worker not found")
}

func journalFault(mode, session, incarnation string) {
	for _, id := range []string{session, incarnation} {
		if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
			panic("invalid fixture identity")
		}
	}
	path := filepath.Join("/runtime/sessions", session, "incarnations", incarnation, "journal.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		file := path + suffix
		if mode == "corrupt-journal" {
			if _, err := os.Stat(file); os.IsNotExist(err) {
				continue
			} else if err != nil {
				panic(err)
			}
			if err := os.Rename(file, file+".fixture-backup"); err != nil {
				panic(err)
			}
		} else {
			if _, err := os.Stat(file + ".fixture-backup"); os.IsNotExist(err) {
				continue
			} else if err != nil {
				panic(err)
			}
			if err := os.Rename(file+".fixture-backup", file); err != nil {
				panic(err)
			}
		}
	}
	if mode == "corrupt-journal" {
		if err := os.WriteFile(path, []byte("intentionally invalid test journal"), 0600); err != nil {
			panic(err)
		}
	}
}

func modelAddress() string {
	if address := os.Getenv("PROJECT_MODEL_ADDR"); address != "" {
		return address
	}
	return "127.0.0.1:18889"
}
