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
	response, err := c.http.Do(request)
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
