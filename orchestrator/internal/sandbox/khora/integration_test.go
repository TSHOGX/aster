//go:build khoraintegration

package khora

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/bridge"
	"harness-platform/orchestrator/internal/sandbox"
	guestfiles "harness-platform/orchestrator/internal/workspace/guest"

	sdk "github.com/TSHOGX/khora/pkg/khora"
)

func TestKhoraTemplateShellAndNativeHistory(t *testing.T) {
	endpoint := os.Getenv("KHORA_INTEGRATION_ENDPOINT")
	template := os.Getenv("KHORA_INTEGRATION_TEMPLATE")
	if endpoint == "" || template == "" {
		t.Fatal("isolated KHORA_INTEGRATION_ENDPOINT and KHORA_INTEGRATION_TEMPLATE required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	backend, err := New(ctx, Config{Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var mounts []sandbox.Mount
	volumes := map[string]string{}
	for _, spec := range []struct{ purpose, destination, access string }{{"workspace", "/workspace", "exclusive"}, {"journal", "/harness-bridge", "exclusive"}, {"config", "/harness-config", "shared-ro"}, {"assets", "/root/.agents", "shared-ro"}} {
		volume, err := backend.CreateVolume(ctx, "template-test-"+spec.purpose, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		volumes[spec.purpose] = volume.VolumeID
		mounts = append(mounts, sandbox.Mount{VolumeID: volume.VolumeID, Destination: spec.destination, Access: spec.access})
	}
	if _, err := client.MkdirVolume(ctx, volumes["config"], "pi", 0755); err != nil {
		t.Fatal(err)
	}
	models := `{"providers":{"harness_anthropic_proxy":{"baseUrl":"http://model.internal:443","api":"anthropic-messages","apiKey":"harness-placeholder","models":[{"id":"synthetic-model","name":"synthetic-model","reasoning":false,"input":["text"],"contextWindow":8192,"maxTokens":256,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`
	for name, body := range map[string]string{"pi/models.json": models, "pi/settings.json": `{"steeringMode":"all","followUpMode":"all","compaction":{"enabled":false}}`} {
		if _, err := client.WriteVolume(ctx, volumes["config"], name, 0, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.WriteVolume(ctx, volumes["assets"], "AGENTS.md", 0, []byte("Synthetic shared context\n")); err != nil {
		t.Fatal(err)
	}
	observed, err := backend.Create(ctx, sandbox.CreateRequest{IdempotencyKey: "template-test-sandbox", TemplateID: template, CPUMillis: 1000, MemoryBytes: 1 << 30, DiskBytes: 256 << 20, Mounts: mounts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = client.Destroy(cleanup, observed.SandboxID)
		for _, volume := range volumes {
			_ = client.DeleteVolume(cleanup, volume)
		}
	})
	reference := sandbox.ProcessReference{StoreID: observed.StoreID, SandboxID: observed.SandboxID, Handle: "template-shell", Epoch: observed.Epoch}
	stream, err := backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: reference, IdempotencyKey: "template-shell-start", Command: "/usr/local/bin/harness-khora-launcher", Arguments: []string{"--session-id", "session", "--generation-id", "execution", "--driver-id", "pi", "--max-turns", "0"}, Directory: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	send := func(frame bridge.StreamFrame) {
		t.Helper()
		frame.Version = 1
		frame.Execution = "execution"
		raw, err := bridge.EncodeStreamFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.SendProcessInput(ctx, reference, raw); err != nil {
			t.Fatal(err)
		}
	}
	send(bridge.StreamFrame{Kind: "sync", CommandID: "initial-sync"})
	decoder := &bridge.StreamDecoder{}
	var output strings.Builder
	complete := false
	var records uint64
	for !complete {
		event, err := stream.Next()
		if err != nil {
			t.Fatalf("guest stream: %v; output=%s", err, output.String())
		}
		if event.Kind == "end" {
			t.Fatalf("bridge ended early: %d %s", event.ExitCode, output.String())
		}
		if event.Kind != "data" {
			continue
		}
		if event.Stream == "stderr" {
			t.Logf("guest diagnostic: %s", event.Data)
			continue
		}
		frames, err := decoder.Feed(event.Data)
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range frames {
			if frame.Kind != "record" {
				continue
			}
			records++
			if frame.Sequence != records {
				t.Fatalf("journal gap: %d want %d", frame.Sequence, records)
			}
			envelope := frame.Envelope
			var responseType string
			var payload any
			switch envelope.Type {
			case bridge.TypeHello:
				responseType = bridge.TypeHelloAck
				payload = map[string]any{"last_output_sequence_by_turn": map[string]int64{}}
			case bridge.TypeProbeNetwork:
				responseType = bridge.TypeNoWork
				payload = map[string]string{"status": "probe_ok"}
			case bridge.TypeClaimNextTurn:
				responseType = bridge.TypeGrant
				payload = map[string]any{"turn_id": 1, "sequence": 1, "attempt": 1, "turn_input_schema": "RunTurn", "input": map[string]string{"content": "printf 'retained-workspace\n' > /workspace/retained.txt; printf 'khora-shell-ok\n'"}}
			case bridge.TypeEmitOutput:
				output.Write(envelope.Payload)
			case bridge.TypeAckTurnCompleted:
				var done struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(envelope.Payload, &done); err != nil || done.Status != "completed" {
					t.Fatalf("shell completion: %s %v", envelope.Payload, err)
				}
				complete = true
			}
			if responseType != "" {
				raw, _ := json.Marshal(payload)
				send(bridge.StreamFrame{Kind: "command", CommandID: "response-" + envelope.MessageID, Envelope: &bridge.Envelope{MessageID: "response-" + envelope.MessageID, RequestID: envelope.RequestID, SessionID: "session", GenerationID: "execution", Type: responseType, Payload: raw}})
			}
			if !complete {
				send(bridge.StreamFrame{Kind: "ack", After: frame.Sequence})
			}
		}
	}
	stream.Close()
	if !strings.Contains(output.String(), "khora-shell-ok") {
		t.Fatalf("shell output missing: %s", output.String())
	}
	confined := runProbe(t, ctx, backend, observed.Reference, "workspace-helper", `import sys,os,io,hashlib
sys.path.insert(0,'/usr/local/lib')
from harness_files import open_root,replace,read_range,file_digest
root=open_root('/workspace')
try:
 payload=b'durable guest helper content'
 result=replace(root,'nested/helper.txt',io.BytesIO(payload),len(payload),hashlib.sha256(payload).hexdigest(),'substrate-operation')
 output=io.BytesIO();read_range(root,'nested/helper.txt',output,3,7)
 assert output.getvalue()==payload[3:10]
 assert file_digest(root,'nested/helper.txt',sync=True)['sha256']==result['sha256']
 os.symlink('/root','/workspace/escape')
 try:replace(root,'escape/outside',io.BytesIO(b'x'),1,hashlib.sha256(b'x').hexdigest(),'escape-operation')
 except (OSError,ValueError):pass
 else:raise AssertionError('symlink parent accepted')
 print('workspace-helper-ok')
finally:os.close(root)
`)
	if !strings.Contains(confined, "workspace-helper-ok") {
		t.Fatalf("guest helper failed: %s", confined)
	}
	remote := guestfiles.Files{Backend: backend, Reference: observed.Reference, Root: "/workspace"}
	content := "streamed remote replacement"
	digest := sha256.Sum256([]byte(content))
	if _, err := remote.ReplacePrepared(ctx, "remote-replace", "remote/file.txt", int64(len(content)), hex.EncodeToString(digest[:]), strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	reader, metadata, err := remote.Open(ctx, "remote/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Seek(9, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(read) != content[9:] || metadata.Size != int64(len(content)) {
		t.Fatalf("remote range: %s %+v %v", read, metadata, err)
	}
	// Exercise the real pinned Pi SessionManager and RPC selector without model
	// traffic; gateway credential/protocol acceptance remains a separate gate.
	script := piNativeProbe(false)
	first := runProbe(t, ctx, backend, observed.Reference, "pi-seed", script)
	if !strings.Contains(first, "native-history-ok") {
		t.Fatalf("Pi seed: %s", first)
	}
	paused, err := backend.Pause(ctx, observed.Reference)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := client.ReadVolume(ctx, volumes["workspace"], "retained.txt", 0, 128)
	if err != nil || string(retained) != "retained-workspace\n" {
		t.Fatalf("offline workspace: %s %v", retained, err)
	}
	// Observe all mounts at the same revision, then detach assets. Retry the
	// original conditional configure as if its successful response were lost.
	binding, err := backend.InspectConfiguration(ctx, paused.Reference)
	if err != nil || !sandbox.SameConfiguration(binding, sandbox.Network{}, mounts) {
		t.Fatalf("paused configuration observation: %+v %v", binding, err)
	}
	var withoutAssets []sandbox.Mount
	for _, mount := range mounts {
		if mount.Destination != "/root/.agents" {
			withoutAssets = append(withoutAssets, mount)
		}
	}
	configured, err := backend.Configure(ctx, paused.Reference, sandbox.Network{}, withoutAssets)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := backend.Configure(ctx, paused.Reference, sandbox.Network{}, withoutAssets)
	if err != nil || replayed.Reference != configured.Reference {
		t.Fatalf("lost configure reply not reconciled: %+v %v", replayed, err)
	}
	if _, err := backend.Configure(ctx, paused.Reference, sandbox.Network{}, mounts); !sandbox.Is(err, sandbox.Precondition) {
		t.Fatalf("stale configure replaced detached binding: %v", err)
	}
	detached, err := backend.InspectConfiguration(ctx, configured.Reference)
	if err != nil || !sandbox.SameConfiguration(detached, sandbox.Network{}, withoutAssets) {
		t.Fatalf("detach not observed: %+v %v", detached, err)
	}
	restored, err := backend.Configure(ctx, configured.Reference, sandbox.Network{}, mounts)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := backend.Resume(ctx, restored.Reference)
	if err != nil {
		t.Fatal(err)
	}
	second := runProbe(t, ctx, backend, resumed.Reference, "pi-restore", piNativeProbe(true))
	if !strings.Contains(second, "native-history-ok") {
		t.Fatalf("Pi cold restore: %s", second)
	}
	t.Logf("engine=%s template=%s shell journal records=%d cold epoch=%d->%d native Pi restore verified", EngineRevision, template, records, observed.Epoch, resumed.Epoch)
}

func runProbe(t *testing.T, ctx context.Context, backend *Backend, reference sandbox.Reference, key, script string) string {
	t.Helper()
	stream, err := backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: reference.StoreID, SandboxID: reference.SandboxID, Handle: key, Epoch: reference.Epoch}, IdempotencyKey: key, Command: "/usr/bin/python3", Arguments: []string{"-c", script}, Directory: "/workspace", Environment: []string{"PATH=/opt/node-v24.15.0/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/root"}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var output strings.Builder
	for {
		frame, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if frame.Kind == "data" {
			output.Write(frame.Data)
		}
		if frame.Kind == "end" {
			if frame.ExitCode != 0 {
				t.Fatalf("probe exit=%d: %s", frame.ExitCode, output.String())
			}
			return output.String()
		}
	}
	t.Fatalf("probe lost terminal output: %s", output.String())
	return ""
}

func piNativeProbe(restore bool) string {
	return fmt.Sprintf(`
import json,os,pathlib,subprocess,sys
sys.path.insert(0,'/usr/local/lib')
from harness_launch import prepare_pi,launch_environment
prepare_pi()
environment=launch_environment('pi')
package='/usr/local/lib/node_modules/@earendil-works/pi-coding-agent/dist/'
seed_path=pathlib.Path('/root/native-selector.json')
if not %s:
 script="""const {SessionManager}=await import('file://'+process.argv[1]+'core/session-manager.js');
 const s=SessionManager.create('/workspace','/root/.pi/agent/sessions');
 s.appendMessage({role:'user',content:'synthetic native history',timestamp:Date.now()});
 s.appendMessage({role:'assistant',content:[{type:'text',text:'synthetic native reply'}],api:'anthropic-messages',provider:'harness_anthropic_proxy',model:'synthetic-model',usage:{input:1,output:1,cacheRead:0,cacheWrite:0,totalTokens:2,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}},stopReason:'stop',timestamp:Date.now()});
 console.log(JSON.stringify({path:s.getSessionFile(),id:s.getSessionId()}));"""
 seed=json.loads(subprocess.check_output(['node','--input-type=module','-e',script,package],env=environment,text=True))
 seed_path.write_text(json.dumps(seed))
 for path in [seed_path,pathlib.Path(seed['path'])]:
  with open(path,'rb') as f:os.fsync(f.fileno())
else:seed=json.loads(seed_path.read_text())
process=subprocess.Popen(['pi','--mode','rpc','--provider','harness_anthropic_proxy','--model','synthetic-model','--session-dir','/root/.pi/agent/sessions','--no-extensions','--no-themes'],env=environment,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
def call(kind,**values):
 process.stdin.write(json.dumps(dict(id=kind,type=kind,**values))+'\n');process.stdin.flush()
 for line in process.stdout:
  event=json.loads(line)
  if event.get('id')==kind:
   assert event.get('success'),event
   return event.get('data')
 raise RuntimeError(process.stderr.read())
try:
 state=call('get_state');assert state['steeringMode']=='all',state
 call('switch_session',sessionPath=seed['path'])
 stats=call('get_session_stats');assert stats['sessionId']==seed['id'] and stats['sessionFile']==seed['path'],stats
 messages=call('get_messages');assert len(messages['messages'])==2,messages
 assert pathlib.Path('/root/.pi/agent/AGENTS.md').read_text()=='Synthetic shared context\n'
 print('native-history-ok')
finally:process.terminate();process.wait(timeout=5)
`, map[bool]string{true: "True", false: "False"}[restore])
}
