package producthttp

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

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
	page, err := s.Files.List(r.Context(), s.scope(r), projectfiles.DirectoryQuery{Path: q.Get("path"), Cursor: q.Get("after"), Limit: intParam(q, "limit", 100), Name: q.Get("name"), Seek: q.Get("seek")})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// ServeContent ignores copy errors after headers. Abort the HTTP body if a
// canceled capture or placement loss would otherwise look like a short success.
type captureBody struct {
	*product.CapturedFile
	controller *http.ResponseController
}

func (b captureBody) Read(p []byte) (int, error) {
	_ = b.controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	n, err := b.CapturedFile.Read(p)
	if err != nil && err != io.EOF {
		panic(http.ErrAbortHandler)
	}
	return n, err
}
func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	name := r.URL.Query().Get("path")
	capture, err := s.Files.Capture(r.Context(), s.scope(r), name)
	if err != nil {
		fail(w, err)
		return
	}
	defer capture.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", `"sha256:`+capture.Digest+`"`)
	if strings.Contains(r.Header.Get("Range"), ",") {
		r = r.Clone(r.Context())
		r.Header.Del("Range")
	}
	// Current bytes were re-acquired and verified before any conditional response.
	// Range/If-Range refer only to this immutable capture, never separate live reads.
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	http.ServeContent(w, r, path.Base(name), time.Time{}, captureBody{CapturedFile: capture, controller: controller})
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
	o, err := s.Files.Execute(r.Context(), s.scope(r), r.Header.Get("Idempotency-Key"), request, body)
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
	o, err := s.Files.Execute(r.Context(), s.scope(r), body.ID, projectfiles.Request{Operation: body.Operation, Path: body.Path, Destination: body.Destination}, nil)
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

func (s *Server) openPreviewSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	q := r.URL.Query()
	descriptor, err := s.Files.PreviewSnapshots().Open(r.Context(), s.scope(r), q.Get("path"), int64(intParam(q, "max_bytes", 20<<20)))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, descriptor)
}
func (s *Server) readPreviewSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	descriptor, reader, release, err := s.Files.PreviewSnapshots().Acquire(r.Context(), s.scope(r), r.PathValue("snapshot"))
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"sha256:`+descriptor.Digest+`"`)
	// Reject multipart range amplification. Full content has separate source admission.
	if strings.Contains(r.Header.Get("Range"), ",") {
		fail(w, product.ErrInvalid)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	http.ServeContent(w, r, "preview", time.Time{}, reader)
}
func (s *Server) releasePreviewSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.filesAvailable(w) {
		return
	}
	if err := s.Files.PreviewSnapshots().Release(r.Context(), s.scope(r), r.PathValue("snapshot")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) previewJobsAvailable(w http.ResponseWriter) (*product.PreviewJobs, bool) {
	if !s.filesAvailable(w) {
		return nil, false
	}
	return s.Files.PreviewJobs("/usr/local/bin/preview-worker", ""), true
}
func (s *Server) openPreviewJob(w http.ResponseWriter, r *http.Request) {
	jobs, ok := s.previewJobsAvailable(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	snap, err := s.Files.PreviewSnapshots().Open(r.Context(), s.scope(r), q.Get("path"), int64(intParam(q, "max_bytes", 20<<20)))
	if err != nil {
		fail(w, err)
		return
	}
	status, err := jobs.Open(r.Context(), s.scope(r), snap.ID, q.Get("kind"))
	_ = s.Files.PreviewSnapshots().Release(r.Context(), s.scope(r), snap.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
func (s *Server) previewJobStatus(w http.ResponseWriter, r *http.Request) {
	jobs, ok := s.previewJobsAvailable(w)
	if !ok {
		return
	}
	status, err := jobs.Status(r.Context(), s.scope(r), r.PathValue("preview"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
func (s *Server) previewJobWindow(w http.ResponseWriter, r *http.Request) {
	jobs, ok := s.previewJobsAvailable(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	window, err := jobs.Window(r.Context(), s.scope(r), r.PathValue("preview"), intParam(q, "sheet", 0), intParam(q, "start", 0), intParam(q, "limit", 100))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, window)
}
func (s *Server) previewJobFind(w http.ResponseWriter, r *http.Request) {
	jobs, ok := s.previewJobsAvailable(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	position, err := jobs.Find(r.Context(), s.scope(r), r.PathValue("preview"), intParam(q, "sheet", 0), intParam(q, "start", 0), q.Get("q"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"position": position})
}
func (s *Server) releasePreviewJob(w http.ResponseWriter, r *http.Request) {
	jobs, ok := s.previewJobsAvailable(w)
	if !ok {
		return
	}
	if err := jobs.Release(r.Context(), s.scope(r), r.PathValue("preview")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
