package servicegateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"harness-platform/orchestrator/internal/product"
)

const PlatformPath = "/platform/v1/schedules"
const PlatformOwnerPath = "/platform-owner/v1/schedules"
const PlatformVersion = product.PlatformSchedulesVersion

type Platform interface {
	PlatformSchedules(context.Context, product.ServiceRequest, product.PlatformScheduleRequest) (product.PlatformScheduleResult, error)
}
type platformCommand struct {
	Request product.ServiceRequest          `json:"request"`
	Input   product.PlatformScheduleRequest `json:"input"`
}

func decodePlatform(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, product.PlatformScheduleBodyLimit))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		platformError(w, product.ErrInvalid)
		return false
	}
	return true
}
func platformError(w http.ResponseWriter, err error) {
	code, status := "unavailable", http.StatusServiceUnavailable
	switch {
	case errors.Is(err, product.ErrInvalid):
		code, status = "invalid", 400
	case errors.Is(err, product.ErrConflict):
		code, status = "conflict", 409
	case errors.Is(err, product.ErrNotFound), errors.Is(err, product.ErrDeleting):
		code, status = "not_found", 404
	case errors.Is(err, product.ErrCapacity):
		code, status = "capacity", 429
	case errors.Is(err, product.ErrDraining):
		code, status = "draining", 423
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// PlatformHandler belongs only on the protected owner socket. The capability
// allowlist is immutable owner configuration; guest JSON cannot expand it.
func PlatformHandler(store Platform, audiences map[string]bool) http.Handler {
	allowed := map[string]bool{}
	for audience, enabled := range audiences {
		allowed[audience] = enabled
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PlatformOwnerPath || r.Method != "POST" {
			platformError(w, product.ErrInvalid)
			return
		}
		var command platformCommand
		if !decodePlatform(w, r, &command) {
			return
		}
		if !allowed[command.Request.Audience] {
			platformError(w, ErrAuthorityUnavailable)
			return
		}
		result, err := store.PlatformSchedules(r.Context(), command.Request, command.Input)
		if err != nil {
			platformError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	})
}
func (a *RemoteAuthority) PlatformSchedules(ctx context.Context, slot product.ServiceRequest, input product.PlatformScheduleRequest) (out product.PlatformScheduleResult, err error) {
	if slot.Audience != a.audience || slot.Instance != a.instance {
		return out, product.ErrNotFound
	}
	raw, err := json.Marshal(platformCommand{Request: slot, Input: input})
	if err != nil {
		return out, product.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://owner"+PlatformOwnerPath, bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Use the same protected transport, with a bounded product-operation timeout.
	client := &http.Client{Transport: a.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return out, ErrAuthorityUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
		decoder := json.NewDecoder(io.LimitReader(response.Body, product.PlatformScheduleBodyLimit+1))
		if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF {
			return out, ErrAuthorityUnavailable
		}
		return out, nil
	case 400:
		return out, product.ErrInvalid
	case 404:
		return out, product.ErrNotFound
	case 409:
		return out, product.ErrConflict
	case 423:
		return out, product.ErrDraining
	case 429:
		return out, product.ErrCapacity
	default:
		return out, ErrAuthorityUnavailable
	}
}
func (g *Gateway) servePlatform(w http.ResponseWriter, r *http.Request, a product.Attribution) {
	if r.URL.Path != PlatformPath || r.URL.RawQuery != "" || g.config.Platform == nil || a.Request == nil {
		platformError(w, ErrAuthorityUnavailable)
		return
	}
	var input product.PlatformScheduleRequest
	if !decodePlatform(w, r, &input) {
		return
	}
	result, err := g.config.Platform.PlatformSchedules(r.Context(), *a.Request, input)
	if err != nil {
		platformError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}
