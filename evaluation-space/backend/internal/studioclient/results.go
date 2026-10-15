package studioclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evaluation-space/internal/projections"
)

type ResultBinder interface {
	BindGenerationResult(context.Context, evolution.GenerationGrant, evolution.RunResultScope) error
}
type Results struct {
	Client  *Client
	Store   *projections.Store
	Central ResultBinder
}

func (c *Client) resultCall(ctx context.Context, action string, input any) ([]byte, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/volumes-studio/internal/v1/run-results/"+action, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth("evaluation-space", c.secret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Studio result unavailable")
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return nil, fmt.Errorf("invalid Studio result response")
	}
	if response.StatusCode == 404 && action == "lookup" {
		value, err := evolution.Decode[struct {
			Error string `json:"error"`
		}](bytes.NewReader(raw))
		if err == nil && value.Error == "not_found" {
			return nil, evolution.ErrRunResultNotFound
		}
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Studio result request rejected")
	}
	return raw, nil
}
func (r Results) Base(ctx context.Context, run evolution.Run) ([]manifest.Entry, error) {
	_, grant, err := r.Store.GenerationGrant(ctx, run.GroupID, run.ID)
	if err != nil {
		return nil, err
	}
	raw, err := r.Client.resultCall(ctx, "base", evolution.GenerationGrantScope{Grant: grant, Action: "read", Fence: run.Fence})
	if err != nil {
		return nil, err
	}
	snapshot, err := evolution.Decode[Snapshot](bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if snapshot.Reference != run.Pins.Base || manifest.ValidateAuthoring(snapshot.Reference.Profile, snapshot.Entries) != nil || manifest.TreeDigest(snapshot.Entries) != run.Pins.Base.TreeDigest {
		return nil, fmt.Errorf("Studio base identity mismatch")
	}
	return snapshot.Entries, nil
}
func (r Results) scope(ctx context.Context, run evolution.Run, patch evolution.Patch) (evolution.RunResultScope, error) {
	raw, err := json.Marshal(patch)
	if err != nil {
		return evolution.RunResultScope{}, err
	}
	scope := evolution.RunResultScope{GroupID: run.GroupID, RunID: run.ID, BindingID: run.BindingID, Fence: run.Fence, Base: run.Pins.Base, Output: run.Pins.Output, PatchDigest: evolution.Digest(raw)}
	if err = scope.Matches(patch); err != nil {
		return scope, err
	}
	_, grant, err := r.Store.GenerationGrant(ctx, run.GroupID, run.ID)
	if err != nil {
		return scope, err
	}
	if r.Central == nil {
		return scope, fmt.Errorf("central result binding required")
	}
	return scope, r.Central.BindGenerationResult(ctx, grant, scope)
}
func (r Results) Lookup(ctx context.Context, run evolution.Run, patch evolution.Patch) (evolution.AcceptedRunResult, error) {
	return r.result(ctx, run, patch, false)
}
func (r Results) Submit(ctx context.Context, run evolution.Run, patch evolution.Patch) (evolution.AcceptedRunResult, error) {
	return r.result(ctx, run, patch, true)
}
func (r Results) result(ctx context.Context, run evolution.Run, patch evolution.Patch, submit bool) (evolution.AcceptedRunResult, error) {
	var receipt evolution.AcceptedRunResult
	scope, err := r.scope(ctx, run, patch)
	if err != nil {
		return receipt, err
	}
	action := "lookup"
	var input any = scope
	if submit {
		action = "submit"
		input = struct {
			Scope evolution.RunResultScope `json:"scope"`
			Patch evolution.Patch          `json:"patch"`
		}{scope, patch}
	}
	raw, err := r.Client.resultCall(ctx, action, input)
	if err != nil {
		return receipt, err
	}
	value, err := evolution.Decode[evolution.RunResultReceipt](bytes.NewReader(raw))
	if err != nil || value.Validate(scope) != nil || value.State != "submitted" {
		return receipt, fmt.Errorf("invalid Studio result receipt")
	}
	return evolution.AcceptedRunResult{RunID: value.RunID, PatchDigest: value.PatchDigest, BranchID: value.BranchID, CommitID: value.CommitID}, nil
}
