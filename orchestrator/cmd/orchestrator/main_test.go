package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/application"
	"harness-platform/orchestrator/internal/auth"
)

func TestCredentialCLIUsesAtomicRegistryAndNeverPrintsHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	var output bytes.Buffer
	args := []string{"user", "add", "--file", path, "--username", "alice", "--password-stdin"}
	if err := run(context.Background(), args, strings.NewReader("first-password\n"), &output); err != nil {
		t.Fatal(err)
	}
	users, err := application.ReadUsers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || !auth.VerifyPassword(users[0].PasswordHash, "first-password") {
		t.Fatal("credential not persisted")
	}
	if err = run(context.Background(), []string{"user", "list", "--file", path}, nil, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "pbkdf2") || !strings.Contains(output.String(), "alice") {
		t.Fatal("credential list exposed password material", output.String())
	}
	if err = run(context.Background(), []string{"user", "password", "--file", path, "--username", "alice", "--password-stdin"}, strings.NewReader("replacement-password\n"), &output); err != nil {
		t.Fatal(err)
	}
	next, err := application.ReadUsers(path)
	if err != nil {
		t.Fatal(err)
	}
	if next[0].PasswordHash == users[0].PasswordHash || !auth.VerifyPassword(next[0].PasswordHash, "replacement-password") {
		t.Fatal("password did not rotate")
	}
	if err = run(context.Background(), []string{"user", "remove", "--file", path, "--username", "alice"}, nil, &output); err != nil {
		t.Fatal(err)
	}
	empty, err := application.ReadUsers(path)
	if err != nil || len(empty) != 0 {
		t.Fatal("last-user removal must disable access, never enable open mode", empty, err)
	}
}
func TestEntryPointRefusesRetiredAndImplicitConfiguration(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"serve", "--deployment", "old.json"}, {"session"}, {"user", "add", "--file", filepath.Join(t.TempDir(), "users"), "--username", "alice", "--keep-sessions"}} {
		if err := run(context.Background(), args, strings.NewReader("password"), &bytes.Buffer{}); err == nil {
			t.Fatal("unsupported command accepted", args)
		}
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"version"}, nil, &output); err != nil || !strings.Contains(output.String(), "commit") {
		t.Fatal(output.String(), err)
	}
}
