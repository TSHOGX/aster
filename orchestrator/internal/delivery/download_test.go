package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, status int, headers http.Header, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func TestCredentialStrippedOnAssetRedirect(t *testing.T) {
	d := NewDownloader("test/repo")
	calls := 0
	d.Client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(r, 302, http.Header{"Location": []string{"https://download.example.test/asset"}}, ""), nil
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("credential forwarded to asset redirect")
		}
		return response(r, 200, http.Header{}, "ok"), nil
	})
	r, e := d.get(context.Background(), "https://api.github.com/asset", "application/octet-stream", "Bearer secret")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestForeignRegistryRealmRefused(t *testing.T) {
	d := NewDownloader("test/repo")
	calls := 0
	d.Client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(r, 401, http.Header{"Www-Authenticate": []string{`Bearer realm="https://evil.example/token",service="ghcr.io",scope="repository:test/repo:pull"`}}, ""), nil
	})
	if _, e := d.registryGet(context.Background(), "test/repo/manifests/sha256:abc"); e == nil {
		t.Fatal("foreign realm accepted")
	}
	if calls != 1 {
		t.Fatal("followed foreign realm")
	}
}

type pidRunner struct{}

func (pidRunner) Run(context.Context, []string, map[string]string) (string, error) {
	return fmt.Sprint(os.Getpid()), nil
}
func TestSourceIdentityUsesRunningExecutable(t *testing.T) {
	i := Target{BackendService: "test.service", Current: t.TempDir()}
	s, e := InspectSource(context.Background(), i, pidRunner{})
	if e != nil {
		t.Fatal(e)
	}
	if !validHash(s.BinarySHA256) || s.PID != os.Getpid() {
		t.Fatal(s)
	}
	i.LegacyBinarySHA256 = s.BinarySHA256
	if e = CheckSource(context.Background(), i, pidRunner{}); e != nil {
		t.Fatal(e)
	}
	i.LegacyBinarySHA256 = strings.Repeat("0", 64)
	if e = CheckSource(context.Background(), i, pidRunner{}); e == nil {
		t.Fatal("source commit accepted instead of actual executable")
	}
}

func TestFetchCompleteReleaseNeedsNoRegistryCredential(t *testing.T) {
	b := fixtureBundle(t)
	names := []string{"release-manifest.json", b.Manifest.Sandbox.Archive.Name}
	for _, a := range b.Manifest.Artifacts {
		names = append(names, a.Name)
	}
	contents := map[string]string{}
	assets := []map[string]string{}
	for n, name := range names {
		body, err := os.ReadFile(filepath.Join(b.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/repos/test/repo/releases/assets/%d", n+1)
		contents[path] = string(body)
		assets = append(assets, map[string]string{"name": name, "url": "https://api.github.com" + path})
	}
	release, _ := json.Marshal(map[string]any{"draft": false, "assets": assets})
	d := NewDownloader("test/repo")
	d.GitHubToken = "synthetic-token"
	d.RegistryToken = ""
	d.Client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" {
			t.Fatalf("unexpected registry access: %s", r.URL.Host)
		}
		if strings.Contains(r.URL.Path, "/releases/tags/") {
			return response(r, 200, http.Header{}, string(release)), nil
		}
		body, ok := contents[r.URL.Path]
		if !ok {
			return nil, fmt.Errorf("unexpected request")
		}
		return response(r, 200, http.Header{}, body), nil
	})
	fetched, err := Fetch(context.Background(), d, b.Manifest.Version, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = fetched.Verify(); err != nil {
		t.Fatal(err)
	}
}
