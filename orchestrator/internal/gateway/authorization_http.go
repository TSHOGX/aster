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
	"sync"
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
	credential, err := peerCredentials(connection)
	if err != nil {
		return 0, err
	}
	return credential.Uid, nil
}
func peerCredentials(connection net.Conn) (*unix.Ucred, error) {
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("control connection is not a Unix socket")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credential *unix.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credential, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	if credentialErr != nil {
		return nil, credentialErr
	}
	if credential == nil {
		return nil, fmt.Errorf("kernel omitted control peer credentials")
	}
	return credential, nil
}

// DialControl authenticates the server before returning a socket to a caller.
func DialControl(ctx context.Context, path string, uid uint32) (net.Conn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	observed, err := peerUID(connection)
	if err != nil || observed != uid {
		_ = connection.Close()
		return nil, fmt.Errorf("control Unix peer does not match configured UID")
	}
	return connection, nil
}

// ListenAuthorization uses the same locked, crash-recoverable listener as gateways.
func ListenAuthorization(path string, group int) (net.Listener, error) {
	return ListenControl(path, group)
}

// ListenControl holds a process lock for the listener's entire lifetime. After
// a crash a new process can reclaim its own stale socket, but never a live peer.
func ListenControl(path string, group int) (net.Listener, error) {
	if !filepath.IsAbs(path) || group < 0 {
		return nil, fmt.Errorf("absolute control socket and group required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), path+".lock")
	keep := false
	defer func() {
		if !keep {
			_ = lock.Close()
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("control lock must be a regular file owned by this process UID")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, fmt.Errorf("control listener is already owned: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("control path is not a socket")
		}
		if err := unix.Lstat(path, &stat); err != nil {
			return nil, err
		}
		if stat.Uid != uint32(os.Geteuid()) {
			return nil, fmt.Errorf("stale control socket belongs to another UID")
		}
		connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, fmt.Errorf("control endpoint is still serving")
		}
		if !errors.Is(dialErr, unix.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot verify stale control socket: %w", dialErr)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
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
	keep = true
	return &lockedControlListener{UnixListener: listener, lock: lock}, nil
}

type lockedControlListener struct {
	*net.UnixListener
	lock *os.File
	once sync.Once
	err  error
}

func (l *lockedControlListener) Close() error {
	l.once.Do(func() { l.err = errors.Join(l.UnixListener.Close(), l.lock.Close()) })
	return l.err
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
