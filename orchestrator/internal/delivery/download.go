package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Downloader never sends a registry credential to a redirect or foreign realm.
type Downloader struct {
	Client        *http.Client
	Repository    string
	GitHubToken   string
	RegistryUser  string
	RegistryToken string
}

func NewDownloader(repo string) *Downloader {
	return &Downloader{Repository: repo, GitHubToken: os.Getenv("HARNESS_GITHUB_TOKEN"), RegistryUser: os.Getenv("HARNESS_GHCR_USER"), RegistryToken: os.Getenv("HARNESS_GHCR_TOKEN"), Client: &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("non-HTTPS redirect refused")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}}}
}
func (d *Downloader) get(ctx context.Context, address, accept, auth string) (*http.Response, error) {
	u, e := url.Parse(address)
	if e != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("HTTPS is required")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return nil, e
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	r, e := d.Client.Do(req)
	if e != nil {
		return nil, e
	}
	return r, nil
}
func responseOK(r *http.Response) error {
	if r.StatusCode != 200 {
		r.Body.Close()
		return fmt.Errorf("download HTTP %d", r.StatusCode)
	}
	return nil
}
func (d *Downloader) GitHubAsset(ctx context.Context, version, name string) (io.ReadCloser, error) {
	if e := ValidVersion(version); e != nil {
		return nil, e
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(d.Repository) {
		return nil, fmt.Errorf("invalid GitHub repository")
	}
	auth := ""
	if d.GitHubToken != "" {
		auth = "Bearer " + d.GitHubToken
	}
	r, e := d.get(ctx, "https://api.github.com/repos/"+d.Repository+"/releases/tags/"+version, "application/vnd.github+json", auth)
	if e != nil {
		return nil, e
	}
	if e = responseOK(r); e != nil {
		return nil, e
	}
	var release struct {
		Draft  bool `json:"draft"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	e = json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&release)
	r.Body.Close()
	if e != nil {
		return nil, e
	}
	if release.Draft {
		return nil, fmt.Errorf("draft release is not deployable")
	}
	for _, a := range release.Assets {
		if a.Name == name {
			u, e := url.Parse(a.URL)
			if e != nil || u.Scheme != "https" || u.Host != "api.github.com" || !strings.HasPrefix(u.Path, "/repos/"+d.Repository+"/releases/assets/") {
				return nil, fmt.Errorf("invalid asset URL")
			}
			r, e := d.get(ctx, a.URL, "application/octet-stream", auth)
			if e != nil {
				return nil, e
			}
			if e = responseOK(r); e != nil {
				return nil, e
			}
			return r.Body, nil
		}
	}
	return nil, fmt.Errorf("release asset missing: %s", name)
}
func saveVerified(path string, body io.ReadCloser, a Artifact) error {
	defer body.Close()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".download-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	n, e := io.Copy(f, io.LimitReader(body, a.Size+1))
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if n != a.Size {
		return fmt.Errorf("download size mismatch")
	}
	if e = VerifyFile(tmp, a); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func (d *Downloader) registryGet(ctx context.Context, path string) (*http.Response, error) {
	address := "https://ghcr.io/v2/" + path
	r, e := d.get(ctx, address, "application/vnd.oci.image.manifest.v1+json", "")
	if e != nil {
		return nil, e
	}
	if r.StatusCode != 401 {
		return r, nil
	}
	challenge := r.Header.Get("WWW-Authenticate")
	r.Body.Close()
	values := map[string]string{}
	for _, m := range regexp.MustCompile(`([a-z]+)="([^"]*)"`).FindAllStringSubmatch(challenge, -1) {
		values[m[1]] = m[2]
	}
	realm, e := url.Parse(values["realm"])
	if e != nil || realm.Scheme != "https" || realm.Host != "ghcr.io" || realm.Path != "/token" {
		return nil, fmt.Errorf("untrusted registry authentication realm")
	}
	q := realm.Query()
	q.Set("service", values["service"])
	q.Set("scope", values["scope"])
	realm.RawQuery = q.Encode()
	req, e := http.NewRequestWithContext(ctx, "GET", realm.String(), nil)
	if e != nil {
		return nil, e
	}
	if d.RegistryToken != "" {
		req.SetBasicAuth(d.RegistryUser, d.RegistryToken)
	}
	tokenResponse, e := d.Client.Do(req)
	if e != nil {
		return nil, e
	}
	defer tokenResponse.Body.Close()
	if e = responseOK(tokenResponse); e != nil {
		return nil, e
	}
	var token struct {
		Token string `json:"token"`
	}
	if e = json.NewDecoder(io.LimitReader(tokenResponse.Body, 1<<20)).Decode(&token); e != nil {
		return nil, e
	}
	if token.Token == "" {
		return nil, fmt.Errorf("registry token absent")
	}
	return d.get(ctx, address, "application/vnd.oci.image.manifest.v1+json", "Bearer "+token.Token)
}
func (d *Downloader) Sandbox(ctx context.Context, s Sandbox, path string) error {
	parts := strings.Split(strings.TrimPrefix(s.Reference, "ghcr.io/"), "@sha256:")
	if len(parts) != 2 || !validHash(parts[1]) {
		return fmt.Errorf("invalid OCI digest")
	}
	r, e := d.registryGet(ctx, parts[0]+"/manifests/sha256:"+parts[1])
	if e != nil {
		return e
	}
	if e = responseOK(r); e != nil {
		return e
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	r.Body.Close()
	if e != nil {
		return e
	}
	if hashBytes(b) != parts[1] {
		return fmt.Errorf("OCI manifest digest mismatch")
	}
	var manifest struct {
		SchemaVersion int `json:"schemaVersion"`
		Layers        []struct {
			Digest    string `json:"digest"`
			Size      int64  `json:"size"`
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		return e
	}
	if manifest.SchemaVersion != 2 || len(manifest.Layers) != 1 {
		return fmt.Errorf("expected single-layer rootfs OCI artifact")
	}
	l := manifest.Layers[0]
	if l.Digest != "sha256:"+s.Archive.SHA256 || l.Size != s.Archive.Size || l.MediaType != "application/vnd.harness.rootfs.v1.tar+gzip" {
		return fmt.Errorf("rootfs descriptor mismatch")
	}
	r, e = d.registryGet(ctx, parts[0]+"/blobs/"+l.Digest)
	if e != nil {
		return e
	}
	if e = responseOK(r); e != nil {
		return e
	}
	return saveVerified(path, r.Body, s.Archive)
}
