package identity

import (
	"aster/evolution-space/internal/httpapi"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
)

func (a *ProjectionAuthority) ResolveEvaluation(ctx context.Context, scope evolution.EvaluationGrantScope) (evolution.EvaluationAssignment, error) {
	var assignment evolution.EvaluationAssignment
	raw, err := json.Marshal(scope)
	if err != nil {
		return assignment, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", a.config.Endpoint+"/api/identity/v1/evaluation-authority", bytes.NewReader(raw))
	if err != nil {
		return assignment, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	response, err := a.client.Do(request)
	if err != nil {
		return assignment, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return assignment, errors.Join(ErrUnauthorized, httpapi.ErrEvaluationDenied)
	}
	if response.StatusCode != 200 {
		return assignment, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return assignment, ErrUnavailable
	}
	assignment, err = evolution.Decode[evolution.EvaluationAssignment](bytes.NewReader(raw))
	if err != nil {
		return assignment, ErrUnavailable
	}
	digest, err := assignment.Digest()
	if err != nil || digest != scope.Grant.AssignmentDigest || assignment.JobID != scope.Grant.JobID || assignment.GroupID != scope.Grant.GroupID {
		return evolution.EvaluationAssignment{}, ErrUnavailable
	}
	subject, check, err := assignment.Subject()
	if err != nil {
		return assignment, ErrUnavailable
	}
	subjectDigest, _ := subject.Digest()
	if subjectDigest != scope.Grant.SubjectDigest || check.Service != scope.Grant.Service {
		return evolution.EvaluationAssignment{}, ErrUnavailable
	}
	return assignment, nil
}
func (a *ProjectionAuthority) SettleEvaluation(ctx context.Context, grant evolution.EvaluationGrant, completion projections.EvaluationCompletion) error {
	result, err := json.Marshal(completion.Result)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(struct {
		Grant        evolution.EvaluationGrant `json:"grant"`
		Tokens       int64                     `json:"tokens"`
		CostMicros   int64                     `json:"cost_micros"`
		ResultDigest string                    `json:"result_digest"`
	}{grant, completion.Usage.Tokens, completion.Usage.CostMicros, evolution.Digest(result)})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", a.config.Endpoint+"/api/identity/v1/evaluation-settlements", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return ErrUnauthorized
	}
	if response.StatusCode != 204 {
		return ErrUnavailable
	}
	return nil
}

func (a *ProjectionAuthority) AuthorizeEvaluationProfile(ctx context.Context, scope evolution.EvaluationProfileScope) error {
	raw, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", a.config.Endpoint+"/api/identity/v1/evaluation-profile-authority", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	response, err := a.client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return errors.Join(ErrUnauthorized, httpapi.ErrEvaluationDenied)
	}
	if response.StatusCode != 204 {
		return ErrUnavailable
	}
	return nil
}

func (a *ProjectionAuthority) AuthorizeRunResult(ctx context.Context, scope evolution.RunResultScope) error {
	if scope.Validate() != nil {
		return ErrUnauthorized
	}
	raw, _ := json.Marshal(scope)
	request, err := http.NewRequestWithContext(ctx, "POST", a.config.Endpoint+"/api/identity/v1/generation/result-authority", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.SetBasicAuth(a.config.ClientID, a.config.ClientSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 409 {
		return errors.Join(ErrUnauthorized, httpapi.ErrRunResultDenied)
	}
	if response.StatusCode != 204 {
		return ErrUnavailable
	}
	return nil
}
