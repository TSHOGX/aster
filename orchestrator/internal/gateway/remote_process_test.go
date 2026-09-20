package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A separate process gives the test genuine kernel lifetime evidence.
func TestGatewayControlProcessHelper(t *testing.T) {
	path := os.Getenv("HARNESS_TEST_GATEWAY_SOCKET")
	if path == "" {
		return
	}
	handler, err := ControlHandler(NewFlows(), ControlOptions{ID: "child", ApplicationUID: uint32(os.Getuid()), MaxOperationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var listener net.Listener
	if os.Getenv("HARNESS_TEST_GATEWAY_UNLOCKED") == "1" {
		listener, err = net.Listen("unix", path)
	} else {
		listener, err = ListenControl(path, os.Getgid())
	}
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ConnContext: PeerContext}
	if err := server.Serve(listener); err != nil {
		t.Fatal(err)
	}
}
func childGateway(t *testing.T, path string, unlocked ...bool) func() {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestGatewayControlProcessHelper$")
	command.Env = append(os.Environ(), "HARNESS_TEST_GATEWAY_SOCKET="+path)
	if len(unlocked) > 0 && unlocked[0] {
		command.Env = append(command.Env, "HARNESS_TEST_GATEWAY_UNLOCKED=1")
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { _ = command.Process.Kill(); _ = command.Wait() }) }
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		remote, err := NewRemote(ctx, RemoteConfig{ID: "child", Socket: path, PeerUID: uint32(os.Getuid()), Timeout: time.Second})
		cancel()
		if err == nil {
			remote.Close()
			return stop
		}
		if time.Now().After(deadline) {
			t.Fatalf("child gateway did not serve: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestGatewayReplacementRequiresPreviousKernelProcessExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	stopOld := childGateway(t, path)
	controls, err := NewRemotes(context.Background(), []RemoteConfig{{ID: "child", Socket: path, PeerUID: uint32(os.Getuid()), Timeout: time.Second}}, mapDirectory{"old": "child", "new": "child"})
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err := controls.Activate(context.Background(), "old", 1); err != nil {
		t.Fatal(err)
	}
	previous := controls.gateways["child"].remote
	instance := previous.Info().Instance
	// Replacing a socket path while the original process is alive is not teardown.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	childGateway(t, path, true)
	previous.transport.CloseIdleConnections()
	if err := controls.Revoke(context.Background(), "old"); !errors.Is(err, ErrGatewayRestarted) {
		t.Fatalf("live process replacement accepted: %v", err)
	}
	stopOld()
	if err := controls.Revoke(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if controls.gateways["child"].remote.Info().Instance == instance {
		t.Fatal("replacement instance was not pinned")
	}
	if err := controls.Activate(context.Background(), "old", 1); err == nil {
		t.Fatal("revoked binding revived after restart")
	}
	if err := controls.Activate(context.Background(), "new", 1); err != nil {
		t.Fatal(err)
	}
}

func TestControlSocketRecoversAfterCrashWithoutReplacingLiveOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	stop := childGateway(t, path)
	if listener, err := ListenControl(path, os.Getgid()); err == nil {
		listener.Close()
		t.Fatal("live control listener replaced")
	}
	stop()
	// SIGKILL leaves the socket inode but releases the kernel-held lock.
	childGateway(t, path)
	remote, err := NewRemote(context.Background(), RemoteConfig{ID: "child", Socket: path, PeerUID: uint32(os.Getuid()), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if err := remote.Activate(context.Background(), "new", 1); err != nil {
		t.Fatal(err)
	}
}
