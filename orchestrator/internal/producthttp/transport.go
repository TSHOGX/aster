// Package producthttp is the browser boundary for the Project product model. It
// translates authorized requests into application calls and returns projections;
// it owns no execution, recovery, or attachment lifetime.
//
// Authentication, authorization, origin policy and byte limits live here, in Go.
package producthttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"harness-platform/orchestrator/internal/product"
)

// Authenticator resolves the actor for a request. The composed deployment
// supplies the existing cookie session lookup; a request with no valid identity
// yields an empty actor and is refused before any store call.
type Authenticator interface {
	Actor(*http.Request) (string, error)
}

type AuthenticatorFunc func(*http.Request) (string, error)

func (f AuthenticatorFunc) Actor(r *http.Request) (string, error) { return f(r) }

// Config is the transport's deployment policy. Nothing here is user input.
type Config struct {
	// ObserveHTTP enables timing. The callback must be concurrency-safe and bounded.
	ObserveHTTP func(HTTPObservation)
	// AllowedOrigins are the exact origins a browser may use. Empty means
	// same-origin only, resolved from the forwarded host.
	AllowedOrigins []string
	// TrustForwardedHost honors X-Forwarded-Host/Proto from a trusted reverse
	// proxy. It must be false when Go is directly exposed.
	TrustForwardedHost bool
	MaxBodyBytes       int64
}

func (c Config) bodyLimit() int64 {
	if c.MaxBodyBytes > 0 {
		return c.MaxBodyBytes
	}
	return 1 << 20
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// status maps application errors onto HTTP without inventing detail. A missing,
// foreign and unauthorized resource are all 404: a client must not be able to
// probe for the existence of another Project's children.
func status(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, ""
	case errors.Is(err, product.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, product.ErrInvalid):
		return http.StatusBadRequest, "invalid request"
	case errors.Is(err, product.ErrConflict):
		return http.StatusConflict, "conflicting or stale intent"
	case errors.Is(err, product.ErrDeleting):
		return http.StatusConflict, "resource is deleting"
	case errors.Is(err, product.ErrFileOperationFailed):
		return http.StatusUnprocessableEntity, "file operation failed"
	case errors.Is(err, product.ErrFileOutcomeUnknown):
		return http.StatusConflict, err.Error()
	case errors.Is(err, product.ErrBusy):
		return http.StatusConflict, "project has admitted activity"
	case errors.Is(err, product.ErrPending):
		return http.StatusAccepted, "operation pending"
	case errors.Is(err, product.ErrCapacity):
		return http.StatusServiceUnavailable, "capacity exhausted"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

func fail(w http.ResponseWriter, err error) {
	code, message := status(err)
	if code == http.StatusAccepted {
		writeJSON(w, code, map[string]string{"status": "pending"})
		return
	}
	writeError(w, code, message)
}

// requestOrigin is the origin a browser sent, and host is the origin this
// deployment believes it is serving. Comparing them is the CSRF and
// cross-origin-WebSocket defense; a browser cannot set either from script.
func (c Config) originAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Browsers always send Origin on cross-origin requests and on every
		// WebSocket handshake. Absence means a non-browser client, which carries no
		// ambient cookie and so is not a cross-origin vector.
		return true
	}
	for _, allowed := range c.AllowedOrigins {
		if strings.EqualFold(origin, strings.TrimRight(allowed, "/")) {
			return true
		}
	}
	if len(c.AllowedOrigins) > 0 {
		return false
	}
	parsed, err := parseOriginHost(origin)
	if err != nil {
		// Unparseable Origin is not something a browser produces.
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if c.TrustForwardedHost && r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return strings.EqualFold(parsed, scheme+"://"+c.expectedHost(r))
}

func (c Config) expectedHost(r *http.Request) string {
	if c.TrustForwardedHost {
		if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
			return forwarded
		}
	}
	return r.Host
}

func parseOriginHost(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("origin is not an absolute URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, out any) bool {
	body := http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	// A trailing document is a sign the client is not speaking this protocol.
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "unexpected trailing request body")
		return false
	}
	return true
}

func intParam(values map[string][]string, key string, fallback int) int {
	raw := values[key]
	if len(raw) == 0 {
		return fallback
	}
	n, err := strconv.Atoi(raw[0])
	if err != nil {
		return fallback
	}
	return n
}

func int64Param(values map[string][]string, key string) int64 {
	raw := values[key]
	if len(raw) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(raw[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
