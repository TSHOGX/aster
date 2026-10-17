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

// EvolutionSpaceAccessRequest uses the opaque application grant, never a caller-supplied
// subject. Empty GroupID lists current readable groups; an exact group checks Action.
type EvolutionSpaceAccessRequest struct {
	Grant   string `json:"grant"`
	GroupID string `json:"group_id,omitempty"`
	Action  string `json:"action"`
	Search  string `json:"search,omitempty"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

var groupIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (r EvolutionSpaceAccessRequest) ValidScope() bool {
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

type EvolutionSpaceGroup struct {
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

type EvolutionSpaceAccess struct {
	Subject   string                `json:"subject"`
	ExpiresAt time.Time             `json:"expires_at"`
	Items     []EvolutionSpaceGroup `json:"items"`
	NextAfter string                `json:"next_after"`
	HasMore   bool                  `json:"has_more"`
}

// EvolutionSpaceGroups never forwards browser credentials. Each call rechecks the
// exact application's current grant, membership and requested group permission.
func (c *Client) EvolutionSpaceGroups(ctx context.Context, session Session, input EvolutionSpaceAccessRequest) (EvolutionSpaceAccess, error) {
	if c.config.ClientID != "evolution-space" || session.Issuer != c.config.Issuer || len(session.Grant) != 43 {
		return EvolutionSpaceAccess{}, ErrUnauthorized
	}
	input.Grant = session.Grant
	raw, err := json.Marshal(input)
	if err != nil {
		return EvolutionSpaceAccess{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint+"/api/identity/v1/evolution-space-access", bytes.NewReader(raw))
	if err != nil {
		return EvolutionSpaceAccess{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	started := time.Now()
	response, err := c.http.Do(req)
	if err != nil {
		return EvolutionSpaceAccess{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return EvolutionSpaceAccess{}, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return EvolutionSpaceAccess{}, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return EvolutionSpaceAccess{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result EvolutionSpaceAccess
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return EvolutionSpaceAccess{}, ErrUnavailable
	}
	if result.Subject != session.Subject || !result.ExpiresAt.After(time.Now()) || len(result.Items) > 50 {
		return EvolutionSpaceAccess{}, ErrUnauthorized
	}
	if input.GroupID != "" && (len(result.Items) != 1 || result.Items[0].ID != input.GroupID || result.HasMore) {
		return EvolutionSpaceAccess{}, ErrUnauthorized
	}
	if result.ExpiresAt.After(started.Add(time.Minute)) {
		result.ExpiresAt = started.Add(time.Minute)
	}
	return result, nil
}

type EvolutionSpaceWorkflowAccessRequest struct {
	Grant   string `json:"grant"`
	GroupID string `json:"group_id"`
	Action  string `json:"action"`
}
type EvolutionSpaceWorkflowAccess struct {
	Subject   string    `json:"subject"`
	GroupID   string    `json:"group_id"`
	Allowed   bool      `json:"allowed"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c *Client) EvolutionSpaceWorkflowAccess(ctx context.Context, session Session, input EvolutionSpaceWorkflowAccessRequest) (EvolutionSpaceWorkflowAccess, error) {
	if c.config.ClientID != "evolution-space" || session.Issuer != c.config.Issuer || len(session.Grant) != 43 || !groupIDPattern.MatchString(input.GroupID) {
		return EvolutionSpaceWorkflowAccess{}, ErrUnauthorized
	}
	input.Grant = session.Grant
	raw, err := json.Marshal(input)
	if err != nil {
		return EvolutionSpaceWorkflowAccess{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint+"/api/identity/v1/evolution-space-workflow-access", bytes.NewReader(raw))
	if err != nil {
		return EvolutionSpaceWorkflowAccess{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)
	response, err := c.http.Do(req)
	if err != nil {
		return EvolutionSpaceWorkflowAccess{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return EvolutionSpaceWorkflowAccess{}, ErrUnauthorized
	}
	if response.StatusCode != 200 {
		return EvolutionSpaceWorkflowAccess{}, ErrUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (32<<10)+1))
	if err != nil || len(raw) > 32<<10 {
		return EvolutionSpaceWorkflowAccess{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result EvolutionSpaceWorkflowAccess
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return EvolutionSpaceWorkflowAccess{}, ErrUnavailable
	}
	if result.Subject != session.Subject || result.GroupID != input.GroupID || !result.ExpiresAt.After(time.Now()) {
		return EvolutionSpaceWorkflowAccess{}, ErrUnauthorized
	}
	return result, nil
}
