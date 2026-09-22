package producthttp

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/product"
)

func TestSharedReaderIdleHeartbeatAndRevocation(t *testing.T) {
	h := newHarness(t)
	scope := product.Scope{Actor: "alice", ProjectID: "p"}
	_, err := h.store.CreateProject(ctx, "alice", "p", "Project")
	must(t, err)
	_, err = h.store.CreateSession(ctx, scope, "s", "Session")
	must(t, err)
	must(t, h.store.GrantConversation(ctx, scope, "s", "bob"))
	h.setActor("bob")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(h.server.URL + "/api/conversations/s/events")
	must(t, err)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	frame := func() string {
		t.Helper()
		var b strings.Builder
		for {
			line, e := reader.ReadString('\n')
			must(t, e)
			if line == "\n" {
				return b.String()
			}
			b.WriteString(line)
		}
	}
	if initial := frame(); !strings.Contains(initial, "event: conversation") {
		t.Fatal(initial)
	}
	// Metadata/control revisions do not send unchanged shared message bodies.
	child, err := h.store.Session(ctx, scope, "s")
	must(t, err)
	must(t, h.store.RenameSession(ctx, scope, "s", child.Revision, "New title"))
	if idle := frame(); idle != ": heartbeat\n" {
		t.Fatal("idle shared reader refetched content", idle)
	}
	must(t, h.store.RevokeConversation(ctx, scope, "s", "bob"))
	// Revocation can be observed by the periodic authority check before another
	// frame. In either case no usable stream or message read remains.
	response.Body.Close()
	response, err = client.Get(h.server.URL + "/api/conversations/s/events")
	must(t, err)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal(response.StatusCode)
	}
}
