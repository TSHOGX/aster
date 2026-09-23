//go:build khoraintegration

package application_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleasedPausedVolumeFiles(t *testing.T) {
	t.Setenv("PROJECT_RELEASE_MODEL_MODE", "model-independent")
	a := newReleasedApp(t)
	base := "/api/projects/paused-volume"
	socket := filepath.Join(a.root, "state/control.sock")
	defer func() {
		if t.Failed() {
			for _, route := range []string{base, base + "/sessions/paused-native", base + "/sessions/paused-native/turns"} {
				resp, e := a.client.Get(a.origin + route)
				if e == nil {
					data, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					t.Log(route, resp.StatusCode, string(data))
				}
			}
			t.Log("diagnostics", a.cli("diagnostics", "--socket", socket, "--project", "paused-volume"))
		}
	}()
	a.request("POST", "/api/projects", map[string]string{"id": "paused-volume", "title": "Storage without compute"}, 201)
	assertCold := func(state string) {
		t.Helper()
		if p := a.request("GET", base, nil, 200); p["state"] != state {
			t.Fatal("file operation changed compute state", p)
		}
		counts := a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)
		if counts["occupied_projects"] != float64(0) {
			t.Fatal("files reserved compute", counts)
		}
	}
	read := func(path, expected string) {
		t.Helper()
		response, err := a.client.Get(a.origin + base + "/files/content?path=" + url.QueryEscape(path))
		require(t, err)
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		require(t, err)
		if response.StatusCode != 200 || string(data) != expected {
			t.Fatalf("file %s: status %d bytes %d", path, response.StatusCode, len(data))
		}
	}
	// Before the first Agent/Terminal, only a workspace volume is necessary.
	a.request("GET", base+"/files?path=&limit=100", nil, 200)
	a.writeFile(base+"/files/content?path=initial.txt", "initial bytes", "initial-upload")
	read("initial.txt", "initial bytes")
	assertCold("created")
	session := base + "/sessions/native"
	a.request("POST", base+"/sessions", map[string]string{"id": "paused-native", "title": "Native history"}, 201)
	session = base + "/sessions/paused-native"
	a.request("POST", session+"/turns", map[string]string{"request_id": "once", "content": "write a short reply"}, 201)
	a.await(45*time.Second, "native completion", func() bool {
		v := a.request("GET", session+"/turns", nil, 200)
		turns, _ := v["turns"].([]any)
		return len(turns) == 1 && turns[0].(map[string]any)["state"] == "completed"
	})
	// The fixture's normal idle policy releases compute without discarding storage.
	a.await(25*time.Second, "idle pause", func() bool { return a.request("GET", base, nil, 200)["state"] == "paused" })
	history := a.request("GET", session+"/view/pages", nil, 200)
	raw, _ := json.Marshal(history)
	if !bytes.Contains(raw, []byte("deterministic-local-reply")) {
		t.Fatal("paused history unavailable")
	}
	for i, text := range []string{"updated", "", strings.Repeat("bounded chunk\n", 40000)} {
		a.writeFile(base+"/files/content?path=initial.txt", text, fmt.Sprintf("paused-write-%d", i))
		read("initial.txt", text)
		assertCold("paused")
	}
	a.writeFile(base+"/files/content?path="+url.QueryEscape(" leading and trailing.txt "), "exact name", "spaced")
	read(" leading and trailing.txt ", "exact name")
	for _, body := range []map[string]string{
		{"id": "directory", "operation": "mkdir", "path": "reports"},
		{"id": "rename", "operation": "move", "path": "initial.txt", "destination": "reports/result.txt"},
		{"id": "remove", "operation": "remove", "path": "reports/result.txt"},
	} {
		a.request("POST", base+"/files", body, 200)
		assertCold("paused")
	}
	page := a.request("GET", base+"/files?path=&limit=1", nil, 200)
	if cursor, _ := page["next"].(string); cursor != "" {
		a.request("GET", base+"/files?path=&limit=1&after="+url.QueryEscape(cursor), nil, 200)
	} else {
		t.Fatal("directory pagination missing")
	}
	assertCold("paused")
	a.request("POST", base+"/resume", nil, 202)
	a.await(20*time.Second, "resume retained storage", func() bool { return a.request("GET", base, nil, 200)["state"] == "active" })
	read(" leading and trailing.txt ", "exact name")
	a.await(25*time.Second, "pause again", func() bool { return a.request("GET", base, nil, 200)["state"] == "paused" })
	assertCold("paused")
	a.request("GET", session+"/view/pages", nil, 200)
	p := a.request("GET", base, nil, 200)
	a.request("DELETE", fmt.Sprintf("%s?revision=%.0f", base, p["revision"]), nil, 202)
	a.await(20*time.Second, "storage-only files cleanup", func() bool {
		return a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["projects"].(map[string]any)["deleted"] == float64(1)
	})
}
