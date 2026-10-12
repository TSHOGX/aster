//go:build khoraintegration

package producthttp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"harness-platform/orchestrator/internal/producttest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/sandbox/khora"
	"harness-platform/orchestrator/internal/sqlitex"
)

func TestComposedProjectRecoveryOnKhora(t *testing.T) {
	endpoint := os.Getenv("PROJECT_TEST_ENDPOINT")
	if endpoint == "" {
		t.Fatal("isolated endpoint required")
	}
	runctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var restartedEngine *exec.Cmd
	defer func() {
		if restartedEngine != nil {
			_ = restartedEngine.Process.Kill()
			_ = restartedEngine.Wait()
		}
	}()
	api, err := sdk.New(endpoint)
	must(t, err)
	for i := 0; i < 100; i++ {
		if api.Ready(runctx) == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	backend, err := khora.New(runctx, khora.Config{Endpoint: endpoint})
	must(t, err)
	identity, err := backend.Identity(runctx)
	must(t, err)
	fixture := &browserSubstrate{Backend: backend, api: api, binaries: os.Getenv("PROJECT_TEST_BIN_DIR"), initialized: map[string]bool{}, modelMode: "model-independent", loseDeleteReplies: true, destroyed: make(chan struct{}, 1)}
	directory := t.TempDir()
	dbPath := filepath.Join(directory, "product.db")
	profile := testProfile()
	profile.StoreID = identity.StoreID
	profile.TemplateID = "agent-runtime"
	profile.DiskBytes = 256 << 20
	sharedBase, err := api.CreateVolume(runctx, 8<<20)
	must(t, err)
	_, err = api.WriteVolume(runctx, sharedBase.VolumeID, "sentinel", 0, []byte("published-shared-asset"))
	must(t, err)
	sharedVolume, err := api.SnapshotVolume(runctx, sharedBase.VolumeID, 8<<20)
	must(t, err)
	sharedEntries := []content.Entry{{Path: "sentinel", Size: int64(len("published-shared-asset")), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("published-shared-asset")))}}
	profile.Volumes = append(profile.Volumes, product.VolumePlan{Key: "shared", Role: "shared_assets", Destination: "/opt/shared-assets", Storage: "shared_readonly", Access: "read_only", CapacityBytes: 8 << 20, Source: &product.VolumeSource{Reference: product.ArtifactReference{ID: "shared_assets", Version: "v1", Digest: content.Digest(sharedEntries)}, VolumeID: sharedVolume, MinimumBytes: 8 << 20}})

	store, profile, err := producttest.OpenWithRuntime(runctx, dbPath, profile, backend, map[string][]content.Entry{content.Digest(sharedEntries): sharedEntries}, content.Publication{StoreID: identity.StoreID, Purpose: "assets", RevisionDigest: content.Digest(sharedEntries), Capacity: 8 << 20, Entries: sharedEntries, StagingVolumeID: sharedBase.VolumeID, DerivedVolumeID: sharedVolume, PublishedVolumeID: sharedVolume})
	must(t, err)
	reader, err := sqlitex.OpenReader(runctx, dbPath, 1)
	must(t, err)
	defer reader.Close()

	scope := product.Scope{Actor: "owner", ProjectID: "recovery"}
	_, err = store.CreateProject(runctx, scope.Actor, scope.ProjectID, "Recovery")
	must(t, err)
	for _, id := range []string{"alpha", "beta", "fault"} {
		_, err = store.CreateSession(runctx, scope, id, id)
		must(t, err)
	}
	var lifecycle *product.Lifecycle
	var agents *product.Agents
	var terminals *product.Terminals
	var shares *product.Shares
	var stop context.CancelFunc
	var done chan struct{}
	start := func() {
		lifecycle = &product.Lifecycle{VerifyRuntime: backend.VerifyRuntime, Store: store, Backend: fixture, MaxProjects: 2}
		agents = product.NewAgents(runctx, store, lifecycle, backend, profile.Bindings(), 16)
		terminals = product.NewTerminals(runctx, store, lifecycle, backend, profile.Bindings(), 16)
		files := &product.Files{Store: store, Lifecycle: lifecycle, Backend: backend, Bindings: profile.Bindings()}
		shares = &product.Shares{Store: store, Files: files, Directory: filepath.Join(directory, "shares")}
		captures := &product.Captures{Store: store, Backend: backend, Directory: filepath.Join(directory, "captures")}
		lifecycle.Artifacts = &product.Artifacts{Store: store, Captures: captures, Shares: shares}
		recovery := &product.AgentRecovery{Store: store, Lifecycle: lifecycle, Backend: backend}
		retirement := &product.ChildRetirement{Store: store, Lifecycle: lifecycle, Backend: backend, Captures: captures, Shares: shares}
		workers := &product.Workers{Store: store, Lifecycle: lifecycle, Agents: agents, Terminals: terminals, Files: files, Shares: shares, Captures: captures, Recovery: recovery, Retirement: retirement, IdleAfter: 10 * time.Minute, Interval: 100 * time.Millisecond, Report: func(kind string, err error) { t.Logf("%s: %v", kind, err) }}
		var workerctx context.Context
		workerctx, stop = context.WithCancel(runctx)
		done = make(chan struct{})
		go func() { defer close(done); workers.Run(workerctx) }()
	}
	shutdown := func() { stop(); <-done; agents.Close(); terminals.Close(); must(t, store.Close()) }
	start()
	defer func() { shutdown() }()
	await := func(t *testing.T, label string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("timeout: " + label)
	}
	turn := func(t *testing.T, session, request, prompt string) {
		t.Helper()
		_, err := store.AdmitTurn(runctx, scope, session, request, prompt, false)
		must(t, err)
		await(t, "turn "+request, func() bool {
			turns, e := store.Turns(runctx, scope, session, 0, 100)
			if e != nil {
				return false
			}
			for _, item := range turns {
				if item.RequestID == request {
					return item.State == "completed"
				}
			}
			return false
		})
	}
	turn(t, "alpha", "first-alpha", "alpha first conversation")
	turn(t, "beta", "first-beta", "beta first conversation")
	original, err := store.Resources(runctx, scope)
	must(t, err)
	serial := 0
	shell := func(t *testing.T, command string) string {
		t.Helper()
		serial++
		r, err := store.Resources(runctx, scope)
		must(t, err)
		handle := fmt.Sprintf("recovery-probe-%d", serial)
		stream, err := backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/bin/sh", Arguments: []string{"-c", command}, Directory: "/workspace"})
		must(t, err)
		defer stream.Close()
		var out strings.Builder
		for {
			f, err := stream.Next()
			must(t, err)
			out.Write(f.Data)
			if f.Kind == "end" {
				if f.ExitCode != 0 {
					t.Fatalf("probe exit %d: %s", f.ExitCode, out.String())
				}
				return out.String()
			}
		}
	}
	var alphaInc, betaInc string

	must(t, reader.QueryRow(`SELECT id FROM agent_incarnations WHERE session_id='alpha' AND state='running'`).Scan(&alphaInc))
	must(t, reader.QueryRow(`SELECT id FROM agent_incarnations WHERE session_id='beta' AND state='running'`).Scan(&betaInc))
	_, err = store.CreateTerminal(runctx, scope, "kept", "Retained", 24, 80)
	must(t, err)
	var terminalInc product.TerminalIncarnation
	await(t, "initial terminal", func() bool {
		terminalInc, err = terminals.Start(runctx, scope, "kept", 24, 80)
		return err == nil && terminalInc.State == "running"
	})
	t.Run("application-restart-adopts-original-runners-and-PTY", func(t *testing.T) {
		shutdown()
		store, err = producttest.Open(runctx, dbPath)
		must(t, err)
		start()
		await(t, "PTY adoption", func() bool {
			view, e := terminals.Attach(runctx, scope, "kept", true)
			if e != nil {
				return false
			}
			view.Close()
			return true
		})
		current, err := store.LiveTerminal(runctx, scope, "kept")
		must(t, err)
		if current.ID != terminalInc.ID {
			t.Fatal("terminal replaced on app restart")
		}
		turn(t, "alpha", "after-restart", "alpha continues after owner restart")
		var id string
		must(t, reader.QueryRow(`SELECT id FROM agent_incarnations WHERE session_id='alpha' AND state='running'`).Scan(&id))
		if id != alphaInc {
			t.Fatal("app restart replaced Agent")
		}
		messages, err := store.Messages(runctx, scope, "alpha", 0, 100)
		must(t, err)
		if len(messages) != 4 {
			t.Fatal("lost or duplicated messages", messages)
		}
	})
	t.Run("PTY-exit-during-owner-outage-retains-its-real-code", func(t *testing.T) {
		_, err := store.CreateTerminal(runctx, scope, "exit-offline", "Offline exit", 24, 80)
		must(t, err)
		await(t, "offline-exit PTY", func() bool {
			i, e := terminals.Start(runctx, scope, "exit-offline", 24, 80)
			return e == nil && i.State == "running"
		})
		view, err := terminals.Attach(runctx, scope, "exit-offline", true)
		must(t, err)
		must(t, terminals.Input(runctx, view, []byte("sleep 1; exit 7\r")))
		view.Close()
		shutdown()
		time.Sleep(2 * time.Second)
		store, err = producttest.Open(runctx, dbPath)
		must(t, err)
		start()
		await(t, "retained terminal exit evidence", func() bool {
			terminal, e := store.Terminal(runctx, scope, "exit-offline")
			return e == nil && terminal.State == "exited" && terminal.ExitCode != nil && *terminal.ExitCode == 7
		})
	})
	t.Run("owner-disconnect-beyond-engine-ring-replays-durable-journal", func(t *testing.T) {
		_, err := store.AdmitTurn(runctx, scope, "alpha", "offline", "offline-ring", false)
		must(t, err)
		await(t, "native prompt applied", func() bool {
			turns, e := store.Turns(runctx, scope, "alpha", 0, 100)
			if e != nil {
				return false
			}
			for _, turn := range turns {
				if turn.RequestID == "offline" {
					return turn.State == "running"
				}
			}
			return false
		})
		shutdown()
		time.Sleep(2500 * time.Millisecond)
		store, err = producttest.Open(runctx, dbPath)
		must(t, err)
		start()
		await(t, "durable replay after owner disconnect", func() bool {
			turns, e := store.Turns(runctx, scope, "alpha", 0, 100)
			if e != nil {
				return false
			}
			for _, turn := range turns {
				if turn.RequestID == "offline" {
					return turn.State == "completed"
				}
			}
			return false
		})
		messages, err := store.Messages(runctx, scope, "alpha", 0, 100)
		must(t, err)
		if len(messages) != 6 || messages[5].Content != strings.Repeat("durable-native-output ", 12000) {
			t.Fatal("long native reply lost or duplicated")
		}
		var id string
		must(t, reader.QueryRow(`SELECT id FROM agent_incarnations WHERE session_id='alpha' AND state='running'`).Scan(&id))
		if id != alphaInc {
			t.Fatal("transport ring gap replaced the runner")
		}
	})
	t.Run("worker-crash-settles-escaped-descendants-without-sibling-restart", func(t *testing.T) {
		_, err := store.AdmitTurn(runctx, scope, "fault", "crash", "crash-descendants", false)
		must(t, err)
		await(t, "escaped descendant started", func() bool {
			return strings.Contains(shell(t, "test -s /workspace/escaped-fault.pid && echo ready || true"), "ready")
		})
		shell(t, "/tmp/runtime-fixture kill-worker fault")
		await(t, "journal and guardian cleanup", func() bool {
			var recovery, cleanup string
			e := reader.QueryRow(`SELECT recovery_state,cleanup_state FROM agent_incarnations WHERE session_id='fault' ORDER BY created_at DESC LIMIT 1`).Scan(&recovery, &cleanup)
			return e == nil && recovery == "complete" && cleanup == "settled"
		})
		state := shell(t, `p=$(cat /workspace/escaped-fault.pid); if [ -e /proc/$p/stat ]; then awk '{print $3}' /proc/$p/stat; else echo gone; fi`)
		if !strings.Contains(state, "gone") && !strings.Contains(state, "Z") {
			t.Fatal("escaped descendant survived guardian", state)
		}
		fault, err := store.Session(runctx, scope, "fault")
		must(t, err)
		if fault.State != "blocked" {
			t.Fatal("unknown native effect not blocked", fault)
		}
		turns, err := store.Turns(runctx, scope, "fault", 0, 10)
		must(t, err)
		if len(turns) != 1 || turns[0].State != "unknown" {
			t.Fatal(turns)
		}
		turn(t, "beta", "sibling-after-crash", "beta is independent")
		var id string
		must(t, reader.QueryRow(`SELECT id FROM agent_incarnations WHERE session_id='beta' AND state='running'`).Scan(&id))
		if id != betaInc {
			t.Fatal("sibling process was replaced")
		}
		r, err := store.Resources(runctx, scope)
		must(t, err)
		if r.Reference.Epoch != original.Reference.Epoch {
			t.Fatal("child crash restarted Project")
		}
		must(t, store.ContinueSession(runctx, scope, "fault", fault.Revision))
		turn(t, "fault", "after-ack", "continue from native history after acknowledgement")
	})
	t.Run("worker-crash-retains-uncertain-steer-without-next-run-leak", func(t *testing.T) {
		_, err := store.CreateSession(runctx, scope, "steer-crash", "Steer crash")
		must(t, err)
		_, err = store.AdmitTurn(runctx, scope, "steer-crash", "slow", "long-for-interrupt", false)
		must(t, err)
		await(t, "Steer crash prompt streaming", func() bool {
			var count int
			e := reader.QueryRow(`SELECT count(*) FROM agent_records r JOIN agent_incarnations i ON i.id=r.incarnation_id WHERE i.session_id='steer-crash' AND r.kind='native' AND json_extract(r.payload,'$.type')='message_start' AND json_extract(r.payload,'$.message.role')='assistant'`).Scan(&count)
			return e == nil && count > 0
		})
		head, err := store.ConversationHead(runctx, scope, "steer-crash")
		must(t, err)
		if head.ActiveRun == nil {
			t.Fatal("native run disappeared before Steer admission")
		}
		run := *head.ActiveRun
		source, err := store.AdmitTurn(runctx, scope, "steer-crash", "guidance", "accepted but uncertain guidance", true)
		must(t, err)
		receipt, err := store.ControlRun(runctx, scope, "steer-crash", product.ExecutionControl{
			RequestID: "crash-steer", Action: "steer", Source: source.ID,
			PromptRevision: source.PromptRevision, RunID: run.ID,
			IncarnationID: run.IncarnationID, Owner: run.Owner,
		})
		must(t, err)
		await(t, "native Steer accepted before crash", func() bool {
			var state string
			e := reader.QueryRow(`SELECT state FROM input_deliveries WHERE id=?`, receipt.DeliveryID).Scan(&state)
			return e == nil && state == "native_queued"
		})
		var token string
		must(t, reader.QueryRow(`SELECT input_token FROM input_deliveries WHERE id=?`, receipt.DeliveryID).Scan(&token))
		shell(t, "/tmp/runtime-fixture kill-worker steer-crash")
		await(t, "uncertain Steer journal recovery", func() bool {
			var recovery, cleanup, state string
			e := reader.QueryRow(`SELECT recovery_state,cleanup_state FROM agent_incarnations WHERE id=?`, run.IncarnationID).Scan(&recovery, &cleanup)
			if e != nil {
				return false
			}
			e = reader.QueryRow(`SELECT state FROM input_deliveries WHERE id=?`, receipt.DeliveryID).Scan(&state)
			return e == nil && recovery == "complete" && cleanup == "settled" && state == "unknown"
		})
		child, err := store.Session(runctx, scope, "steer-crash")
		must(t, err)
		if child.State != "blocked" {
			t.Fatal("uncertain Steer allowed execution", child)
		}
		must(t, store.ContinueSession(runctx, scope, "steer-crash", child.Revision))
		turn(t, "steer-crash", "after-uncertain-steer", "new explicit work after recovery")
		var state string
		must(t, reader.QueryRow(`SELECT state FROM input_deliveries WHERE id=?`, receipt.DeliveryID).Scan(&state))
		if state != "unknown" {
			t.Fatal("unknown Steer was silently resolved or resent", state)
		}
		var consumed int
		must(t, reader.QueryRow(`SELECT count(*) FROM agent_records WHERE kind='native' AND json_extract(payload,'$.type')='input_consumed' AND json_extract(payload,'$.input_token')=?`, token).Scan(&consumed))
		if consumed != 0 {
			t.Fatal("old native Steer leaked into the new run", consumed)
		}
	})
	t.Run("Session-retirement-preserves-workspace-and-siblings", func(t *testing.T) {
		shell(t, "printf shared-retained > /workspace/owned-by-project")
		child, err := store.Session(runctx, scope, "alpha")
		must(t, err)
		stop()
		<-done
		must(t, store.DeleteSession(runctx, scope, "alpha", child.Revision))
		agents.Close()
		terminals.Close()
		must(t, store.Close())
		store, err = producttest.Open(runctx, dbPath)
		must(t, err)
		start()

		await(t, "Session cleanup", func() bool {
			child, e := store.Session(runctx, scope, "alpha")
			return e == nil && child.Deletion == product.Settled
		})
		got := shell(t, "test ! -e /runtime/sessions/alpha && cat /workspace/owned-by-project")
		if got != "shared-retained" {
			t.Fatal(got)
		}
		turn(t, "beta", "after-child-delete", "beta after sibling deletion")
		if _, err = os.Stat(filepath.Join(directory, "captures", scope.ProjectID, "alpha")); !os.IsNotExist(err) {
			t.Fatal("Session archive retained", err)
		}
	})
	t.Run("failed-journal-recovery-is-local-and-retryable", func(t *testing.T) {
		stop()
		<-done
		var incarnation string
		must(t, reader.QueryRow(`SELECT id FROM agent_incarnations WHERE session_id='fault' AND state='running'`).Scan(&incarnation))
		shell(t, "/tmp/runtime-fixture kill-worker fault")
		await(t, "fault worker ended", func() bool {
			var state string
			e := reader.QueryRow(`SELECT state FROM agent_incarnations WHERE id=?`, incarnation).Scan(&state)
			return e == nil && state == "exited"
		})
		shell(t, "/tmp/runtime-fixture corrupt-journal /runtime fault "+incarnation)
		agents.Close()
		terminals.Close()
		must(t, store.Close())
		store, err = producttest.Open(runctx, dbPath)
		must(t, err)
		start()
		await(t, "explicit failed journal recovery", func() bool {
			var state string
			e := reader.QueryRow(`SELECT recovery_state FROM agent_incarnations WHERE id=?`, incarnation).Scan(&state)
			return e == nil && state == "failed"
		})
		blockers, err := store.Blockers(runctx, scope)
		must(t, err)
		for _, blocker := range blockers {
			if blocker.ID == "recovery/"+incarnation {
				t.Fatal("failed host recovery pinned the Project", blocker)
			}
		}
		turn(t, "beta", "during-recovery-failure", "sibling remains independent of journal failure")
		shell(t, "/tmp/runtime-fixture restore-journal /runtime fault "+incarnation)
		child, err := store.Session(runctx, scope, "fault")
		must(t, err)
		must(t, store.RetryRecovery(runctx, scope, "fault", child.Revision))
		await(t, "repaired journal replay", func() bool {
			var state string
			e := reader.QueryRow(`SELECT recovery_state FROM agent_incarnations WHERE id=?`, incarnation).Scan(&state)
			return e == nil && state == "complete"
		})
		turn(t, "fault", "after-recovery-retry", "continue after original journal was restored")
	})
	t.Run("guardian-loss-does-not-pretend-descendants-are-settled", func(t *testing.T) {
		var ref sandbox.ProcessReference
		must(t, reader.QueryRow(`SELECT store_id,sandbox_id,epoch,handle FROM agent_incarnations WHERE session_id='fault' AND state='running'`).Scan(&ref.StoreID, &ref.SandboxID, &ref.Epoch, &ref.Handle))
		must(t, backend.SignalProcess(runctx, ref, 9))
		await(t, "unknown guardian cleanup", func() bool {
			var cleanup, recovery string
			e := reader.QueryRow(`SELECT cleanup_state,recovery_state FROM agent_incarnations WHERE handle=?`, ref.Handle).Scan(&cleanup, &recovery)
			return e == nil && cleanup == "unknown" && recovery == "complete"
		})
		child, err := store.Session(runctx, scope, "fault")
		must(t, err)
		if child.State != "blocked" {
			t.Fatal(child)
		}
		if err = store.ContinueSession(runctx, scope, "fault", child.Revision); err != product.ErrBusy {
			t.Fatal("unknown descendants permitted replacement", err)
		}
		turn(t, "beta", "after-guardian-loss", "sibling continues despite unknown cleanup")
	})
	t.Run("forced-cold-restart-fences-terminal-and-retains-native-history", func(t *testing.T) {
		p, err := store.Project(runctx, scope)
		must(t, err)
		if err = lifecycle.Restart(runctx, scope, p.Revision); err != product.ErrBusy {
			t.Fatal("ordinary restart accepted live PTY", err)
		}
		must(t, lifecycle.ForceRestart(runctx, scope, p.Revision))
		await(t, "cold restart", func() bool {
			p, e := store.Project(runctx, scope)
			r, e2 := store.Resources(runctx, scope)
			return e == nil && e2 == nil && p.State == "active" && r.Reference.Epoch > original.Reference.Epoch
		})
		terminal, err := store.Terminal(runctx, scope, "kept")
		must(t, err)
		if terminal.State != "lost" || terminal.ExitCode != nil {
			t.Fatal(terminal)
		}
		turn(t, "beta", "after-cold-restart", "beta retains its native conversation")
		if got := shell(t, "cat /workspace/owned-by-project"); got != "shared-retained" {
			t.Fatal(got)
		}
	})
	t.Run("engine-crash-reconciles-placement-before-new-work", func(t *testing.T) {
		_, err := store.CreateTerminal(runctx, scope, "engine-kept", "Engine restart", 24, 80)
		must(t, err)
		var engineTerminal product.TerminalIncarnation
		await(t, "pre-crash terminal", func() bool {
			var e error
			engineTerminal, e = terminals.Start(runctx, scope, "engine-kept", 24, 80)
			return e == nil && engineTerminal.State == "running"
		})
		before, err := store.Resources(runctx, scope)
		must(t, err)
		priorPTY, err := backend.InspectProcess(runctx, engineTerminal.Reference)
		must(t, err)
		pid, err := strconv.Atoi(os.Getenv("PROJECT_TEST_ENGINE_PID"))
		must(t, err)
		engine, err := os.FindProcess(pid)
		must(t, err)
		must(t, engine.Signal(syscall.SIGKILL))
		base := os.Getenv("PROJECT_TEST_BASE")
		binary := os.Getenv("PROJECT_TEST_ENGINE_BINARY")
		if base == "" || binary == "" {
			t.Fatal("isolated engine restart configuration missing")
		}
		logfile, err := os.Create(filepath.Join(os.Getenv("PROJECT_TEST_BIN_DIR"), "S09-engine-restart.log"))
		must(t, err)
		replacement := exec.CommandContext(runctx, binary, "--addr", "127.0.0.1:18191", "--templates", filepath.Join(base, "templates"), "--state", filepath.Join(base, "fs/state"), "--store", filepath.Join(base, "fs/engine.db"), "--volumes-dir", filepath.Join(base, "fs/volumes"), "--manual-cold-recovery", "--node-memory-byte", "4294967296", "--node-disk-byte", "6442450944", "--reaper-interval", "30s")
		replacement.Stdout = logfile
		replacement.Stderr = logfile
		must(t, replacement.Start())
		restartedEngine = replacement
		// This replacement remains the engine for the remaining deletion scenario.
		t.Cleanup(func() { logfile.Close() })
		await(t, "replacement engine ready", func() bool { return api.Ready(runctx) == nil })
		observed, err := backend.Inspect(runctx, before.Reference)
		must(t, err)
		t.Logf("engine restart observation: state=%s placement=%s epoch=%d (before %d)", observed.State, observed.PlacementState, observed.Epoch, before.Reference.Epoch)
		if observed.State == "running" && observed.PlacementState == "active" && observed.Epoch == before.Reference.Epoch {
			await(t, "retained PTY reattachment", func() bool {
				i, e := terminals.Start(runctx, scope, "engine-kept", 24, 80)
				return e == nil && i.State == "running" && i.ID == engineTerminal.ID && i.Owner > engineTerminal.Owner
			})
			currentPTY, err := backend.InspectProcess(runctx, engineTerminal.Reference)
			must(t, err)
			if currentPTY.PID != priorPTY.PID || currentPTY.PID == 0 {
				t.Fatal("retained handle changed native process", priorPTY, currentPTY)
			}
			view, err := terminals.Attach(runctx, scope, "engine-kept", true)
			must(t, err)
			defer view.Close()
			must(t, terminals.Input(runctx, view, []byte("printf 'ENGINE-LIVE\\n'\r")))
			var output strings.Builder
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
		read:
			for {
				select {
				case frame, ok := <-view.Frames():
					if !ok {
						t.Fatal("retained terminal detached")
					}
					output.Write(frame.Data)
					if strings.Contains(output.String(), "\rENGINE-LIVE\r\n") {
						break read
					}
				case <-timer.C:
					t.Fatal("retained terminal not interactive", output.String())
				}
			}
		} else {
			await(t, "lost PTY reconciled", func() bool {
				terminal, e := store.Terminal(runctx, scope, "engine-kept")
				return e == nil && terminal.State == "lost" && terminal.ExitCode == nil
			})
		}
		turn(t, "beta", "after-engine-crash", "recover native history after engine crash")
		after, err := store.Resources(runctx, scope)
		must(t, err)
		if after.Reference.StoreID != before.Reference.StoreID || after.Reference.SandboxID != before.Reference.SandboxID || after.Reference.Epoch < before.Reference.Epoch {
			t.Fatal("engine recovery changed resource ownership", before, after)
		}
		if observed.Epoch == before.Reference.Epoch && observed.State == "running" && after.Reference.Epoch != before.Reference.Epoch {
			t.Fatal("transport restart unnecessarily replaced live placement")
		}

	})
	t.Run("Project-deletion-reclaims-all-owned-storage-and-artifacts", func(t *testing.T) {
		share, token, err := shares.Create(runctx, scope, "beta", []string{"owned-by-project"}, time.Now().Add(time.Hour))
		must(t, err)
		_, err = store.CreateSchedule(runctx, scope, product.Schedule{TriggerKind: "cron", ID: "future", Title: "Future", Prompt: "never after deletion", Cron: "* * * * *", Timezone: "UTC", State: "active"})
		must(t, err)
		r, err := store.Resources(runctx, scope)
		must(t, err)
		p, err := store.Project(runctx, scope)
		must(t, err)
		must(t, store.DeleteProject(runctx, scope, p.Revision))
		select {
		case <-fixture.destroyed:
		case <-time.After(15 * time.Second):
			t.Fatal("sandbox destroy effect not observed")
		}
		shutdown()
		store, err = producttest.Open(runctx, dbPath)
		must(t, err)
		retained, err := store.Resources(runctx, scope)
		must(t, err)
		if !retained.Occupied {
			t.Fatal("lost reply released capacity before settlement")
		}
		start()

		await(t, "Project deletion", func() bool { p, e := store.Project(runctx, scope); return e == nil && p.State == "deleted" })
		if _, err = backend.Inspect(runctx, r.Reference); !sandbox.Is(err, sandbox.NotFound) {
			t.Fatal("sandbox retained", err)
		}
		volumes, err := api.Volumes(runctx)
		must(t, err)
		for _, volume := range volumes {
			if volume.VolumeID == r.VolumeForRole("workspace") || volume.VolumeID == r.VolumeForRole("runtime") {
				t.Fatal("owned volume still listed after completed deletion", volume.VolumeID)
			}
		}

		sharedBytes, err := api.ReadVolume(runctx, sharedVolume, "sentinel", 0, 64)
		must(t, err)
		if string(sharedBytes) != "published-shared-asset" {
			t.Fatal("Project deletion altered shared publication", string(sharedBytes))
		}
		if _, err = store.PublicShare(runctx, token); err != product.ErrNotFound {
			t.Fatal(err)
		}
		for _, path := range []string{filepath.Join(directory, "shares", share.ID), filepath.Join(directory, "captures", scope.ProjectID)} {
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("owned host artifacts retained", path, err)
			}
		}
		var operations int
		must(t, reader.QueryRow(`SELECT count(*) FROM project_operations WHERE project_id=? AND kind='delete'`, scope.ProjectID).Scan(&operations))
		if operations != 1 {
			t.Fatal("deletion replaced its original operation", operations)
		}
		var count int
		must(t, reader.QueryRow(`SELECT (SELECT count(*) FROM messages)+(SELECT count(*) FROM turns)+(SELECT count(*) FROM agent_records)+(SELECT count(*) FROM schedules)`).Scan(&count))
		if count != 0 {
			t.Fatal("private history remains", count)
		}
	})
}
