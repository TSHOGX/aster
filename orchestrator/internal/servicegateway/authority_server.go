package servicegateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"harness-platform/orchestrator/internal/product"
)

const AuthorityPath = "/gateway/v1/"

type authorityCommand struct {
	Request  product.ServiceRequest `json:"request"`
	Token    string                 `json:"token,omitempty"`
	Expected int64                  `json:"expected,omitempty"`
}

// AuthorityHandler must be mounted only on a protected host Unix socket. The
// configured audience map bounds registration and determines capacity; caller
// input cannot expand it. Registration callers also hold the process lock.
func AuthorityHandler(store *product.Store, audiences map[string]int) http.Handler {
	limits := make(map[string]int, len(audiences))
	for audience, slots := range audiences {
		limits[audience] = slots
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		var command authorityCommand
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&command) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid authority request", 400)
			return
		}
		slots, ok := limits[command.Request.Audience]
		// Configuration revocation denies new/continued authority, but settlement
		// still belongs to the original registered instance. Otherwise a removed
		// audience could strand a durable blocker after its stream was canceled.
		if !ok && r.URL.Path != AuthorityPath+"release" {
			http.Error(w, "audience denied", 403)
			return
		}
		var result any = struct{}{}
		var err error
		switch r.URL.Path {
		case AuthorityPath + "registration":
			result, err = store.GatewayRegistration(r.Context(), command.Request.Audience)
		case AuthorityPath + "register":
			result, err = store.RegisterGateway(r.Context(), command.Request.Audience, command.Request.Instance, command.Expected, slots)
		case AuthorityPath + "acquire":
			result, err = store.AcquireGatewayRequest(r.Context(), command.Request, command.Token)
		case AuthorityPath + "check":
			err = store.CheckGatewayRequest(r.Context(), command.Request)
		case AuthorityPath + "release":
			err = store.ReleaseGatewayRequest(r.Context(), command.Request)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			status := http.StatusServiceUnavailable
			switch {
			case errors.Is(err, product.ErrDraining):
				status = http.StatusLocked
			case errors.Is(err, product.ErrCapacity):
				status = http.StatusTooManyRequests
			case errors.Is(err, product.ErrInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, product.ErrConflict), errors.Is(err, product.ErrNotFound):
				status = http.StatusForbidden
			}
			// No token, SQL detail or upstream secret is returned or logged.
			http.Error(w, http.StatusText(status), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
