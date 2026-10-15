package identity

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

func (c *Client) ResolveEvolutionOutput(ctx context.Context, scope evolution.OutputCatalogScope) (evolution.Principal, error) {
	var result evolution.Principal
	if scope.Query.Validate() != nil || !manifest.ValidID(scope.Ticket) {
		return result, ErrUnauthorized
	}
	raw, _ := json.Marshal(scope)
	request, err := http.NewRequestWithContext(ctx, "POST", c.config.Endpoint+"/api/identity/v1/studio-output-reader", bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	request.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return result, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return result, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return result, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return result, ErrUnavailable
	}
	result, err = evolution.Decode[evolution.Principal](bytes.NewReader(raw))
	if err != nil || result.Issuer != c.config.Issuer || result.Kind != "human" || !manifest.ValidID(result.Subject) {
		return evolution.Principal{}, ErrUnavailable
	}
	return result, nil
}
