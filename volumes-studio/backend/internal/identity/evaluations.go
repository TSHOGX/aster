package identity

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

func (c *Client) AuthorizeEvaluation(ctx context.Context, assignment evolution.EvaluationAssignment) (evolution.EvaluationGrant, error) {
	var grant evolution.EvaluationGrant
	digest, err := assignment.Digest()
	if err != nil {
		return grant, err
	}
	raw, _ := json.Marshal(assignment)
	request, err := http.NewRequestWithContext(ctx, "POST", c.config.Endpoint+"/api/identity/v1/evaluation-authorizations", bytes.NewReader(raw))
	if err != nil {
		return grant, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	response, err := c.Do(request)
	if err != nil {
		return grant, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return grant, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return grant, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(raw) > 16384 {
		return grant, ErrUnavailable
	}
	grant, err = evolution.Decode[evolution.EvaluationGrant](bytes.NewReader(raw))
	if err != nil || grant.Validate() != nil || grant.AssignmentDigest != digest || grant.JobID != assignment.JobID || grant.GroupID != assignment.GroupID {
		return evolution.EvaluationGrant{}, ErrUnavailable
	}
	subject, check, _ := assignment.Subject()
	subjectDigest, _ := subject.Digest()
	if grant.SubjectDigest != subjectDigest || grant.Service != check.Service {
		return evolution.EvaluationGrant{}, ErrUnavailable
	}
	return grant, nil
}

func (c *Client) ResolveEvaluationInput(ctx context.Context, grant evolution.EvaluationGrant) (evolution.EvaluationAssignment, error) {
	var assignment evolution.EvaluationAssignment
	raw, err := json.Marshal(evolution.EvaluationGrantScope{Grant: grant, Action: "input"})
	if err != nil {
		return assignment, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", c.config.Endpoint+"/api/identity/v1/evaluation-authority", bytes.NewReader(raw))
	if err != nil {
		return assignment, err
	}
	request.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.Do(request)
	if err != nil {
		return assignment, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return assignment, ErrUnauthorized
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
	if err != nil || digest != grant.AssignmentDigest || assignment.JobID != grant.JobID || assignment.GroupID != grant.GroupID {
		return evolution.EvaluationAssignment{}, ErrUnavailable
	}
	subject, check, err := assignment.Subject()
	if err != nil {
		return assignment, ErrUnavailable
	}
	subjectDigest, _ := subject.Digest()
	if subjectDigest != grant.SubjectDigest || check.Service != grant.Service {
		return evolution.EvaluationAssignment{}, ErrUnavailable
	}
	return assignment, nil
}

// AuthorizeRunResult checks the central terminal fence and bound patch digest;
// the incoming producer identity is independently authenticated by the listener.
func (c *Client) AuthorizeRunResult(ctx context.Context, scope evolution.RunResultScope) error {
	if scope.Validate() != nil {
		return ErrUnauthorized
	}
	raw, _ := json.Marshal(scope)
	request, err := http.NewRequestWithContext(ctx, "POST", c.config.Endpoint+"/api/identity/v1/generation/result-authority", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 409 {
		return ErrUnauthorized
	}
	if response.StatusCode != 204 {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) ResolveGenerationBase(ctx context.Context, scope evolution.GenerationGrantScope) (evolution.GenerationAssignment, error) {
	var assignment evolution.GenerationAssignment
	if scope.Action != "read" || scope.Grant.Validate() != nil {
		return assignment, ErrUnauthorized
	}
	raw, _ := json.Marshal(scope)
	request, err := http.NewRequestWithContext(ctx, "POST", c.config.Endpoint+"/api/identity/v1/generation/authority", bytes.NewReader(raw))
	if err != nil {
		return assignment, err
	}
	request.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.Do(request)
	if err != nil {
		return assignment, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 409 {
		return assignment, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return assignment, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return assignment, ErrUnavailable
	}
	assignment, err = evolution.Decode[evolution.GenerationAssignment](bytes.NewReader(raw))
	if err != nil {
		return assignment, ErrUnavailable
	}
	digest, err := assignment.Digest()
	if err != nil || digest != scope.Grant.AssignmentDigest || assignment.RunID != scope.Grant.RunID || assignment.GroupID != scope.Grant.GroupID {
		return evolution.GenerationAssignment{}, ErrUnavailable
	}
	return assignment, nil
}
