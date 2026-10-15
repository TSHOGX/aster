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

func (c *Client) ResolveEvolutionParticipant(ctx context.Context, group, operation string, revision int64, digest, action string) (evolution.Principal, error) {
	var principal evolution.Principal
	body := struct {
		Scope struct {
			GroupID     string `json:"group_id"`
			OperationID string `json:"operation_id"`
			Revision    int64  `json:"revision"`
			Digest      string `json:"digest"`
		} `json:"scope"`
		Action string `json:"action"`
	}{Action: action}
	body.Scope.GroupID = group
	body.Scope.OperationID = operation
	body.Scope.Revision = revision
	body.Scope.Digest = digest
	raw, err := json.Marshal(body)
	if err != nil {
		return principal, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", c.config.Endpoint+"/api/identity/v1/studio-participant-authority", bytes.NewReader(raw))
	if err != nil {
		return principal, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	response, err := c.Do(request)
	if err != nil {
		return principal, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return principal, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return principal, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return principal, ErrUnavailable
	}
	type actor struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
	}
	resolved, err := evolution.Decode[actor](bytes.NewReader(raw))
	if err != nil || resolved.Issuer != c.config.Issuer || !manifest.ValidID(resolved.Subject) {
		return principal, ErrUnavailable
	}
	return evolution.Principal{Issuer: resolved.Issuer, Subject: resolved.Subject, Kind: "human"}, nil
}
