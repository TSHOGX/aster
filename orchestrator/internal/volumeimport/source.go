package volumeimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"harness-platform/orchestrator/internal/volumeimport/manifest"
)

type Source struct {
	ID          string   `json:"id"`
	PublisherID string   `json:"publisher_id"`
	Endpoint    string   `json:"endpoint"`
	ClientID    string   `json:"client_id"`
	SecretEnv   string   `json:"secret_env"`
	Libraries   []string `json:"libraries"`
}

func (s Source) Validate() error {
	if !manifest.ValidID(s.ID) || !manifest.ValidID(s.PublisherID) || !manifest.ValidID(s.ClientID) || s.SecretEnv == "" || len(s.Libraries) == 0 || len(s.Libraries) > 1000 {
		return fmt.Errorf("invalid trusted volume source")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Hostname() == "" || u.Path != "" || u.RawPath != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("volume source endpoint must be an origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("remote volume source requires HTTPS")
		}
	}
	for _, id := range s.Libraries {
		if id != "*" && !manifest.ValidID(id) {
			return fmt.Errorf("invalid trusted library scope")
		}
	}
	return nil
}
func (s Source) Allows(library string) bool {
	for _, id := range s.Libraries {
		if id == "*" || id == library {
			return true
		}
	}
	return false
}

type Connector struct {
	source Source
	http   *http.Client
}

func NewConnector(source Source) (*Connector, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: time.Minute, MaxIdleConnsPerHost: 4, DisableCompression: true}
	return &Connector{source: source, http: &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type SourceError struct{ Status int }

func (e *SourceError) Error() string {
	return fmt.Sprintf("configured source returned status %d", e.Status)
}
func (c *Connector) request(ctx context.Context, method, path string, body any, headers http.Header) (*http.Response, error) {
	secret := os.Getenv(c.source.SecretEnv)
	if len(secret) < 32 || len(secret) > 1024 {
		return nil, fmt.Errorf("configured volume source credential unavailable")
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.source.Endpoint+"/distribution/v1"+path, reader)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.source.ClientID, secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("configured source unavailable")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, &SourceError{response.StatusCode}
	}
	return response, nil
}
func (c *Connector) json(ctx context.Context, method, path string, body, out any) error {
	response, err := c.request(ctx, method, path, body, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == 204 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return fmt.Errorf("source JSON exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing source response")
	}
	return nil
}

type CatalogLibrary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type CatalogRelease struct {
	ID               string `json:"id"`
	LibraryID        string `json:"library_id"`
	PublisherID      string `json:"publisher_id"`
	Label            string `json:"label"`
	Note             string `json:"note"`
	DescriptorDigest string `json:"descriptor_digest"`
	State            string `json:"state"`
	Publisher        struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
	} `json:"publisher"`
}

func (c *Connector) Libraries(ctx context.Context, after string) ([]CatalogLibrary, string, error) {
	var response struct {
		Libraries []CatalogLibrary `json:"libraries"`
		Next      string           `json:"next"`
	}
	if err := c.json(ctx, "GET", "/libraries?after="+url.QueryEscape(after), nil, &response); err != nil {
		return nil, "", err
	}
	if len(response.Libraries) > 200 || response.Next != "" && !manifest.ValidID(response.Next) {
		return nil, "", ErrIdentityConflict
	}
	result := []CatalogLibrary{}
	for _, library := range response.Libraries {
		if !manifest.ValidID(library.ID) || len(library.Name) > 200 || len(library.Description) > 4000 {
			return nil, "", ErrIdentityConflict
		}
		if c.source.Allows(library.ID) {
			result = append(result, library)
		}
	}
	return result, response.Next, nil
}
func (c *Connector) Releases(ctx context.Context, library, after string) ([]CatalogRelease, string, error) {
	if !manifest.ValidID(library) || !c.source.Allows(library) {
		return nil, "", ErrIdentityConflict
	}
	var response struct {
		Versions []CatalogRelease `json:"versions"`
		Next     string           `json:"next"`
	}
	if err := c.json(ctx, "GET", "/libraries/"+library+"/versions?after="+url.QueryEscape(after), nil, &response); err != nil {
		return nil, "", err
	}
	if len(response.Versions) > 200 || response.Next != "" && !manifest.ValidID(response.Next) {
		return nil, "", ErrIdentityConflict
	}
	for _, v := range response.Versions {
		if v.LibraryID != library || v.PublisherID != c.source.PublisherID || !manifest.ValidID(v.ID) || !manifest.ValidDigest(v.DescriptorDigest) || len(v.Label) > 128 || v.State != "published" {
			return nil, "", ErrIdentityConflict
		}
	}
	return response.Versions, response.Next, nil
}
func (c *Connector) Manifest(ctx context.Context, version, expected string) (manifest.Descriptor, []byte, error) {
	if !manifest.ValidID(version) || !manifest.ValidDigest(expected) {
		return manifest.Descriptor{}, nil, ErrIdentityConflict
	}
	response, err := c.request(ctx, "GET", "/versions/"+version+"/manifest", nil, nil)
	if err != nil {
		return manifest.Descriptor{}, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, manifest.MaxManifestBytes+1))
	if err != nil {
		return manifest.Descriptor{}, nil, err
	}
	d, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil {
		return d, nil, err
	}
	digest, err := d.Digest()
	if err != nil {
		return d, nil, err
	}
	if d.PublisherID != c.source.PublisherID || d.VersionID != version || digest != expected || !c.source.Allows(d.LibraryID) || response.Header.Get("ETag") != `"`+expected+`"` {
		return d, nil, ErrIdentityConflict
	}
	return d, raw, nil
}

type Export struct {
	ID               string `json:"id"`
	VersionID        string `json:"version_id"`
	DescriptorDigest string `json:"descriptor_digest"`
	ExpiresAt        int64  `json:"expires_at"`
	State            string `json:"state"`
}

// Acquire resumes the same immutable source, verifying each complete blob in the
// local cache before acknowledging source acquisition. Export IDs/receipts are
// durable; a lost admission reply reuses the original request key. Expired export
// authority receives a new key without changing the descriptor or byte offsets.
func (c *Connector) Acquire(ctx context.Context, cache *Cache) error {
	status := cache.Status()
	if status.Identity.SourceID != c.source.ID || status.Identity.PublisherID != c.source.PublisherID {
		return ErrIdentityConflict
	}
	if status.State == SourceAcquired {
		return nil
	}
	if status.State == Quarantined {
		return ErrQuarantined
	}
	export, request, err := cache.exportState()
	if err != nil {
		return err
	}
	if export == nil && request == "" {
		request, err = cache.newExportRequest()
		if err != nil {
			return err
		}
	}
	if export == nil {
		var admitted Export
		err = c.json(ctx, "POST", "/exports", map[string]string{"request_id": request, "version_id": status.Identity.VersionID, "descriptor_digest": status.Identity.DescriptorDigest}, &admitted)
		if err != nil {
			return err
		}
		if !manifest.ValidID(admitted.ID) || admitted.VersionID != status.Identity.VersionID || admitted.DescriptorDigest != status.Identity.DescriptorDigest {
			return ErrIdentityConflict
		}
		if err = cache.setExport(&admitted); err != nil {
			return err
		}
		export = &admitted
	}
	if export.State != "open" || export.ExpiresAt <= time.Now().Unix() {
		_, err = cache.newExportRequest()
		if err != nil {
			return err
		}
		return fmt.Errorf("source export expired; retry acquisition with current authority")
	}
	for _, blob := range status.Blobs {
		if blob.Verified {
			continue
		}
		size, _ := manifest.ParseSize(blob.Size)
		offset := blob.Acquired
		for offset < size || size == 0 {
			if export.ExpiresAt <= time.Now().Add(time.Minute).Unix() {
				var renewed Export
				if err = c.json(ctx, "POST", "/exports/"+export.ID+"/renew", nil, &renewed); err != nil {
					return err
				}
				if renewed.ID != export.ID || renewed.DescriptorDigest != export.DescriptorDigest || renewed.VersionID != export.VersionID {
					return ErrIdentityConflict
				}
				if err = cache.setExport(&renewed); err != nil {
					return err
				}
				export = &renewed
			}
			length := size - offset
			if length > MaxChunkBytes {
				length = MaxChunkBytes
			}
			headers := http.Header{"If-Range": {`"` + blob.Digest + `"`}}
			if size != 0 {
				headers.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
			}
			response, err := c.request(ctx, "GET", "/exports/"+export.ID+"/blobs/"+blob.Digest, nil, headers)
			if err != nil {
				var sourceError *SourceError
				if errors.As(err, &sourceError) && sourceError.Status == 409 {
					_, _ = cache.newExportRequest()
				}
				return err
			}
			expectedStatus := 206
			if size == 0 {
				expectedStatus = 200
			}
			if response.StatusCode != expectedStatus || response.Header.Get("ETag") != `"`+blob.Digest+`"` || response.Header.Get("Content-Length") != strconv.FormatInt(length, 10) || size != 0 && response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size) {
				response.Body.Close()
				return ErrIdentityConflict
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, length+1))
			closeErr := response.Body.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if int64(len(data)) != length {
				return ErrCheckpoint
			}
			sum := sha256.Sum256(data)
			if err = cache.Append(blob.Digest, offset, data, "sha256:"+hex.EncodeToString(sum[:])); err != nil {
				return err
			}
			offset += length
			if size == 0 {
				break
			}
		}
	}
	if cache.Status().State != SourceAcquired {
		return ErrCheckpoint
	}
	// Closing a source pin is cleanup, never a dependency of local acquisition.
	_ = c.json(ctx, "DELETE", "/exports/"+export.ID, nil, nil)
	return nil
}
