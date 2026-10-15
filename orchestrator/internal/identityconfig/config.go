// Package identityconfig defines the sole account authority's registered clients.
// Configuration contains credential references, never credential values.
package identityconfig

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
)

type Client struct {
	ID         string   `json:"id"`
	Callback   string   `json:"callback"`
	SecretEnvs []string `json:"secret_envs"`
}
type Config struct {
	Issuer  string   `json:"issuer"`
	Listen  string   `json:"listen"`
	Clients []Client `json:"clients"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var environment = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("identity.listen must be a private loopback IP listener")
	}
	issuer, err := url.Parse(c.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Hostname() == "" || issuer.User != nil || issuer.Path != "" || issuer.RawQuery != "" || issuer.ForceQuery || issuer.Fragment != "" {
		return fmt.Errorf("identity issuer must be a canonical HTTPS origin")
	}
	if len(c.Clients) == 0 || len(c.Clients) > 16 {
		return fmt.Errorf("identity requires 1 to 16 registered clients")
	}
	seen := map[string]bool{}
	for _, client := range c.Clients {
		if !identifier.MatchString(client.ID) || seen[client.ID] {
			return fmt.Errorf("invalid or duplicate identity client")
		}
		seen[client.ID] = true
		var expectedCallback string
		switch client.ID {
		case "studio":
			expectedCallback = "/volumes-studio/auth/callback"
		case "evaluation-space":
			expectedCallback = "/evaluation-space/auth/callback"
		default:
			return fmt.Errorf("unsupported identity application %s", client.ID)
		}
		callback, err := url.Parse(client.Callback)
		if err != nil || callback.Scheme != "https" || callback.Host != issuer.Host || callback.User != nil || callback.Path != expectedCallback || callback.RawPath != "" || callback.RawQuery != "" || callback.ForceQuery || callback.Fragment != "" {
			return fmt.Errorf("identity callback must be the exact registered service callback on the issuer origin")
		}
		if len(client.SecretEnvs) == 0 || len(client.SecretEnvs) > 2 {
			return fmt.Errorf("identity client requires one current and optionally one rotating credential reference")
		}
		for _, name := range client.SecretEnvs {
			if !environment.MatchString(name) {
				return fmt.Errorf("invalid identity credential reference")
			}
		}
	}
	return nil
}
func (c *Config) Client(id string) (Client, bool) {
	if c != nil {
		for _, client := range c.Clients {
			if client.ID == id {
				return client, true
			}
		}
	}
	return Client{}, false
}
func (c Client) Authenticate(secret string) bool {
	if len(secret) < 32 || len(secret) > 1024 {
		return false
	}
	provided := sha256.Sum256([]byte(secret))
	accepted := 0
	for _, name := range c.SecretEnvs {
		expected := os.Getenv(name)
		hash := sha256.Sum256([]byte(expected))
		matched := subtle.ConstantTimeCompare(provided[:], hash[:])
		if len(expected) >= 32 && len(expected) <= 1024 {
			accepted |= matched
		}
	}
	return accepted == 1
}
func (c *Config) CheckCredentials() error {
	if c == nil {
		return nil
	}
	if err := c.Validate(); err != nil {
		return err
	}
	for _, client := range c.Clients {
		for _, name := range client.SecretEnvs {
			n := len(os.Getenv(name))
			if n < 32 || n > 1024 {
				return fmt.Errorf("identity client credential %s must contain 32 to 1024 bytes", name)
			}
		}
	}
	return nil
}
