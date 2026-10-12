package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntryPointRefusesRetiredAndImplicitConfiguration(t *testing.T) {
	for _, args := range [][]string{{"release-identity"}, {"serve"}, {"serve", "--deployment", "old.json"}, {"session"}, {"user", "add", "--file", filepath.Join(t.TempDir(), "users"), "--username", "alice", "--keep-sessions"}} {
		if err := run(context.Background(), args, strings.NewReader("password"), &bytes.Buffer{}); err == nil {
			t.Fatal("unsupported command accepted", args)
		}
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"version"}, nil, &output); err != nil || !strings.Contains(output.String(), "commit") {
		t.Fatal(output.String(), err)
	}
}
