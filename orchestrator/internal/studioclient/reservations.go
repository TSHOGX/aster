// Package evolutionspaceclient implements the Studio activation participant transport.
package studioclient

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
	"time"
)

// ErrNotFound means an authorized lookup confirmed no stored projection.
var ErrRejected = errors.New("Studio reservation rejected")

var ErrNotFound = errors.New("Studio reservation not found")

type Client struct {
	endpoint, secret string
	http             *http.Client
}

func New(endpoint, secret string) (*Client, error) {
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.ForceQuery {
		return nil, fmt.Errorf("Studio endpoint must be an origin")
	}
	if origin.Scheme != "https" {
		ip := net.ParseIP(origin.Hostname())
		if origin.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("remote Studio endpoint requires HTTPS")
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
	if path == "/volumes-studio/internal/v1/evolution/lookup" && response.StatusCode == http.StatusNotFound {
		return receipt, ErrNotFound
	}
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusUnprocessableEntity {
		return receipt, ErrRejected
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
	if receipt.Participant != "studio" || receipt.OperationID != p.OperationID || receipt.ConfigurationDigest != p.Digest || receipt.Generation != p.Configuration.Revision || (receipt.State != "reserved" && receipt.State != "adopted" && receipt.State != "released") {
		return evolution.ActivationReceipt{}, fmt.Errorf("participant receipt identity mismatch")
	}
	return receipt, nil
}

func (c *Client) request(ctx context.Context, action string, p Projection) (evolution.ActivationReceipt, error) {
	if p.Configuration.Validate() != nil {
		return evolution.ActivationReceipt{}, fmt.Errorf("invalid configuration")
	}
	raw, err := json.Marshal(p.Configuration)
	if err != nil || evolution.Digest(raw) != p.Digest {
		return evolution.ActivationReceipt{}, fmt.Errorf("configuration digest mismatch")
	}
	var body []byte
	if action == "reserve" {
		body, err = json.Marshal(p)
	} else {
		body, err = json.Marshal(struct {
			GroupID     string `json:"group_id"`
			OperationID string `json:"operation_id"`
			Revision    int64  `json:"revision"`
			Digest      string `json:"digest"`
		}{p.Configuration.GroupID, p.OperationID, p.Configuration.Revision, p.Digest})
	}
	if err != nil {
		return evolution.ActivationReceipt{}, err
	}
	return c.call(ctx, "POST", "/volumes-studio/internal/v1/evolution/"+action, body, p)
}
func (c *Client) Lookup(ctx context.Context, p Projection) (evolution.ActivationReceipt, error) {
	return c.request(ctx, "lookup", p)
}

// Reserve recovers a lost reply by looking up durable state; released operations
// are terminal and must never be reused to claim fresh ownership.
func (c *Client) Reserve(ctx context.Context, p Projection) (evolution.ActivationReceipt, error) {
	receipt, err := c.Lookup(ctx, p)
	if errors.Is(err, ErrNotFound) {
		receipt, err = c.request(ctx, "reserve", p)
		if errors.Is(err, ErrRejected) {
			return evolution.ActivationReceipt{}, err
		}
		if err != nil {
			receipt, err = c.Lookup(ctx, p)
		}
	}
	if err != nil {
		return receipt, err
	}
	if receipt.State == "released" {
		return evolution.ActivationReceipt{}, fmt.Errorf("reservation released")
	}
	return receipt, nil
}
func (c *Client) Adopt(ctx context.Context, p Projection) (evolution.ActivationReceipt, error) {
	receipt, err := c.Lookup(ctx, p)
	if err != nil {
		return receipt, err
	}
	if receipt.State == "adopted" {
		return receipt, nil
	}
	if receipt.State != "reserved" {
		return evolution.ActivationReceipt{}, fmt.Errorf("reservation is not pending")
	}
	receipt, err = c.request(ctx, "adopt", p)
	if errors.Is(err, ErrRejected) {
		return evolution.ActivationReceipt{}, err
	}
	if err != nil {
		receipt, err = c.Lookup(ctx, p)
	}
	if err != nil {
		return receipt, err
	}
	if receipt.State != "adopted" {
		return evolution.ActivationReceipt{}, fmt.Errorf("adoption remains pending")
	}
	return receipt, nil
}
func (c *Client) Cancel(ctx context.Context, p Projection) (evolution.ActivationReceipt, error) {
	receipt, err := c.request(ctx, "cancel", p)
	if err != nil {
		original := err
		receipt, err = c.Lookup(ctx, p)
		if err != nil {
			return evolution.ActivationReceipt{}, errors.Join(original, err)
		}
	}

	if receipt.State != "released" {
		return evolution.ActivationReceipt{}, fmt.Errorf("reservation not released")
	}
	return receipt, nil
}
