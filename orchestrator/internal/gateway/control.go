package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/egress"

	"github.com/google/uuid"
)

const ControlVersion = 1
const gatewayInstanceHeader = "X-Harness-Gateway-Instance"

// GatewayControl is implemented in the upstream protocol process. Finish must
// mean actual upstream activity has stopped, not just cancellation requested.
type GatewayControl interface {
	Activate(string, uint64) error
	Revoke(context.Context, string) error
	RevokeGrant(context.Context, string) error
	Close(context.Context) error
}

type ControlInfo struct {
	Version  int             `json:"version"`
	ID       string          `json:"id"`
	Instance string          `json:"instance"`
	Protocol egress.Protocol `json:"protocol"`
}

type ControlOptions struct {
	ID                  string
	Protocol            egress.Protocol
	ApplicationUID      uint32
	MaxOperationTimeout time.Duration
}

type controlHandler struct {
	control GatewayControl
	info    ControlInfo
	options ControlOptions
}

type controlRequest struct {
	BindingID string `json:"binding_id,omitempty"`
	GrantID   string `json:"grant_id,omitempty"`
	Revision  uint64 `json:"revision,omitempty"`
}

func validControlID(id string) bool {
	return id != "" && len(id) <= 512 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}

func validProtocol(protocol egress.Protocol) bool {
	return protocol == egress.ModelHTTP || protocol == egress.DorisMySQL || protocol == egress.OBSHTTP
}

// ControlHandler is served in the credential-bearing gateway with PeerContext.
// A random instance token fences replies and mutations across gateway restarts.
func ControlHandler(control GatewayControl, p ControlOptions) (http.Handler, error) {
	if control == nil || !validControlID(p.ID) || !validProtocol(p.Protocol) || p.MaxOperationTimeout <= 0 {
		return nil, fmt.Errorf("gateway control identity, protocol, flow registry and timeout required")
	}
	return &controlHandler{control: control, options: p, info: ControlInfo{Version: ControlVersion, ID: p.ID, Instance: uuid.NewString(), Protocol: p.Protocol}}, nil
}

func (h *controlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	uid, ok := r.Context().Value(peerUIDKey{}).(uint32)
	if !ok || uid != h.options.ApplicationUID {
		http.Error(w, "application peer is not authorized", http.StatusForbidden)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "control query is not supported", http.StatusBadRequest)
		return
	}
	w.Header().Set(gatewayInstanceHeader, h.info.Instance)
	if r.URL.Path == "/v1/info" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.info)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get(gatewayInstanceHeader) != h.info.Instance {
		http.Error(w, "gateway instance changed; reconciliation required", http.StatusPreconditionFailed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var p controlRequest
	if err := decodeControlRequest(decoder, &p); err != nil {
		http.Error(w, "invalid gateway control request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.options.MaxOperationTimeout)
	defer cancel()
	var err error
	switch r.URL.Path {
	case "/v1/bindings/activate":
		if !validControlID(p.BindingID) || p.GrantID != "" || p.Revision == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err = h.control.Activate(p.BindingID, p.Revision)
	case "/v1/bindings/revoke":
		if !validControlID(p.BindingID) || p.GrantID != "" || p.Revision != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err = h.control.Revoke(ctx, p.BindingID)
	case "/v1/grants/revoke":
		if !validControlID(p.GrantID) || p.BindingID != "" || p.Revision != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err = h.control.RevokeGrant(ctx, p.GrantID)
	case "/v1/drain":
		if p != (controlRequest{}) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err = h.control.Close(ctx)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, ErrRevoked) {
			status = http.StatusConflict
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, "gateway operation did not settle", status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
