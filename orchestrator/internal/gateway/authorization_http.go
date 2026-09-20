package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"harness-platform/orchestrator/internal/egress"

	"golang.org/x/sys/unix"
)

type Authorizer interface {
	ResolveEgressRoute(context.Context, egress.RouteRequest) (egress.RouteIdentity, error)
	AuthorizeEgressRequest(context.Context, egress.AuthorizationRequest) (egress.Authorization, error)
	FinishEgressRequest(context.Context, egress.RequestFinish) error
}

type peerUIDKey struct{}

// AuthorizationHandler is the trusted gateway control contract. It must be
// served with PeerContext over a protected Unix socket, never a public listener.
func AuthorizationHandler(authorizer Authorizer, allowedUID uint32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, known := r.Context().Value(peerUIDKey{}).(uint32)
		if !known || uid != allowedUID {
			http.Error(w, "gateway peer is not authorized", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/v1/route":
			var request egress.RouteRequest
			if err := decodeControlRequest(decoder, &request); err != nil {
				http.Error(w, "invalid route request", 400)
				return
			}
			request.Now = time.Now().UTC()
			route, err := authorizer.ResolveEgressRoute(r.Context(), request)
			if err != nil {
				writeAuthorizationFailure(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(route)
		case "/v1/authorize":
			var request egress.AuthorizationRequest
			if err := decodeControlRequest(decoder, &request); err != nil {
				http.Error(w, "invalid authorization request", 400)
				return
			}
			// The gateway cannot choose the authority clock.
			request.Now = time.Now().UTC()
			authorization, err := authorizer.AuthorizeEgressRequest(r.Context(), request)
			if err != nil {
				status := http.StatusServiceUnavailable
				if errors.Is(err, egress.ErrDenied) {
					status = http.StatusForbidden
				} else if errors.Is(err, egress.ErrRequestConflict) {
					status = http.StatusConflict
				}
				http.Error(w, "authorization unavailable", status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(authorization)
		case "/v1/finish":
			var request egress.RequestFinish
			if err := decodeControlRequest(decoder, &request); err != nil {
				http.Error(w, "invalid completion request", 400)
				return
			}
			request.Now = time.Now().UTC()
			if err := authorizer.FinishEgressRequest(r.Context(), request); err != nil {
				status := http.StatusServiceUnavailable
				if errors.Is(err, egress.ErrRequestConflict) {
					status = http.StatusConflict
				}
				http.Error(w, "request completion unavailable", status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

// PeerContext obtains identity from the kernel on the accepted Unix connection.
// An HTTP header cannot manufacture this context value.
func PeerContext(ctx context.Context, connection net.Conn) context.Context {
	uid, err := peerUID(connection)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, peerUIDKey{}, uid)
}

func peerUID(connection net.Conn) (uint32, error) {
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("control connection is not a Unix socket")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credential *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credential, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credentialErr != nil {
		return 0, credentialErr
	}
	if credential == nil {
		return 0, fmt.Errorf("kernel omitted control peer credentials")
	}
	return credential.Uid, nil
}

// ListenAuthorization creates a new socket without removing an existing one.
// Startup reconciliation owns stale-socket cleanup; blindly unlinking could
// disconnect a still-serving control plane.
func ListenAuthorization(path string, group int) (*net.UnixListener, error) {
	return ListenControl(path, group)
}

// ListenControl creates either direction of the protected gateway control
// contract. An existing endpoint is never unlinked or taken over implicitly.
func ListenControl(path string, group int) (*net.UnixListener, error) {
	if !filepath.IsAbs(path) || group < 0 {
		return nil, fmt.Errorf("absolute authorization socket and gateway group required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, -1, group); err != nil {
		listener.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func decodeControlRequest(decoder *json.Decoder, target any) error {
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("authorization request contains trailing data")
	}
	return nil
}
