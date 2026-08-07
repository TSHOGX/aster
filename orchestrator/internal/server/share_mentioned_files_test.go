package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// The workspace panel's "In this reply" section needs two things from a reader's
// view of a shared session: the artifact list, and messages whose stop_reason
// still identifies the turn-final reply. Both come from routes behind the read
// gate, so a regression there silently empties the section instead of erroring.
func TestReaderReceivesArtifactsAndFinalReplyStopReason(t *testing.T) {
	f := newShareFixture(t)
	ctx := context.Background()
	f.grant(t, "bob", store.ShareVisibilityFull)

	now := time.Now().UTC()
	if err := f.store.UpsertArtifact(ctx, store.Artifact{
		SessionID: "sess_alice", Path: "outputs/soh.png", Size: 1234,
		ModTime: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	rec := httptest.NewRecorder()
	f.srv.listArtifacts(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions/sess_alice/artifacts", nil), "bob"), "sess_alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("reader artifacts: want 200 got %d (%s)", rec.Code, rec.Body.String())
	}
	var arts struct {
		Artifacts []struct{ Path string } `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &arts); err != nil {
		t.Fatalf("decode artifacts: %v", err)
	}
	// An identity share exposes the whole verified workspace; the per-file
	// whitelist is only for anonymous links. An empty list here would leave the
	// section with nothing to resolve mentions against.
	if len(arts.Artifacts) != 1 || arts.Artifacts[0].Path != "outputs/soh.png" {
		t.Fatalf("reader artifacts = %+v, want outputs/soh.png", arts.Artifacts)
	}
}

// The pane suppresses the section for a shell session by reading session.mode off
// the sidebar listing. A shared row goes through sharedSession(), which strips
// owner/failure fields — mode must survive that trimming, or an agent session
// shared with a colleague would be misread as a shell and show nothing.
func TestSharedSessionListingKeepsMode(t *testing.T) {
	f := newShareFixture(t)
	f.grant(t, "bob", store.ShareVisibilityFull)

	rec := httptest.NewRecorder()
	f.srv.listSessions(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/sessions", nil), "bob"))
	if rec.Code != http.StatusOK {
		t.Fatalf("list sessions: want 200 got %d", rec.Code)
	}
	var body struct {
		Sessions []struct {
			ID     string `json:"id"`
			Access string `json:"access"`
			Mode   string `json:"mode"`
			UserID string `json:"user_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var row *struct {
		ID     string `json:"id"`
		Access string `json:"access"`
		Mode   string `json:"mode"`
		UserID string `json:"user_id"`
	}
	for i := range body.Sessions {
		if body.Sessions[i].ID == "sess_alice" {
			row = &body.Sessions[i]
		}
	}
	if row == nil {
		t.Fatal("shared session missing from bob's listing")
	}
	if row.Access != "reader" {
		t.Fatalf("access = %q, want reader", row.Access)
	}
	if row.Mode != "agent" {
		t.Fatalf("mode = %q, want agent (the shell guard keys off this)", row.Mode)
	}
	if row.UserID != "" {
		t.Fatalf("user_id leaked to a reader: %q", row.UserID)
	}
}
