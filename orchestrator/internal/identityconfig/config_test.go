package identityconfig

import (
	"strings"
	"testing"
)

func TestRegisteredClientValidationAndRotation(t *testing.T) {
	c := &Config{Issuer: "https://example.com", Listen: "127.0.0.1:8083", Clients: []Client{{ID: "studio", Callback: "https://example.com/volumes-studio/auth/callback", SecretEnvs: []string{"IDENTITY_CURRENT", "IDENTITY_PREVIOUS"}}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	current := strings.Repeat("current", 8)
	previous := strings.Repeat("previous", 8)
	t.Setenv("IDENTITY_CURRENT", current)
	t.Setenv("IDENTITY_PREVIOUS", previous)
	if err := c.CheckCredentials(); err != nil {
		t.Fatal(err)
	}
	client, _ := c.Client("studio")
	if !client.Authenticate(current) || !client.Authenticate(previous) || client.Authenticate("wrong") {
		t.Fatal("credential rotation")
	}
	t.Setenv("IDENTITY_PREVIOUS", "")
	if client.Authenticate(previous) {
		t.Fatal("revoked credential accepted")
	}
	if err := c.CheckCredentials(); err == nil {
		t.Fatal("missing configured secret accepted")
	}
	for _, callback := range []string{"https://evil.example/volumes-studio/auth/callback", "https://example.com/volumes-studio/auth/callback?x=1", "https://example.com/volumes-studio/auth/callback/", "http://example.com/volumes-studio/auth/callback", "https://user@example.com/volumes-studio/auth/callback"} {
		c.Clients[0].Callback = callback
		if err := c.Validate(); err == nil {
			t.Fatal("unsafe callback", callback)
		}
	}
}

func TestEvolutionSpaceCallbackIsServiceSpecific(t *testing.T) {
	c := &Config{Issuer: "https://example.com", Listen: "127.0.0.1:8083", Clients: []Client{{ID: "evolution-space", Callback: "https://example.com/evolution-space/auth/callback", SecretEnvs: []string{"EVOLUTION_SPACE_IDENTITY_SECRET"}}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Clients[0].Callback = "https://example.com/volumes-studio/auth/callback"
	if err := c.Validate(); err == nil {
		t.Fatal("Evolution Space accepted Studio callback")
	}
	c.Clients[0].ID = "studio"
	c.Clients[0].Callback = "https://example.com/evolution-space/auth/callback"
	if err := c.Validate(); err == nil {
		t.Fatal("Studio accepted Evolution Space callback")
	}
}

func TestUnknownApplicationCannotBorrowStudioCallback(t *testing.T) {
	c := &Config{Issuer: "https://example.com", Listen: "127.0.0.1:8083", Clients: []Client{{ID: "unknown", Callback: "https://example.com/volumes-studio/auth/callback", SecretEnvs: []string{"IDENTITY_SECRET"}}}}
	if err := c.Validate(); err == nil {
		t.Fatal("unknown client treated as Studio")
	}
}

func TestRetiredSpaceIdentityAndCallbackAreRejected(t *testing.T) {
	for _, client := range []Client{
		{ID: "evaluation-space", Callback: "https://example.com/evolution-space/auth/callback", SecretEnvs: []string{"IDENTITY_SECRET"}},
		{ID: "evolution-space", Callback: "https://example.com/evaluation-space/auth/callback", SecretEnvs: []string{"IDENTITY_SECRET"}},
	} {
		c := &Config{Issuer: "https://example.com", Listen: "127.0.0.1:8083", Clients: []Client{client}}
		if err := c.Validate(); err == nil {
			t.Fatal("retired application configuration accepted", client.ID, client.Callback)
		}
	}
}
