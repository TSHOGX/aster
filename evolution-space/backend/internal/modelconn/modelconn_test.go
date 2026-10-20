package modelconn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const seal = "a-host-secret-of-more-than-thirty-two-characters"

func open(t *testing.T, dir, secret string) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(dir, "modelconn.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func input(base string, key *string) Input {
	return Input{Name: "Team GLM", Enabled: true, Settings: Settings{BaseURL: base, Model: "glm-5.2", MaxOutputTokens: 4000, JSONMode: true}, Policy: Policy{CallTokens: 8000, TimeoutSeconds: 30, Concurrency: 2}, Key: key}
}
func ptr(s string) *string { return &s }

func TestRevisionsPinSettingsKeysAreEncryptedAndRotateInPlace(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir, seal)
	if _, err := s.Save(ctx, "admin", "team-glm", input("http://example.com/v1", ptr("sk-secret-value-1"))); !errors.Is(err, ErrInvalid) {
		t.Fatal("plain HTTP to a remote host accepted", err)
	}
	if _, err := s.Save(ctx, "admin", "team-glm", input("https://models.example.com/v1/", nil)); !errors.Is(err, ErrInvalid) {
		t.Fatal("new connection without a key accepted", err)
	}
	first, err := s.Save(ctx, "admin", "team-glm", input("https://models.example.com/v1/", ptr("sk-secret-value-1")))
	if err != nil || first.Current.Revision != 1 || !first.KeySet || first.KeyHint != "…ue-1" || first.Settings.BaseURL != "https://models.example.com/v1" {
		t.Fatal("create", first, err)
	}
	renamed := input("https://models.example.com/v1", nil)
	renamed.ExpectedRevision, renamed.Name = 1, "Renamed"
	renamed.Policy.CallTokens = 9000
	same, err := s.Save(ctx, "admin", "team-glm", renamed)
	if err != nil || same.Current != first.Current || same.Name != "Renamed" || same.Policy.CallTokens != 9000 {
		t.Fatal("name and policy changes must not re-pin", same, err)
	}
	if _, err = s.Save(ctx, "admin", "team-glm", renamed); !errors.Is(err, ErrNotFound) && err != nil {
		t.Fatal(err)
	}
	changed := renamed
	changed.Settings.Model = "glm-5.3"
	changed.Key = ptr("sk-secret-value-2")
	second, err := s.Save(ctx, "admin", "team-glm", changed)
	if err != nil || second.Current.Revision != 2 || second.Current.Digest == first.Current.Digest {
		t.Fatal("model change must publish a new revision", second, err)
	}
	if _, err = s.Save(ctx, "admin", "team-glm", changed); !errors.Is(err, ErrConflict) {
		t.Fatal("stale expected revision accepted", err)
	}
	// The first revision still resolves with its own model and the rotated key.
	_, settings, key, err := s.Resolve(ctx, first.Current)
	if err != nil || settings.Model != "glm-5.2" || key != "sk-secret-value-2" {
		t.Fatal("pinned revision", settings, key, err)
	}
	refs, _ := s.Revisions(ctx)
	if len(refs) != 2 {
		t.Fatal(refs)
	}
	s.Close()
	raw, _ := os.ReadFile(filepath.Join(dir, "modelconn.db"))
	wal, _ := os.ReadFile(filepath.Join(dir, "modelconn.db-wal"))
	if strings.Contains(string(raw)+string(wal), "sk-secret-value") {
		t.Fatal("plaintext key stored")
	}
	other := open(t, dir, "a-different-host-secret-of-thirty-two-characters")
	if _, _, _, err = other.Resolve(ctx, second.Current); err == nil {
		t.Fatal("key decrypted with another host secret")
	}
	other.Close()
	plain := open(t, dir, "")
	if _, err = plain.Save(ctx, "admin", "other", input("https://models.example.com/v1", ptr("sk-new"))); !errors.Is(err, ErrNoSealKey) {
		t.Fatal("key stored without host secret", err)
	}
	plain.Close()
}

func TestConnectionTestReportsModelsAndRejectedKeys(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.2"},{"id":"glm-5.3"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","model":"glm-5.2","choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"total_tokens":20}}`))
	}))
	defer server.Close()
	client := NewClient()
	settings := Settings{BaseURL: server.URL + "/v1", Model: "glm-5.2", MaxOutputTokens: 512, JSONMode: true}
	result := client.Test(ctx, settings, "sk-test-key")
	if !result.OK || len(result.Models) != 2 || result.ResponseModel != "glm-5.2" {
		t.Fatal("test", result)
	}
	if bad := client.Test(ctx, settings, "wrong"); bad.OK || bad.Status != 401 || strings.Contains(bad.Error, "wrong") {
		t.Fatal("rejected key", bad)
	}
}
