//go:build khoraintegration

package application_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReleasedBuiltinVolumePublication(t *testing.T) {
	a := newReleasedApp(t)
	socket := filepath.Join(a.root, "state/control.sock")
	source := filepath.Join(a.root, "dataset")
	require(t, os.Mkdir(source, 0755))
	require(t, os.WriteFile(filepath.Join(source, "example.txt"), []byte("reviewed original"), 0644))
	measured := a.cli("volume", "inspect-source", "--source", source)
	digest := measured["digest"].(string)
	args := []string{"volume", "publish", "--socket", socket, "--source", source, "--id", "examples", "--version", "v1", "--digest", digest, "--capacity-bytes", "8388608", "--minimum-bytes", "1048576", "--roles", "workspace,data"}
	before := a.request("GET", "/api/admin/runtime/default", nil, 200)
	first := a.cli(args...)
	if replay := a.cli(args...); !reflect.DeepEqual(first, replay) || replay["default_changed"] != false {
		t.Fatal(first, replay)
	}
	after := a.request("GET", "/api/admin/runtime/default", nil, 200)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("publication selected a new default")
	}
	// A changed request cannot silently relabel an already published capacity.
	changed := append([]string{}, args...)
	for index := range changed {
		if changed[index] == "8388608" {
			changed[index] = "16777216"
		}
	}
	if raw, err := exec.CommandContext(a.ctx, a.binary, changed...).CombinedOutput(); err == nil || !strings.Contains(string(raw), "different content or capacity") {
		t.Fatal(string(raw), err)
	}
	options := a.request("GET", "/api/admin/runtime/options?kind=volume", nil, 200)["options"].([]any)
	found := false
	for _, raw := range options {
		option := raw.(map[string]any)
		if option["reference"].(map[string]any)["id"] == "examples" {
			found = option["availability"] == "available" && option["source_capacity_bytes"] == "8388608" && option["minimum_bytes"] == "1048576"
		}
	}
	if !found {
		t.Fatal(options)
	}
	for _, raw := range after["mounts"].([]any) {
		mount := raw.(map[string]any)
		if mount["role"] == "workspace" {
			mount["source"] = map[string]any{"kind": "builtin", "artifact_id": "examples", "version": "v1", "digest": digest}
		}
	}
	body := map[string]any{"expected_revision": after["revision"], "sandbox_release": after["sandbox_release"], "mounts": after["mounts"]}
	preview := a.request("POST", "/api/admin/runtime/default/preview", body, 200)
	body["request_id"], body["expected_dependencies"] = "select-published-workspace", preview["expected_dependencies"]
	a.request("PATCH", "/api/admin/runtime/default", body, 200)
	read := func(base string) string {
		t.Helper()
		response, err := a.client.Get(a.origin + base + "/files/content?path=example.txt")
		require(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require(t, err)
		if response.StatusCode != 200 {
			t.Fatal(response.StatusCode, string(raw))
		}
		return string(raw)
	}
	for _, id := range []string{"seed-a", "seed-b"} {
		base := "/api/projects/" + id
		a.request("POST", "/api/projects", map[string]string{"id": id, "title": "Operator published seed"}, 201)
		a.writeFile(base+"/files/content?path=uploaded.txt", "first upload", "upload-"+id)
		if value := read(base); value != "reviewed original" {
			t.Fatal(value)
		}
	}
	a.writeFile("/api/projects/seed-a/files/content?path=example.txt", "private changed copy", "change-seed-copy")
	a.stop()
	a.start()
	if read("/api/projects/seed-a") != "private changed copy" || read("/api/projects/seed-b") != "reviewed original" {
		t.Fatal("copy reset or source aliased")
	}
	for _, id := range []string{"seed-a", "seed-b"} {
		base := "/api/projects/" + id
		p := a.request("GET", base, nil, 200)
		a.request("DELETE", fmt.Sprintf("%s?revision=%.0f", base, p["revision"]), nil, 202)
	}
	a.await(25*time.Second, "published copy cleanup", func() bool {
		return a.cli("diagnostics", "--socket", socket)["state"].(map[string]any)["projects"].(map[string]any)["deleted"] == float64(2)
	})
	if replay := a.cli(args...); replay["default_changed"] != false {
		t.Fatal(replay)
	}
	t.Log("exact operator publication replay, capacity conflict, unchanged default, selectable builtin metadata, independent first-upload seed copies, preserved edits after owner restart and cleanup passed")
}
