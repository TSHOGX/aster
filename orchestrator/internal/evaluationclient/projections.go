// Package evaluationclient implements the central activation participant transport.
package evaluationclient

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ErrNotFound means an authorized lookup confirmed no stored projection.
var ErrNotFound = errors.New("evaluation projection not found")

type Client struct {
	endpoint, secret string
	http             *http.Client
}

func New(endpoint, secret string) (*Client, error) {
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.ForceQuery {
		return nil, fmt.Errorf("evaluation endpoint must be an origin")
	}
	if origin.Scheme != "https" {
		ip := net.ParseIP(origin.Hostname())
		if origin.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote evaluation endpoint requires HTTPS")
		}
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("central service credential required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	return &Client{endpoint: endpoint, secret: secret, http: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type Projection struct {
	OperationID   string                  `json:"operation_id"`
	Digest        string                  `json:"digest"`
	Configuration evolution.Configuration `json:"configuration"`
}

func (c *Client) Adopt(ctx context.Context, p Projection) (evolution.ActivationReceipt, error) {
	if err := p.Configuration.Validate(); err != nil {
		return evolution.ActivationReceipt{}, err
	}
	raw, err := json.Marshal(p.Configuration)
	if err != nil {
		return evolution.ActivationReceipt{}, err
	}
	if evolution.Digest(raw) != p.Digest {
		return evolution.ActivationReceipt{}, fmt.Errorf("projection digest mismatch")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return evolution.ActivationReceipt{}, err
	}
	return c.call(ctx, "POST", "/evaluation-space/api/v1/projections", body, p)
}

// Reconcile first checks durable participant state. An uncertain adoption is
// resolved by another exact-scope lookup; callers may retry the same operation.
func (c *Client) Reconcile(ctx context.Context, p Projection) (evolution.ActivationReceipt, error) {
	receipt, err := c.Lookup(ctx, p)
	if err == nil {
		return receipt, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return receipt, err
	}
	receipt, adoptErr := c.Adopt(ctx, p)
	if adoptErr == nil {
		return receipt, nil
	}
	receipt, lookupErr := c.Lookup(ctx, p)
	if lookupErr == nil {
		return receipt, nil
	}
	return evolution.ActivationReceipt{}, errors.Join(adoptErr, lookupErr)
}

func (c *Client) Lookup(ctx context.Context, p Projection) (evolution.ActivationReceipt, error) {
	path := "/evaluation-space/api/v1/groups/" + url.PathEscape(p.Configuration.GroupID) + "/projections/" + strconv.FormatInt(p.Configuration.Revision, 10) + "?" + url.Values{"operation_id": {p.OperationID}, "digest": {p.Digest}}.Encode()
	return c.call(ctx, "GET", path, nil, p)
}
func (c *Client) call(ctx context.Context, method, path string, body []byte, p Projection) (evolution.ActivationReceipt, error) {
	var receipt evolution.ActivationReceipt
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return receipt, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("central", c.secret)
	response, err := c.http.Do(request)
	if err != nil {
		return receipt, fmt.Errorf("participant response uncertain: %w", err)
	}
	defer response.Body.Close()
	if method == http.MethodGet && response.StatusCode == http.StatusNotFound {
		return receipt, ErrNotFound
	}
	if response.StatusCode != 200 {
		return receipt, fmt.Errorf("participant status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return receipt, fmt.Errorf("invalid participant receipt")
	}
	receipt, err = evolution.Decode[evolution.ActivationReceipt](bytes.NewReader(raw))
	if err != nil {
		return receipt, err
	}
	if receipt.Participant != "evaluation-space" || receipt.OperationID != p.OperationID || receipt.ConfigurationDigest != p.Digest || receipt.Generation != p.Configuration.Revision || receipt.State != "adopted" {
		return evolution.ActivationReceipt{}, fmt.Errorf("participant receipt identity mismatch")
	}
	return receipt, nil
}
