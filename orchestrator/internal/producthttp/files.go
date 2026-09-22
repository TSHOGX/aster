package producthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

func (s *Server) filesAvailable(w http.ResponseWriter) bool {
	if s.Files == nil {
		writeError(w, http.StatusServiceUnavailable, "Files unavailable")
		return false
	}
	return true
}
func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	q := r.URL.Query()
	request := projectfiles.Request{Operation: "list", Path: q.Get("path"), After: q.Get("after"), Limit: intParam(q, "limit", 100)}
	var out boundedFileMetadata
	_, err := s.Files.Run(r.Context(), s.scope(r), uuid.NewString(), request, nil, &out)
	if err != nil {
		fail(w, err)
		return
	}
	var page projectfiles.Page
	if err = json.Unmarshal(out.Bytes(), &page); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

type boundedFileMetadata struct{ bytes.Buffer }

func (b *boundedFileMetadata) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 256<<10 {
		return 0, product.ErrCapacity
	}
	return b.Buffer.Write(p)
}

type fileResponse struct {
	w       http.ResponseWriter
	started bool
}

func (f *fileResponse) Write(p []byte) (int, error) { f.started = true; return f.w.Write(p) }
func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	name := r.URL.Query().Get("path")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Trailer", "X-File-Outcome")
	output := &fileResponse{w: w}
	o, err := s.Files.Run(r.Context(), s.scope(r), uuid.NewString(), projectfiles.Request{Operation: "read", Path: name}, nil, output)
	if err != nil && !output.started {
		w.Header().Del("Content-Disposition")
		w.Header().Del("Trailer")
		fail(w, err)
		return
	}
	if !output.started {
		w.WriteHeader(http.StatusOK)
	}
	w.Header().Set("X-File-Outcome", o.State)
	if err != nil {
		// A truncated transfer must fail the HTTP body, not look like a successful
		// shorter download to a client which ignores trailers.
		panic(http.ErrAbortHandler)
	}
}
func (s *Server) writeFile(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	if r.ContentLength < 0 || r.ContentLength > projectfiles.MaxTransfer {
		writeError(w, http.StatusBadRequest, "bounded Content-Length required")
		return
	}
	request := projectfiles.Request{Operation: "write", Path: r.URL.Query().Get("path"), Size: r.ContentLength, Digest: r.Header.Get("X-Content-SHA256")}
	body := http.MaxBytesReader(w, r.Body, projectfiles.MaxTransfer)
	o, err := s.Files.Run(r.Context(), s.scope(r), r.Header.Get("Idempotency-Key"), request, body, io.Discard)
	s.fileResult(w, o, err)
}
func (s *Server) mutateFile(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	var body struct {
		ID          string `json:"id"`
		Operation   string `json:"operation"`
		Path        string `json:"path"`
		Destination string `json:"destination"`
	}
	if !decode(w, r, s.Config.bodyLimit(), &body) {
		return
	}
	if body.Operation != "mkdir" && body.Operation != "move" && body.Operation != "remove" {
		fail(w, product.ErrInvalid)
		return
	}
	o, err := s.Files.Run(r.Context(), s.scope(r), body.ID, projectfiles.Request{Operation: body.Operation, Path: body.Path, Destination: body.Destination}, nil, io.Discard)
	s.fileResult(w, o, err)
}
func (s *Server) fileResult(w http.ResponseWriter, o product.FileOperation, err error) {
	if err != nil && o.ID == "" {
		fail(w, err)
		return
	}
	code := http.StatusOK
	if err != nil {
		code, _ = status(err)
	}
	writeJSON(w, code, o)
}
func (s *Server) fileOperation(w http.ResponseWriter, r *http.Request) {
	o, err := s.Store.FileOperation(r.Context(), s.scope(r), r.PathValue("operation"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}
func (s *Server) watchFiles(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	// A bounded watch reconnects with a fresh permission check and a rescan. It
	// never owns Project activity. Cancellation closes the engine subscription.
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	watch, err := s.Files.Watch(ctx, s.scope(r), 0)
	if err != nil {
		fail(w, err)
		return
	}
	defer watch.Close()
	stop := context.AfterFunc(ctx, func() { _ = watch.Close() })
	defer stop()
	frames := make(chan sandbox.FileInvalidation, 1)
	go func() {
		defer close(frames)
		for {
			e, err := watch.Next()
			if err != nil {
				return
			}
			select {
			case frames <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(e sandbox.FileInvalidation) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		raw, _ := json.Marshal(e)
		_, err := fmt.Fprintf(w, "event: invalidate\ndata: %s\n\n", raw)
		return err == nil && controller.Flush() == nil
	}
	if !send(sandbox.FileInvalidation{Reset: true, Reason: "attachment"}) {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			actor, err := s.Auth.Actor(r)
			if err != nil || actor != actorFrom(r.Context()) {
				return
			}
			p, err := s.Store.Project(ctx, s.scope(r))
			if err != nil || p.State != "active" {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		case e, ok := <-frames:
			if !ok {
				return
			}
			if !send(e) {
				return
			}
		}
	}
}
