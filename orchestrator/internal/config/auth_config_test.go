package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAuthUsersValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-users.yaml")
	content := `users:
  - username: lab
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
  - username: alice
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	users, err := LoadAuthUsersFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(users) != 2 || users[0].Username != "lab" || users[1].Username != "alice" {
		t.Fatalf("unexpected users: %+v", users)
	}
}

func TestLoadAuthUsersRoleDefaultsAndValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-users.yaml")
	content := `users:
  - username: lab
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
    role: admin
  - username: alice
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	users, err := LoadAuthUsersFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if users[0].Role != "admin" || users[1].Role != "" {
		t.Fatalf("roles=%+v", users)
	}
	if err := os.WriteFile(path, []byte(`users:
  - username: lab
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
    role: owner
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthUsersFile(path); err == nil {
		t.Fatal("invalid role should fail")
	}
}

func TestLoadAuthUsersMissingFileIsOpen(t *testing.T) {
	users, err := LoadAuthUsersFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("expected no users, got %d", len(users))
	}
}

func TestLoadAuthUsersRejectsBadUsername(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-users.yaml")
	content := `users:
  - username: "bad.name"
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadAuthUsersFile(path); err == nil {
		t.Fatalf("expected error for username with a dot")
	}
}

func TestLoadAuthUsersRejectsPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-users.yaml")
	content := `users:
  - username: lab
    password_hash: "hunter2"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadAuthUsersFile(path); err == nil {
		t.Fatalf("expected error for non-pbkdf2 password_hash")
	}
}

func TestLoadAuthUsersRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth-users.yaml")
	content := `users:
  - username: lab
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
  - username: lab
    password_hash: "pbkdf2-sha256$600000$c2FsdA$aGFzaA"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadAuthUsersFile(path); err == nil {
		t.Fatalf("expected error for duplicate username")
	}
}

func TestAuthEnabled(t *testing.T) {
	if (Config{}).AuthEnabled() {
		t.Fatalf("empty config should be open")
	}
	if !(Config{SharedSecret: "x"}).AuthEnabled() {
		t.Fatalf("shared secret should enable auth")
	}
	if !(Config{AuthUsers: []AuthUser{{Username: "lab"}}}).AuthEnabled() {
		t.Fatalf("users should enable auth")
	}
}
