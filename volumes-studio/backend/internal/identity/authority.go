package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"aster/contracts/evolution"
)

// Authorize obtains fresh scoped evidence over the registered private connection.
// Redirects, outages and malformed responses never become an allow decision.
func (c *Client) Authorize(ctx context.Context, access evolution.AccessRequest) (evolution.AccessDecision, error) {
	var decision evolution.AccessDecision
	if access.Principal.Issuer != c.config.Issuer {
		return decision, ErrUnauthorized
	}
	raw, err := json.Marshal(access)
	if err != nil {
		return decision, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint+"/api/identity/v1/studio-access", bytes.NewReader(raw))
	if err != nil {
		return decision, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	response, err := c.Do(req)
	if err != nil {
		return decision, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return decision, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return decision, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return decision, ErrUnavailable
	}
	decision, err = evolution.Decode[evolution.AccessDecision](bytes.NewReader(raw))
	if err != nil || decision.Allowed && decision.PolicyRevision < 1 {
		return evolution.AccessDecision{}, ErrUnavailable
	}
	return decision, nil
}
