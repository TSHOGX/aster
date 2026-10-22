// Package appidentity implements the registered applications' central identity protocol.
// Applications own their session persistence and resource authorization.
package appidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Application struct{ Prefix, AccessEntitlement string }

func (a Application) SessionCookie() string {
	return "__Secure-" + strings.ReplaceAll(strings.Trim(a.Prefix, "/"), "-", "_") + "_session"
}
func (a Application) LoginCookie() string {
	return "__Secure-" + strings.ReplaceAll(strings.Trim(a.Prefix, "/"), "-", "_") + "_login"
}
func ApplicationFor(id string) (Application, bool) {
	switch id {
	case "studio":
		return Application{"/volumes-studio/", "volumes_studio.access"}, true
	case "evolution-space":
		return Application{"/evolution-space/", "evolution_space.access"}, true
	default:
		return Application{}, false
	}
}

var ErrUnavailable = errors.New("identity_unavailable")
var ErrUnauthorized = errors.New("identity_unauthorized")

type Config struct {
	Issuer       string
	Endpoint     string
	ClientID     string
	ClientSecret string
}
type Assertion struct {
	Issuer         string    `json:"issuer"`
	Subject        string    `json:"subject"`
	CentralSession string    `json:"central_session"`
	Epoch          int64     `json:"auth_epoch"`
	Entitlements   []string  `json:"entitlements"`
	ExpiresAt      time.Time `json:"expires_at"`
	Grant          string    `json:"grant,omitempty"`
}
type Client struct {
	config Config
	http   *http.Client
}

func NewClient(config Config) (*Client, error) {
	if _, ok := ApplicationFor(config.ClientID); !ok {
		return nil, fmt.Errorf("unknown identity application")
	}
	issuer, err := url.Parse(config.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Hostname() == "" || issuer.User != nil || issuer.Path != "" || issuer.RawQuery != "" || issuer.ForceQuery || issuer.Fragment != "" {
		return nil, fmt.Errorf("central issuer must be a canonical HTTPS origin")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, fmt.Errorf("identity endpoint must be an origin")
	}
	if endpoint.Scheme != "https" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote identity endpoint requires HTTPS")
		}
	}
	if len(config.ClientID) == 0 || len(config.ClientID) > 128 || len(config.ClientSecret) < 32 || len(config.ClientSecret) > 1024 {
		return nil, fmt.Errorf("registered client and 32–1024 byte credential required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: time.Minute, MaxIdleConnsPerHost: 8}
	return &Client{config: config, http: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, action string, body any) (Assertion, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return Assertion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint+"/api/identity/v1/"+action, bytes.NewReader(b))
	if err != nil {
		return Assertion{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	// Bound evidence against the time before the remote check. Transit consumes
	// validity; neither a delayed response nor an outage extends central evidence.
	started := time.Now()
	response, err := c.http.Do(req)
	if err != nil {
		return Assertion{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return Assertion{}, ErrUnauthorized
	}
	if action == "revoke" && response.StatusCode == http.StatusNoContent {
		return Assertion{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return Assertion{}, ErrUnavailable
	}
	b, err = io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return Assertion{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	var a Assertion
	if err = decoder.Decode(&a); err != nil || decoder.Decode(new(any)) != io.EOF {
		return Assertion{}, ErrUnavailable
	}
	if a.Issuer != c.config.Issuer || a.Subject == "" || len(a.Subject) > 128 || a.Epoch < 1 || !a.ExpiresAt.After(time.Now()) || len(a.Entitlements) > 32 {
		return Assertion{}, ErrUnauthorized
	}
	admitted := false
	for _, e := range a.Entitlements {
		app, _ := ApplicationFor(c.config.ClientID)
		if e == app.AccessEntitlement {
			admitted = true
		}
	}
	if !admitted {
		return Assertion{}, ErrUnauthorized
	}
	if a.CentralSession == "" {
		return Assertion{}, ErrUnauthorized
	}
	if action == "token" && len(a.Grant) != 43 {
		return Assertion{}, ErrUnauthorized
	}
	if a.ExpiresAt.After(started.Add(time.Minute)) {
		a.ExpiresAt = started.Add(time.Minute)
	}
	return a, nil
}
func (c *Client) Exchange(ctx context.Context, code, verifier string) (Assertion, error) {
	return c.call(ctx, "token", map[string]string{"code": code, "code_verifier": verifier, "redirect_uri": c.config.Issuer + c.Application().Prefix + "auth/callback"})
}
func (c *Client) Introspect(ctx context.Context, grant string) (Assertion, error) {
	return c.call(ctx, "introspect", map[string]string{"grant": grant})
}
func (c *Client) Revoke(ctx context.Context, grant string) error {
	_, err := c.call(ctx, "revoke", map[string]string{"grant": grant})
	return err
}
func (c *Client) AuthorizeURL(state, challenge string) string {
	return c.config.Issuer + "/api/identity/v1/authorize?" + url.Values{"client_id": {c.config.ClientID}, "redirect_uri": {c.config.Issuer + c.Application().Prefix + "auth/callback"}, "state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}.Encode()
}
func (c *Client) Application() Application                         { app, _ := ApplicationFor(c.config.ClientID); return app }
func (c *Client) Do(request *http.Request) (*http.Response, error) { return c.http.Do(request) }
func LocalReturn(prefix, value string) (string, error) {
	if prefix != "/volumes-studio/" && prefix != "/evolution-space/" {
		return "", ErrUnauthorized
	}
	if value == "" {
		return prefix, nil
	}
	if len(value) > 2048 || !strings.HasPrefix(value, prefix) || strings.ContainsAny(value, "\\\x00\r\n") {
		return "", ErrUnauthorized
	}
	u, err := url.Parse(value)
	if err != nil || u.Host != "" || u.Scheme != "" || u.Fragment != "" {
		return "", ErrUnauthorized
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return "", ErrUnauthorized
		}
	}
	if !strings.HasPrefix(u.Path, prefix) || strings.HasPrefix(u.Path, prefix+"auth/") {
		return "", ErrUnauthorized
	}
	return value, nil
}
