package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

func TestSearchEndpointScopingAndContract(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := newAuthTestServer(t, dir, st, []config.AuthUser{
		{Username: "alice", PasswordHash: mustHash(t, "pw")},
		{Username: "bob", PasswordHash: mustHash(t, "pw")},
	})

	now := time.Now().UTC()
	for _, s := range []store.Session{
		{ID: "sess_alice", UserID: "alice", Status: string(sessionstate.Created), Agent: "pi", Mode: "agent", Workspace: dir, CreatedAt: now, UpdatedAt: now},
		{ID: "sess_bob", UserID: "bob", Status: string(sessionstate.Created), Agent: "pi", Mode: "agent", Workspace: dir, CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.CreateSession(ctx, s); err != nil {
			t.Fatalf("create session %q: %v", s.ID, err)
		}
	}
	if _, err := st.AddMessage(ctx, "sess_alice", "assistant", "the kubernetes deployment succeeded"); err != nil {
		t.Fatalf("add alice msg: %v", err)
	}
	if _, err := st.AddMessage(ctx, "sess_bob", "assistant", "the kubernetes rollout failed"); err != nil {
		t.Fatalf("add bob msg: %v", err)
	}

	withUser := func(r *http.Request, user string) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), userContextKey{}, user))
	}

	type searchResp struct {
		Results []struct {
			Kind      string `json:"kind"`
			SessionID string `json:"session_id"`
			Role      string `json:"role"`
			Snippet   string `json:"snippet"`
			MessageID *int64 `json:"message_id"`
		} `json:"results"`
		Truncated bool `json:"truncated"`
	}

	// Alice searches for "kubernetes" and must only see her own message.
	rec := httptest.NewRecorder()
	srv.search(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/search?q=kubernetes", nil), "alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("search: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp searchResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("alice should get exactly one hit, got %d: %s", len(resp.Results), rec.Body.String())
	}
	if resp.Results[0].SessionID != "sess_alice" {
		t.Fatalf("alice hit session = %q, want sess_alice", resp.Results[0].SessionID)
	}
	if resp.Results[0].Kind != "message" || resp.Results[0].MessageID == nil {
		t.Fatalf("expected a message hit with message_id, got %+v", resp.Results[0])
	}

	// Empty query returns an empty result set (not an error).
	rec = httptest.NewRecorder()
	srv.search(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/search?q=", nil), "alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty query: want 200 got %d", rec.Code)
	}
	resp = searchResp{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("empty query should return no results, got %d", len(resp.Results))
	}

	// Bad limit -> 400.
	rec = httptest.NewRecorder()
	srv.search(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/search?q=kubernetes&limit=abc", nil), "alice"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: want 400 got %d", rec.Code)
	}

	// limit=1 with a single matching message -> truncated true.
	rec = httptest.NewRecorder()
	srv.search(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/search?q=kubernetes&limit=1", nil), "alice"))
	resp = searchResp{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode limit: %v", err)
	}
	if len(resp.Results) != 1 || !resp.Truncated {
		t.Fatalf("want 1 result and truncated=true, got %d truncated=%v", len(resp.Results), resp.Truncated)
	}
}
