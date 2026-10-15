package identity

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type RunAdmissionScope struct {
	Subject   string `json:"subject"`
	GroupID   string `json:"group_id"`
	BindingID string `json:"binding_id"`
	Revision  int64  `json:"revision"`
	Digest    string `json:"digest"`
	Automatic bool   `json:"automatic"`
}

var ErrRunAdmissionDenied = errors.New("run_admission_denied")
var ErrRunAuthorityUnavailable = errors.New("run_authority_unavailable")

// AuthorizeRunAdmission obtains fresh central admission authority. It must not be
// used to revoke already admitted runs merely because a newer config is active.
func (a *ProjectionAuthority) AuthorizeRunAdmission(ctx context.Context, scope RunAdmissionScope) error {
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", a.config.Endpoint+"/api/identity/v1/run-admission-authority", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	response, err := a.client.Do(request)
	if err != nil {
		return ErrRunAuthorityUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return ErrRunAdmissionDenied
	}
	if response.StatusCode != 200 {
		return ErrRunAuthorityUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return ErrRunAuthorityUnavailable
	}
	value, err := evolution.Decode[struct {
		Allowed bool `json:"allowed"`
	}](bytes.NewReader(raw))
	if err != nil {
		return ErrRunAuthorityUnavailable
	}
	if !value.Allowed {
		return ErrRunAdmissionDenied
	}
	return nil
}
