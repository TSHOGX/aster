package evaluationclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type CatalogPage struct {
	Revisions    []evolution.Revision `json:"revisions"`
	NextID       string               `json:"next_id"`
	NextRevision int64                `json:"next_revision"`
	HasMore      bool                 `json:"has_more"`
}
type CatalogServiceStatus struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Enabled  bool   `json:"enabled"`
}
type CatalogEntry struct {
	Ref        evolution.Revision    `json:"ref"`
	Definition any                   `json:"definition"`
	Status     *CatalogServiceStatus `json:"status,omitempty"`
}

func CatalogKind(kind string) bool {
	return kind == "template" || kind == "workflow" || kind == "service" || kind == "profile"
}
func (c *Client) catalogRead(ctx context.Context, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", c.endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth("central", c.secret)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("catalog status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, evolution.MaxDocumentBytes+1))
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return nil, fmt.Errorf("catalog response limit")
	}
	return raw, nil
}
func (c *Client) Catalog(ctx context.Context, kind, after string, revision int64, limit int) (CatalogPage, error) {
	var page CatalogPage
	if !CatalogKind(kind) || limit < 1 || limit > 50 || revision < 0 || after == "" && revision != 0 || after != "" && (!manifest.ValidID(after) || revision < 1) {
		return page, fmt.Errorf("invalid catalog cursor")
	}
	raw, err := c.catalogRead(ctx, "/evaluation-space/api/v1/catalog/"+kind+"?"+url.Values{"after_id": {after}, "after_revision": {strconv.FormatInt(revision, 10)}, "limit": {strconv.Itoa(limit)}}.Encode())
	if err != nil {
		return page, err
	}
	page, err = evolution.Decode[CatalogPage](bytes.NewReader(raw))
	if err != nil {
		return page, err
	}
	if len(page.Revisions) > limit || page.HasMore && len(page.Revisions) == 0 {
		return CatalogPage{}, fmt.Errorf("invalid catalog page")
	}
	previousID, previousRevision := after, revision
	for _, ref := range page.Revisions {
		if ref.Validate() != nil || ref.ID < previousID || ref.ID == previousID && ref.Revision <= previousRevision {
			return CatalogPage{}, fmt.Errorf("invalid catalog ordering")
		}
		previousID, previousRevision = ref.ID, ref.Revision
	}
	if page.NextID != previousID || page.NextRevision != previousRevision {
		return CatalogPage{}, fmt.Errorf("invalid catalog continuation")
	}
	return page, nil
}
func (c *Client) CatalogDefinition(ctx context.Context, kind string, ref evolution.Revision) (CatalogEntry, error) {
	result := CatalogEntry{Ref: ref}
	if !CatalogKind(kind) || ref.Validate() != nil {
		return result, fmt.Errorf("invalid catalog reference")
	}
	path := "/evaluation-space/api/v1/catalog/" + kind + "/" + url.PathEscape(ref.ID) + "/" + strconv.FormatInt(ref.Revision, 10) + "?" + url.Values{"digest": {ref.Digest}}.Encode()
	raw, err := c.catalogRead(ctx, path)
	if err != nil {
		return result, err
	}
	var definition any
	var stored evolution.Revision
	switch kind {
	case "workflow":
		graph, err := evolution.DecodeWorkflow(bytes.NewReader(raw))
		if err != nil {
			return result, err
		}
		canonical, err := graph.Canonical()
		if err != nil || graph.ID != ref.ID || evolution.Digest(canonical) != ref.Digest {
			return result, fmt.Errorf("workflow digest mismatch")
		}
		result.Definition = graph
		return result, nil
	case "template":
		value, err := evolution.Decode[evolution.Template](bytes.NewReader(raw))
		if err != nil {
			return result, err
		}
		if err = value.Validate(); err != nil {
			return result, err
		}
		definition = value
		stored = value.Ref
	case "profile":
		value, err := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(raw))
		if err != nil {
			return result, err
		}
		if err = value.Validate(); err != nil {
			return result, err
		}
		definition = value
		stored = value.Ref
	case "service":
		value, err := evolution.Decode[evolution.Service](bytes.NewReader(raw))
		if err != nil {
			return result, err
		}
		if err = value.Validate(); err != nil {
			return result, err
		}
		definition = value
		stored = value.Ref
	}
	digest, err := evolution.DefinitionDigest(definition)
	if err != nil || stored != ref || digest != ref.Digest {
		return result, fmt.Errorf("catalog definition mismatch")
	}
	if service, ok := definition.(evolution.Service); ok {
		raw, err = c.catalogRead(ctx, "/evaluation-space/api/v1/catalog/service/"+url.PathEscape(ref.ID)+"/"+strconv.FormatInt(ref.Revision, 10)+"/status?"+url.Values{"digest": {ref.Digest}}.Encode())
		if err != nil {
			return result, err
		}
		status, err := evolution.Decode[CatalogServiceStatus](bytes.NewReader(raw))
		if err != nil || status.ID != ref.ID || status.Revision < 1 {
			return result, fmt.Errorf("invalid service status")
		}
		result.Status = &status
		// Browser selectors need capabilities and availability, never operator wiring.
		result.Definition = struct {
			Ref          evolution.Revision     `json:"ref"`
			Kind         string                 `json:"kind"`
			Enabled      bool                   `json:"enabled"`
			Capabilities evolution.Capabilities `json:"capabilities"`
		}{service.Ref, service.Kind, service.Enabled, service.Capabilities}
	} else {
		result.Definition = definition
	}
	return result, nil
}

type ConfigurationValidation struct {
	Digest string `json:"digest"`
	Valid  bool   `json:"valid"`
	Error  string `json:"error,omitempty"`
}

func (c *Client) ValidateConfiguration(ctx context.Context, config evolution.Configuration) (ConfigurationValidation, error) {
	var result ConfigurationValidation
	raw, err := json.Marshal(config)
	if err != nil {
		return result, err
	}
	digest := evolution.Digest(raw)
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/evaluation-space/api/v1/catalog/validate", bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	request.SetBasicAuth("central", c.secret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, fmt.Errorf("catalog validation status %d", response.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return result, fmt.Errorf("invalid validation response")
	}
	result, err = evolution.Decode[ConfigurationValidation](bytes.NewReader(raw))
	if err != nil || result.Digest != digest || result.Valid && result.Error != "" || !result.Valid && result.Error == "" {
		return ConfigurationValidation{}, fmt.Errorf("invalid configuration validation identity")
	}
	return result, nil
}
