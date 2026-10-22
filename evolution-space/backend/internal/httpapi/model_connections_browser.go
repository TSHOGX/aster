package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evolution-space/internal/modelconn"
)

// BrowserModelConnectionAPI lets platform administrators manage installation-
// wide model connections. Keys are write-only: responses report only whether a
// key is set and a short suffix hint.
func BrowserModelConnectionAPI(options BrowserOptions) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	mux := http.NewServeMux()
	admin := func(w http.ResponseWriter, r *http.Request) bool {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if !session.HasEntitlement(ModelsEntitlement) {
			write(w, 403, map[string]string{"error": "model_connections_admin_required"})
			return false
		}
		if options.Connections == nil {
			write(w, 409, map[string]string{"error": "model_connections_disabled"})
			return false
		}
		return true
	}
	fail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, modelconn.ErrConflict):
			write(w, 409, map[string]string{"error": "model_connection_changed"})
		case errors.Is(err, modelconn.ErrNoSealKey):
			write(w, 409, map[string]string{"error": "model_key_encryption_not_configured"})
		case errors.Is(err, modelconn.ErrNotFound):
			write(w, 404, map[string]string{"error": "model_connection_not_found"})
		case errors.Is(err, modelconn.ErrInvalid):
			write(w, 422, map[string]string{"error": "invalid_model_connection", "message": strings.TrimPrefix(err.Error(), modelconn.ErrInvalid.Error()+": ")})
		default:
			write(w, 503, map[string]string{"error": "model_connections_unavailable"})
		}
	}
	mux.HandleFunc("GET /evolution-space/api/v1/model-connections", func(w http.ResponseWriter, r *http.Request) {
		if !admin(w, r) {
			return
		}
		items, err := options.Connections.List(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]any{"items": items, "key_encryption": options.Connections.SealConfigured()})
	})
	mux.HandleFunc("PUT /evolution-space/api/v1/model-connections/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !admin(w, r) {
			return
		}
		input, err := evolution.Decode[modelconn.Input](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_model_connection_request"})
			return
		}
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		saved, err := options.Connections.Save(r.Context(), session.Subject, r.PathValue("id"), input)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, saved)
	})
	mux.HandleFunc("POST /evolution-space/api/v1/model-connections/test", func(w http.ResponseWriter, r *http.Request) {
		if !admin(w, r) {
			return
		}
		input, err := evolution.Decode[struct {
			ID       string             `json:"id,omitempty"`
			Settings modelconn.Settings `json:"settings"`
			Key      string             `json:"api_key,omitempty"`
		}](http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_model_connection_test"})
			return
		}
		key := strings.TrimSpace(input.Key)
		if key == "" && input.ID != "" {
			// A stored key is only ever sent to the host it was saved for.
			stored, err := storedBaseURL(r.Context(), options.Connections, input.ID)
			if err != nil || stored != strings.TrimRight(input.Settings.BaseURL, "/") {
				write(w, 200, modelconn.TestResult{Error: "Enter the API key again to test a different base URL."})
				return
			}
			if key, err = options.Connections.StoredKey(r.Context(), input.ID); err != nil {
				write(w, 200, modelconn.TestResult{Error: "Enter an API key; no usable key is stored for this connection."})
				return
			}
		}
		if key == "" {
			write(w, 200, modelconn.TestResult{Error: "Enter an API key to test this connection."})
			return
		}
		write(w, 200, options.Connections.Test(r.Context(), input.Settings, key))
	})
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, contextWithBrowserSession(r, session))
	}
}

func storedBaseURL(ctx context.Context, connections ModelConnections, id string) (string, error) {
	items, err := connections.List(ctx)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.ID == id {
			return strings.TrimRight(item.Settings.BaseURL, "/"), nil
		}
	}
	return "", errors.New("model connection not found")
}
