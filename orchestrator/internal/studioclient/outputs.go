package studioclient

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var ErrOutputDenied = errors.New("output metadata denied")
var ErrOutputChanged = errors.New("output catalog changed")

func (c *Client) outputRead(ctx context.Context, scope evolution.OutputCatalogScope) ([]byte, error) {
	if scope.Query.Validate() != nil || !manifest.ValidID(scope.Ticket) {
		return nil, fmt.Errorf("invalid output scope")
	}
	raw, _ := json.Marshal(scope)
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/volumes-studio/internal/v1/evolution/outputs", bytes.NewReader(raw))
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
	if response.StatusCode == 403 {
		return nil, ErrOutputDenied
	}
	if response.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if response.StatusCode == 409 {
		return nil, ErrOutputChanged
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("output metadata status %d", response.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return nil, fmt.Errorf("output metadata limit")
	}
	return raw, nil
}
func (c *Client) OutputCatalog(ctx context.Context, scope evolution.OutputCatalogScope) (evolution.OutputCatalogPage, error) {
	var result evolution.OutputCatalogPage
	raw, err := c.outputRead(ctx, scope)
	if err != nil {
		return result, err
	}
	result, err = evolution.Decode[evolution.OutputCatalogPage](bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	if len(result.Items) > scope.Query.Limit || result.Total < len(result.Items) || len(result.Next) > 4096 {
		return evolution.OutputCatalogPage{}, fmt.Errorf("invalid output page")
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		if !manifest.ValidID(item.ID) || !manifest.SupportedProfile(item.Profile) || seen[item.ID] {
			return evolution.OutputCatalogPage{}, fmt.Errorf("invalid output library")
		}
		seen[item.ID] = true
	}
	return result, nil
}
func (c *Client) OutputLibrary(ctx context.Context, scope evolution.OutputCatalogScope) (evolution.OutputLibraryDetail, error) {
	var result evolution.OutputLibraryDetail
	raw, err := c.outputRead(ctx, scope)
	if err != nil {
		return result, err
	}
	result, err = evolution.Decode[evolution.OutputLibraryDetail](bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	if result.Library.ID != scope.Query.LibraryID || result.Main.Validate() != nil || result.Main.LibraryID != result.Library.ID || result.Main.Profile != result.Library.Profile {
		return evolution.OutputLibraryDetail{}, fmt.Errorf("output detail scope mismatch")
	}
	if result.Published != nil && (result.Published.Validate() != nil || result.Published.LibraryID != result.Library.ID) {
		return evolution.OutputLibraryDetail{}, fmt.Errorf("output release mismatch")
	}
	for _, reservation := range result.Reservations {
		if manifest.ValidAuthoringPath(reservation.Prefix, true) != nil || reservation.Generation < 1 || reservation.GroupID != scope.Query.GroupID && reservation.BindingID != "" {
			return evolution.OutputLibraryDetail{}, fmt.Errorf("invalid reservation scope")
		}
	}
	return result, nil
}
