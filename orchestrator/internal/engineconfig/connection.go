// Package engineconfig validates the shared engine connection independently of role-specific content.
package engineconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
)

type Connection struct {
	Endpoint   string `json:"endpoint"`
	StoreID    string `json:"store_id"`
	CAFile     string `json:"ca_file"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
}

func (p Connection) Build(base string) (khorabackend.Config, error) {
	if p.StoreID == "" {
		return khorabackend.Config{}, fmt.Errorf("pinned engine store_id required")
	}
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(base, value)
	}
	endpoint, err := url.Parse(p.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return khorabackend.Config{}, fmt.Errorf("engine must be an origin without credentials, path, query or fragment")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	switch endpoint.Scheme {
	case "http":
		address, err := netip.ParseAddr(endpoint.Hostname())
		if err != nil || !address.IsLoopback() {
			return khorabackend.Config{}, fmt.Errorf("plain HTTP engine must use a loopback IP literal")
		}
		if p.CAFile != "" || p.ClientCert != "" || p.ClientKey != "" {
			return khorabackend.Config{}, fmt.Errorf("TLS files require an HTTPS engine")
		}
	case "https":
		if p.CAFile == "" || p.ClientCert == "" || p.ClientKey == "" {
			return khorabackend.Config{}, fmt.Errorf("HTTPS engine requires CA and client certificate/key")
		}
		ca, err := os.ReadFile(resolve(p.CAFile))
		if err != nil {
			return khorabackend.Config{}, fmt.Errorf("read engine CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return khorabackend.Config{}, fmt.Errorf("invalid engine CA")
		}
		cert, err := tls.LoadX509KeyPair(resolve(p.ClientCert), resolve(p.ClientKey))
		if err != nil {
			return khorabackend.Config{}, fmt.Errorf("load engine client certificate: %w", err)
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{cert}}
	default:
		return khorabackend.Config{}, fmt.Errorf("HTTP or authenticated HTTPS engine required")
	}
	// Process streams have their own lifetime contexts; a global HTTP timeout
	// would detach healthy long turns. Startup and control operations are bounded.
	return khorabackend.Config{Endpoint: p.Endpoint, StoreID: p.StoreID, HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
