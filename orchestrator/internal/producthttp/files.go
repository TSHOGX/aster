package producthttp

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/projectfiles"
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
