//go:build khoraintegration

package application_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The engine publishes HTTP behind an operator-owned mTLS edge. Both hops use
// the public API. The gateway UID's kernel floor prohibits loopback, including
// the engine API, so its configured endpoint must be an authenticated host IP.
func engineTLS(t *testing.T, directory, endpoint string) map[string]any {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "disposable-release"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("192.0.2.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require(t, err)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	listener, err := net.Listen("tcp", "192.0.2.1:18195")
	require(t, err)
	target, err := url.Parse(endpoint)
	require(t, err)
	server := &http.Server{Handler: httputil.NewSingleHostReverseProxy(target), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}}
	go func() { _ = server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	t.Cleanup(func() { server.Close() })
	require(t, os.WriteFile(filepath.Join(directory, "tls.crt"), certPEM, 0644))
	require(t, os.WriteFile(filepath.Join(directory, "tls.key"), keyPEM, 0644))
	return map[string]any{"endpoint": "https://192.0.2.1:18195", "ca_file": "tls.crt", "client_cert": "tls.crt", "client_key": "tls.key"}
}
