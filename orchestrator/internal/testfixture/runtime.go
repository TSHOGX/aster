// Package testfixture supplies synthetic current-protocol metadata to isolated
// tests. It contains no engine adapter and grants no application authority.
package testfixture

import (
	"context"
	"fmt"
	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox"
	"strings"
)

func RuntimeManifest() (runtimerelease.Manifest, string) {
	digest := "sha256:" + strings.Repeat("0", 64)
	mounts := runtimerelease.DefaultMountContract()
	mounts.RequiredPublications = []runtimerelease.RequiredPublication{}
	m := runtimerelease.Manifest{Version: runtimerelease.ManifestVersion, HistoryProtocol: runtimerelease.HistoryProtocol, Mounts: mounts, ScheduleProtocol: runtimerelease.ScheduleProtocol, NativeProtocol: runtimerelease.NativeProtocol, ReaderProtocol: runtimerelease.ReaderProtocol, Platform: "linux/amd64", EngineModule: runtimerelease.EngineModule, EngineRevision: runtimerelease.EngineRevision, AgentProtocol: agentwire.Version, Workspace: "/workspace", RuntimeState: "/runtime", PiPackage: runtimerelease.PiPackage, PiVersion: runtimerelease.PiVersion, PiIntegrity: runtimerelease.PiIntegrity, NodeVersion: runtimerelease.NodeVersion, NativeTree: digest, Files: map[string]string{}}
	for _, component := range runtimerelease.Components {
		m.Files[component] = digest
	}
	raw, err := runtimerelease.Bytes(m)
	if err != nil {
		panic(err)
	}
	identity, err := runtimerelease.Digest(raw)
	if err != nil {
		panic(err)
	}
	return m, identity
}

// VerifyRuntime represents the synthetic current-protocol image used by ordinary
// service tests. Privileged integration cases replace it with the real adapter.
func VerifyRuntime(ctx context.Context, reference sandbox.Reference, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reference.StoreID == "" || reference.SandboxID == "" || reference.Epoch == 0 || !runtimerelease.ValidDigest(digest) {
		return fmt.Errorf("invalid fixture runtime identity")
	}
	return nil
}
