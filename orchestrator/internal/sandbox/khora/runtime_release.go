package khora

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
)

// VerifyRuntime runs the template's narrow self-verifier before a Project grants
// child execution authority. The original per-placement verification key is
// reused on a lost reply; it performs no workspace mutation.
func (b *Backend) VerifyRuntime(ctx context.Context, ref sandbox.Reference, digest string) error {
	// An older verifier can legitimately accept its own older protocol. Check
	// the actual manifest against this host's contract before trusting its exit.
	observed, err := b.Inspect(ctx, ref)
	if err != nil {
		return err
	}
	if observed.Epoch != ref.Epoch || observed.State != "running" || observed.PlacementState != "active" {
		return failure("runtime manifest", sandbox.Precondition, fmt.Errorf("runtime placement changed"))
	}
	file, err := b.client.ReadFile(ctx, ref.SandboxID, runtimerelease.ManifestPath)
	if err != nil {
		return mapError("runtime manifest", err)
	}
	_, loadErr := runtimerelease.Load(file, digest)
	if err = errors.Join(loadErr, file.Close()); err != nil {
		return failure("runtime manifest", sandbox.Incompatible, err)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s", ref.SandboxID, ref.Epoch, digest)))
	handle := fmt.Sprintf("runtime-verify-%x", sum[:16])
	stream, err := b.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: sandbox.ProcessReference{StoreID: ref.StoreID, SandboxID: ref.SandboxID, Epoch: ref.Epoch, Handle: handle}, IdempotencyKey: handle, Command: "/usr/local/bin/runtime-verify", Arguments: []string{digest}, Directory: "/"})
	if err != nil {
		return err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	var detail strings.Builder
	for {
		frame, err := stream.Next()
		if err != nil {
			return err
		}
		if frame.Kind == "data" && detail.Len() < 2048 {
			detail.Write(frame.Data[:min(len(frame.Data), 2048-detail.Len())])
		}
		if frame.Kind == "end" {
			if frame.ExitCode != 0 {
				return fmt.Errorf("Project runtime verification failed (%d): %s", frame.ExitCode, detail.String())
			}
			return nil
		}
	}
}
