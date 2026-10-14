// Package distribution exposes only portable immutable content to configured
// service clients. It is mounted on a private listener, never the browser gateway.
package distribution

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aster/contracts/volumemanifest"
	"volume-studio/backend/internal/authoring"
)

type Client struct {
	ID            string   `json:"id"`
	SecretEnvs    []string `json:"secret_envs"`
	GrantRevision int64    `json:"grant_revision"`
	Libraries     []string `json:"libraries"`
}
type Config struct {
	Clients []Client `json:"clients"`
}

func (c Config) Validate() error {
	if len(c.Clients) == 0 || len(c.Clients) > 64 {
		return fmt.Errorf("distribution needs 1 to 64 service clients")
	}
	seen := map[string]bool{}
	envName := regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	for _, client := range c.Clients {
		if !manifest.ValidID(client.ID) || seen[client.ID] || client.GrantRevision < 1 || len(client.Libraries) == 0 || len(client.Libraries) > 1000 || len(client.SecretEnvs) == 0 || len(client.SecretEnvs) > 2 {
			return fmt.Errorf("invalid distribution client")
		}
		seen[client.ID] = true
		for _, id := range client.Libraries {
			if id != "*" && !manifest.ValidID(id) {
				return fmt.Errorf("invalid library grant")
			}
		}
		for _, name := range client.SecretEnvs {
			if !envName.MatchString(name) || len(os.Getenv(name)) < 32 || len(os.Getenv(name)) > 1024 {
				return fmt.Errorf("distribution credential reference %s is missing or invalid", name)
			}
		}
	}
	return nil
}
func (c Config) authenticate(r *http.Request) (authoring.ServiceGrant, bool) {
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		return authoring.ServiceGrant{}, false
	}
	id, secret, ok := r.BasicAuth()
	if !ok || len(secret) < 32 || len(secret) > 1024 {
		return authoring.ServiceGrant{}, false
	}
	provided := sha256.Sum256([]byte(secret))
	for _, client := range c.Clients {
		if client.ID != id {
			continue
		}
		match := 0
		for _, name := range client.SecretEnvs {
			value := os.Getenv(name)
			expected := sha256.Sum256([]byte(value))
			n := subtle.ConstantTimeCompare(provided[:], expected[:])
			if len(value) >= 32 && len(value) <= 1024 {
				match |= n
			}
		}
		return authoring.ServiceGrant{ClientID: id, Revision: client.GrantRevision, Libraries: client.Libraries}, match == 1
	}
	return authoring.ServiceGrant{}, false
}
func New(store *authoring.Store, config Config) (http.Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /distribution/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]any{"format": manifest.Format, "profiles": []string{manifest.Profile, manifest.SharedAssetsProfile}, "publisher_id": store.PublisherID(), "max_range_bytes": strconv.Itoa(authoring.MaxChunkBytes), "max_manifest_bytes": strconv.Itoa(manifest.MaxManifestBytes)})
	})
	mux.HandleFunc("GET /distribution/v1/libraries", func(w http.ResponseWriter, r *http.Request) {
		libraries, next, err := store.GrantedLibraries(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), r.URL.Query().Get("after"))
		respond(w, map[string]any{"libraries": libraries, "next": next}, err)
	})
	mux.HandleFunc("GET /distribution/v1/libraries/{library}/versions", func(w http.ResponseWriter, r *http.Request) {
		versions, err := store.Catalog(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), r.PathValue("library"), r.URL.Query().Get("after"))
		next := ""
		if len(versions) == 200 {
			next = versions[199].ID
		}
		respond(w, map[string]any{"versions": versions, "next": next}, err)
	})
	mux.HandleFunc("GET /distribution/v1/versions/{version}/manifest", func(w http.ResponseWriter, r *http.Request) {
		raw, digest, err := store.GrantedDescriptor(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), r.PathValue("version"))
		if err != nil {
			storeError(w, err)
			return
		}
		etag := `"` + digest + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("POST /distribution/v1/exports", func(w http.ResponseWriter, r *http.Request) {
		var input authoring.ExportInput
		if !decode(w, r, &input) {
			return
		}
		e, err := store.Export(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), input)
		respond(w, e, err)
	})
	mux.HandleFunc("GET /distribution/v1/exports/{export}", func(w http.ResponseWriter, r *http.Request) {
		e, err := store.ExportStatus(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), r.PathValue("export"))
		respond(w, e, err)
	})
	mux.HandleFunc("POST /distribution/v1/exports/{export}/renew", func(w http.ResponseWriter, r *http.Request) {
		e, err := store.RenewExport(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), r.PathValue("export"))
		respond(w, e, err)
	})
	mux.HandleFunc("DELETE /distribution/v1/exports/{export}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.CloseExport(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), r.PathValue("export")); err != nil {
			storeError(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /distribution/v1/exports/{export}/blobs/{digest}", func(w http.ResponseWriter, r *http.Request) {
		reader, size, err := store.ExportBlob(r.Context(), r.Context().Value(grantKey{}).(authoring.ServiceGrant), r.PathValue("export"), r.PathValue("digest"))
		if err != nil {
			storeError(w, err)
			return
		}
		defer reader.Close()
		etag := `"` + r.PathValue("digest") + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		if value := r.Header.Get("If-Range"); value != "" && value != etag {
			failure(w, 412, "range_identity_changed")
			return
		}
		start, end, partial, err := byteRange(r.Header.Get("Range"), size)
		if err != nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			failure(w, 416, "bounded_single_range_required")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		length := end - start + 1
		if size == 0 {
			length = 0
		}
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if _, err = reader.Seek(start, io.SeekStart); err != nil {
			failure(w, 500, "source_read_failed")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
		if partial {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.WriteHeader(206)
		} else {
			w.WriteHeader(200)
		}
		_, _ = io.CopyN(w, reader, length)
	})
	requests := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		grant, ok := config.authenticate(r)
		if !ok {
			failure(w, 401, "service_authentication_required")
			return
		}
		select {
		case requests <- struct{}{}:
			defer func() { <-requests }()
		default:
			failure(w, 429, "distribution_busy")
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), grantKey{}, grant)))
	}), nil
}

type grantKey struct{}

func byteRange(value string, size int64) (start, end int64, partial bool, err error) {
	if value == "" {
		if size > authoring.MaxChunkBytes {
			return 0, 0, false, authoring.ErrInvalid
		}
		return 0, size - 1, false, nil
	}
	if size == 0 || !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, false, authoring.ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, false, authoring.ErrInvalid
	}
	start, e1 := strconv.ParseInt(parts[0], 10, 64)
	end, e2 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil || start < 0 || end < start || end >= size || end-start+1 > authoring.MaxChunkBytes {
		return 0, 0, false, authoring.ErrInvalid
	}
	return start, end, true, nil
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"error": code})
}
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authoring.ErrNotFound):
		failure(w, 404, "not_found")
	case errors.Is(err, authoring.ErrInvalid):
		failure(w, 422, "invalid_input")
	case errors.Is(err, authoring.ErrConflict), errors.Is(err, authoring.ErrBusy):
		failure(w, 409, "export_unavailable")
	default:
		failure(w, 503, "source_unavailable")
	}
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		storeError(w, err)
		return
	}
	write(w, 200, value)
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		failure(w, 415, "json_required")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil || d.Decode(new(any)) != io.EOF {
		failure(w, 400, "invalid_json")
		return false
	}
	return true
}
