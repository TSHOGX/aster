package studioclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

type Client struct {
	endpoint, secret string
	http             *http.Client
}
type Snapshot struct {
	Reference evolution.ContentRef `json:"reference"`
	Entries   []manifest.Entry     `json:"entries"`
}

func New(endpoint, secret string) (*Client, error) {
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, fmt.Errorf("canonical Studio origin required")
	}
	if origin.Scheme != "https" {
		ip := net.ParseIP(origin.Hostname())
		if origin.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote Studio requires HTTPS")
		}
	}
	if len(secret) < 32 || len(secret) > 1024 {
		return nil, fmt.Errorf("Studio input credential required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, TLSHandshakeTimeout: 5 * time.Second, IdleConnTimeout: time.Minute}
	return &Client{endpoint: endpoint, secret: secret, http: &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, grant evolution.EvaluationGrant, side, path string) (*http.Response, error) {
	raw, _ := json.Marshal(struct {
		Grant evolution.EvaluationGrant `json:"grant"`
		Side  string                    `json:"side"`
		Path  string                    `json:"path"`
	}{grant, side, path})
	request, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/volumes-studio/internal/v1/evaluation-input", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth("evolution-space", c.secret)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Studio input unavailable")
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		return nil, fmt.Errorf("Studio input rejected")
	}
	return response, nil
}
func (c *Client) Snapshot(ctx context.Context, grant evolution.EvaluationGrant, side string, ref evolution.ContentRef) (Snapshot, error) {
	var snapshot Snapshot
	response, err := c.request(ctx, grant, side, "")
	if err != nil {
		return snapshot, err
	}
	defer response.Body.Close()
	snapshot, err = evolution.Decode[Snapshot](response.Body)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Reference != ref || manifest.ValidateAuthoring(ref.Profile, snapshot.Entries) != nil || manifest.TreeDigest(snapshot.Entries) != ref.TreeDigest {
		return Snapshot{}, fmt.Errorf("Studio snapshot identity mismatch")
	}
	return snapshot, nil
}
func (c *Client) File(ctx context.Context, grant evolution.EvaluationGrant, side string, entry manifest.Entry) (io.ReadCloser, error) {
	response, err := c.request(ctx, grant, side, entry.Path)
	if err != nil {
		return nil, err
	}
	size, err := manifest.ParseSize(entry.Size)
	if err != nil || size > manifest.MaxFileBytes || response.ContentLength != size || response.Header.Get("X-Content-Digest") != entry.Digest {
		response.Body.Close()
		return nil, fmt.Errorf("Studio file identity mismatch")
	}
	return response.Body, nil
}
