package evolutionspaceclient

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
)

func (c *Client) SealGenerationInput(ctx context.Context, artifact evolution.ArtifactRef, body []byte) error {
	if artifact.Validate() != nil || artifact.Size != int64(len(body)) || evolution.Digest(body) != artifact.Digest {
		return fmt.Errorf("invalid generation input")
	}
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/evolution-space/internal/v1/generation/seal-input", bytes.NewReader(body))
	if err != nil {
		return err
	}
	ref, _ := json.Marshal(artifact)
	request.Header.Set("X-Corpus-Artifact", base64.RawURLEncoding.EncodeToString(ref))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.SetBasicAuth("central", c.secret)
	raw, err := c.generationResponse(request)
	if err != nil {
		return err
	}
	result, err := evolution.Decode[evolution.ArtifactRef](bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if result != artifact {
		return fmt.Errorf("sealed input receipt mismatch")
	}
	return nil
}
func (c *Client) AdmitGeneration(ctx context.Context, assignment evolution.GenerationAssignment) (evolution.Run, error) {
	var result evolution.Run
	if _, err := assignment.Digest(); err != nil {
		return result, err
	}
	raw, _ := json.Marshal(assignment)
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/evolution-space/internal/v1/generation/admit", bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("central", c.secret)
	raw, err = c.generationResponse(request)
	if err != nil {
		return result, err
	}
	result, err = evolution.Decode[evolution.Run](bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	if result.ID != assignment.RunID || result.BatchID != assignment.BatchID || result.GroupID != assignment.GroupID || result.BindingID != assignment.BindingID || result.Budget != assignment.Budget || !reflect.DeepEqual(result.Pins, assignment.Pins) {
		return evolution.Run{}, fmt.Errorf("generation admission receipt mismatch")
	}
	return result, nil
}
func (c *Client) generationResponse(request *http.Request) ([]byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("generation status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return nil, fmt.Errorf("invalid generation response")
	}
	return raw, nil
}

func (c *Client) GenerationOutput(ctx context.Context, scope evolution.GenerationOutputScope) (evolution.GenerationOutput, error) {
	var output evolution.GenerationOutput
	if scope.Validate() != nil {
		return output, fmt.Errorf("invalid generation output scope")
	}
	raw, _ := json.Marshal(scope)
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/evolution-space/internal/v1/generation/output", bytes.NewReader(raw))
	if err != nil {
		return output, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("central", c.secret)
	raw, err = c.generationResponse(request)
	if err != nil {
		return output, err
	}
	output, err = evolution.Decode[evolution.GenerationOutput](bytes.NewReader(raw))
	if err != nil {
		return output, err
	}
	if output.Scope != scope || output.Workflow.Validate() != nil || output.Artifact.Validate() != nil || output.Artifact.GroupID != scope.GroupID {
		return evolution.GenerationOutput{}, fmt.Errorf("generation output receipt mismatch")
	}
	return output, nil
}
