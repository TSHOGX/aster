package appidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"
)

// EvaluationAccessRequest uses the opaque application grant, never a caller-supplied
// subject. Empty GroupID lists current readable groups; an exact group checks Action.
type EvaluationAccessRequest struct {
	Grant   string `json:"grant"`
	GroupID string `json:"group_id,omitempty"`
	Action  string `json:"action"`
	Search  string `json:"search,omitempty"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

var groupIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (r EvaluationAccessRequest) ValidScope() bool {
	if r.GroupID != "" && !groupIDPattern.MatchString(r.GroupID) || r.After != "" && !groupIDPattern.MatchString(r.After) || len(r.Search) > 256 || r.Limit < 1 || r.Limit > 50 {
		return false
	}
	if r.GroupID == "" && r.Action != "config.read" || r.GroupID != "" && (r.Search != "" || r.After != "") {
		return false
	}
	switch r.Action {
	case "config.read", "config.manage", "run.start", "source.read", "workflow.edit":
		return true
	}
	return false
}

type EvaluationGroup struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	CanEditWorkflows bool   `json:"can_edit_workflows"`
	CanManage        bool   `json:"can_manage"`
	CanRun           bool   `json:"can_run"`
	CanReadEvidence  bool   `json:"can_read_evidence"`
	DesiredRevision  int64  `json:"desired_revision"`
	ActiveRevision   int64  `json:"active_revision"`
	AutoEnabled      bool   `json:"auto_enabled"`
	State            string `json:"state"`
}

type EvaluationAccess struct {
	Subject   string            `json:"subject"`
	ExpiresAt time.Time         `json:"expires_at"`
	Items     []EvaluationGroup `json:"items"`
	NextAfter string            `json:"next_after"`
	HasMore   bool              `json:"has_more"`
}

// EvaluationGroups never forwards browser credentials. Each call rechecks the
// exact application's current grant, membership and requested group permission.
func (c *Client) EvaluationGroups(ctx context.Context, session Session, input EvaluationAccessRequest) (EvaluationAccess, error) {
	if c.config.ClientID != "evaluation-space" || session.Issuer != c.config.Issuer || len(session.Grant) != 43 {
		return EvaluationAccess{}, ErrUnauthorized
	}
	input.Grant = session.Grant
	raw, err := json.Marshal(input)
	if err != nil {
		return EvaluationAccess{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint+"/api/identity/v1/evaluation-space-access", bytes.NewReader(raw))
	if err != nil {
		return EvaluationAccess{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	started := time.Now()
	response, err := c.http.Do(req)
	if err != nil {
		return EvaluationAccess{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return EvaluationAccess{}, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return EvaluationAccess{}, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return EvaluationAccess{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result EvaluationAccess
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return EvaluationAccess{}, ErrUnavailable
	}
	if result.Subject != session.Subject || !result.ExpiresAt.After(time.Now()) || len(result.Items) > 50 {
		return EvaluationAccess{}, ErrUnauthorized
	}
	if input.GroupID != "" && (len(result.Items) != 1 || result.Items[0].ID != input.GroupID || result.HasMore) {
		return EvaluationAccess{}, ErrUnauthorized
	}
	if result.ExpiresAt.After(started.Add(time.Minute)) {
		result.ExpiresAt = started.Add(time.Minute)
	}
	return result, nil
}

type EvaluationWorkflowAccessRequest struct {
	Grant   string `json:"grant"`
	GroupID string `json:"group_id"`
	Action  string `json:"action"`
}
type EvaluationWorkflowAccess struct {
	Subject   string    `json:"subject"`
	GroupID   string    `json:"group_id"`
	Allowed   bool      `json:"allowed"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c *Client) EvaluationWorkflowAccess(ctx context.Context, session Session, input EvaluationWorkflowAccessRequest) (EvaluationWorkflowAccess, error) {
	if c.config.ClientID != "evaluation-space" || session.Issuer != c.config.Issuer || len(session.Grant) != 43 || !groupIDPattern.MatchString(input.GroupID) {
		return EvaluationWorkflowAccess{}, ErrUnauthorized
	}
	input.Grant = session.Grant
	raw, err := json.Marshal(input)
	if err != nil {
		return EvaluationWorkflowAccess{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint+"/api/identity/v1/evaluation-space-workflow-access", bytes.NewReader(raw))
	if err != nil {
		return EvaluationWorkflowAccess{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	response, err := c.http.Do(req)
	if err != nil {
		return EvaluationWorkflowAccess{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return EvaluationWorkflowAccess{}, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return EvaluationWorkflowAccess{}, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (32<<10)+1))
	if err != nil || len(raw) > 32<<10 {
		return EvaluationWorkflowAccess{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result EvaluationWorkflowAccess
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return EvaluationWorkflowAccess{}, ErrUnavailable
	}
	if result.Subject != session.Subject || result.GroupID != input.GroupID || !result.ExpiresAt.After(time.Now()) {
		return EvaluationWorkflowAccess{}, ErrUnauthorized
	}
	return result, nil
}
