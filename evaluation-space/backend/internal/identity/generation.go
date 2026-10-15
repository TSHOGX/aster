package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"aster/contracts/evolution"
)

func (a *ProjectionAuthority) generationCall(ctx context.Context, action string, input any) ([]byte, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", a.config.Endpoint+"/api/identity/v1/generation/"+action, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 409 {
		return nil, ErrUnauthorized
	}
	if response.StatusCode == 204 {
		return nil, nil
	}
	if response.StatusCode != 200 {
		return nil, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return nil, ErrUnavailable
	}
	return raw, nil
}
func (a *ProjectionAuthority) AdmitGeneration(ctx context.Context, input evolution.GenerationAssignment) (evolution.GenerationGrant, error) {
	var grant evolution.GenerationGrant
	digest, err := input.Digest()
	if err != nil {
		return grant, err
	}
	raw, err := a.generationCall(ctx, "admit", input)
	if err != nil {
		return grant, err
	}
	grant, err = evolution.Decode[evolution.GenerationGrant](bytes.NewReader(raw))
	if err != nil || grant.Validate() != nil || grant.RunID != input.RunID || grant.GroupID != input.GroupID || grant.AssignmentDigest != digest {
		return evolution.GenerationGrant{}, ErrUnavailable
	}
	return grant, nil
}
func (a *ProjectionAuthority) ResolveGeneration(ctx context.Context, scope evolution.GenerationGrantScope) (evolution.GenerationAssignment, error) {
	var input evolution.GenerationAssignment
	raw, err := a.generationCall(ctx, "authority", scope)
	if err != nil {
		return input, err
	}
	input, err = evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
	if err != nil {
		return input, ErrUnavailable
	}
	digest, err := input.Digest()
	if err != nil || digest != scope.Grant.AssignmentDigest || input.RunID != scope.Grant.RunID || input.GroupID != scope.Grant.GroupID {
		return evolution.GenerationAssignment{}, ErrUnavailable
	}
	return input, nil
}
func (a *ProjectionAuthority) ObserveGeneration(ctx context.Context, input evolution.GenerationObservation) error {
	_, err := a.generationCall(ctx, "observe", input)
	return err
}
func (a *ProjectionAuthority) BindGenerationResult(ctx context.Context, grant evolution.GenerationGrant, scope evolution.RunResultScope) error {
	_, err := a.generationCall(ctx, "bind-result", struct {
		Grant evolution.GenerationGrant `json:"grant"`
		Scope evolution.RunResultScope  `json:"scope"`
	}{grant, scope})
	return err
}
func (a *ProjectionAuthority) SettleGeneration(ctx context.Context, grant evolution.GenerationGrant, fence, tokens, cost int64) error {
	_, err := a.generationCall(ctx, "settle", struct {
		Grant      evolution.GenerationGrant `json:"grant"`
		Fence      int64                     `json:"fence"`
		Tokens     int64                     `json:"tokens"`
		CostMicros int64                     `json:"cost_micros"`
	}{grant, fence, tokens, cost})
	return err
}

func (a *ProjectionAuthority) AuthorizeGenerationInput(ctx context.Context, artifact evolution.ArtifactRef) error {
	_, err := a.generationCall(ctx, "input-authority", artifact)
	return err
}
