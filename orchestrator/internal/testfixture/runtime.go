// Package testfixture supplies synthetic current-protocol metadata to isolated
// tests. It contains no engine adapter and grants no application authority.
package testfixture

import (
	"harness-platform/orchestrator/internal/runtimerelease"
	"strings"
)

func RuntimeManifest() (runtimerelease.Manifest, string) {
	digest := "sha256:" + strings.Repeat("0", 64)
	m := runtimerelease.Manifest{Version: 4, ScheduleProtocol: runtimerelease.ScheduleProtocol, NativeProtocol: runtimerelease.NativeProtocol, ReaderProtocol: runtimerelease.ReaderProtocol, Platform: "linux/amd64", EngineModule: runtimerelease.EngineModule, EngineRevision: runtimerelease.EngineRevision, AgentProtocol: 2, Workspace: "/workspace", RuntimeState: "/runtime", PiPackage: runtimerelease.PiPackage, PiVersion: runtimerelease.PiVersion, PiIntegrity: runtimerelease.PiIntegrity, NodeVersion: runtimerelease.NodeVersion, NativeTree: digest, Files: map[string]string{}}
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
