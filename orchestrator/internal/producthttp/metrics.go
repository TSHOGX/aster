package producthttp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// HTTPObservation has bounded labels. Request identifiers belong in restricted
// traces, never metric dimensions. Bodies, paths and identities are not recorded.
type HTTPObservation struct {
	Category string
	Status   int
	Bytes    int64
	Duration time.Duration
}
type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
func httpCategory(r *http.Request) string {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return "control"
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/events"), strings.HasSuffix(p, "/watch"):
		return "events"
	case strings.HasSuffix(p, "/attach"):
		return "terminal"
	case strings.HasSuffix(p, "/files/content"):
		return "file"
	case strings.HasSuffix(p, "/files"):
		return "directory"
	case strings.HasSuffix(p, "/messages"), strings.Contains(p, "/view/"):
		return "conversation"
	default:
		return "index"
	}
}
func (s *Server) diagnostics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewString() // Never trust an inbound correlation ID as authority.
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		if s.Config.ObserveHTTP == nil {
			next.ServeHTTP(w, r)
			return
		}
		out := &timedWriter{ResponseWriter: w, start: time.Now()}
		defer func() {
			s.Config.ObserveHTTP(HTTPObservation{Category: httpCategory(r), Status: out.status, Bytes: out.bytes, Duration: time.Since(out.start)})
		}()
		next.ServeHTTP(out, r)
	})
}

type timedWriter struct {
	http.ResponseWriter
	start  time.Time
	status int
	bytes  int64
}

func (w *timedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *timedWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.Header().Set("Server-Timing", fmt.Sprintf("headers;dur=%.3f", float64(time.Since(w.start).Microseconds())/1000))
	w.ResponseWriter.WriteHeader(status)
}
func (w *timedWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}
func (w *timedWriter) Flush() { _ = w.FlushError() }
func (w *timedWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *timedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}
