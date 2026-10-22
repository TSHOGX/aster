package studioclient

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (c *Client) Bases(ctx context.Context, p Projection) (map[string]evolution.ContentRef, error) {
	return c.bases(ctx, p, "bases")
}
func (c *Client) ManualBases(ctx context.Context, p Projection) (map[string]evolution.ContentRef, error) {
	return c.bases(ctx, p, "manual-bases")
}
func (c *Client) bases(ctx context.Context, p Projection, action string) (map[string]evolution.ContentRef, error) {
	body, _ := json.Marshal(struct {
		GroupID     string `json:"group_id"`
		OperationID string `json:"operation_id"`
		Revision    int64  `json:"revision"`
		Digest      string `json:"digest"`
	}{p.Configuration.GroupID, p.OperationID, p.Configuration.Revision, p.Digest})
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/volumes-studio/internal/v1/evolution/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth("central", c.secret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Studio base status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return nil, fmt.Errorf("invalid Studio bases")
	}
	result, err := evolution.Decode[map[string]evolution.ContentRef](bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	for _, binding := range p.Configuration.Bindings {
		if !binding.Enabled {
			continue
		}
		expected[binding.Output.LibraryID] = true
		ref := result[binding.Output.LibraryID]
		if ref.Validate() != nil || ref.LibraryID != binding.Output.LibraryID || ref.Profile != binding.Output.Profile {
			return nil, fmt.Errorf("Studio base scope mismatch")
		}
	}
	if len(result) != len(expected) {
		return nil, fmt.Errorf("unexpected Studio bases")
	}
	return result, nil
}
