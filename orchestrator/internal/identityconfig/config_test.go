package identityconfig

import (
	"strings"
	"testing"
)

func TestRegisteredClientValidationAndRotation(t *testing.T) {
	c := &Config{Issuer: "https://example.com", Clients: []Client{{ID: "studio", Callback: "https://example.com/volumes-studio/auth/callback", SecretEnvs: []string{"IDENTITY_CURRENT", "IDENTITY_PREVIOUS"}}}}
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
