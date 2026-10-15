package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"aster/contracts/volumemanifest"
	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/identity"
)

type actorKey struct{}
type AuthoringAPI struct {
	Store     *authoring.Store
	mux       *http.ServeMux
	mutations chan struct{}
}

func NewAuthoringAPI(store *authoring.Store) *AuthoringAPI {
	a := &AuthoringAPI{Store: store, mux: http.NewServeMux(), mutations: make(chan struct{}, 4)}
	a.registerBranches()
	a.registerReviews()
	m := a.mux
	m.HandleFunc("GET "+apiPrefix+"capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"profiles": []string{manifest.Profile, manifest.SharedAssetsProfile}, "max_file_bytes": strconv.FormatInt(manifest.MaxFileBytes, 10), "max_tree_bytes": strconv.FormatInt(manifest.MaxTreeBytes, 10), "max_entries": manifest.MaxEntries, "max_chunk_bytes": strconv.Itoa(authoring.MaxChunkBytes), "max_text_bytes": strconv.Itoa(authoring.MaxTextBytes), "page_size": 200})
	})
	m.HandleFunc("GET "+apiPrefix+"libraries", a.libraries)
	m.HandleFunc("POST "+apiPrefix+"libraries", a.create)
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}", a.libraryAction("volume.read", a.library))
	m.HandleFunc("PATCH "+apiPrefix+"libraries/{library}", a.libraryAction("volume.configure", a.metadata))
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/versions/{version}/validation", a.libraryAction("volume.read", a.validation))
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/versions/{version}/search", a.libraryAction("volume.read", a.search))
	m.HandleFunc("POST "+apiPrefix+"libraries/{library}/versions", a.publish)
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/versions", a.libraryAction("volume.read", a.versions))
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/versions/{version}", a.libraryAction("volume.read", a.version))
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/versions/{version}/tree", a.libraryAction("volume.read", a.versionTree))
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/versions/{version}/content", a.libraryAction("volume.read", a.content))
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/versions/{version}/retention", a.libraryAction("volume.read", a.retention))
	m.HandleFunc("POST "+apiPrefix+"libraries/{library}/versions/{version}/lifecycle", a.libraryAction("volume.publish", a.lifecycle))
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/uploads", a.uploads)
	m.HandleFunc("POST "+apiPrefix+"libraries/{library}/uploads", a.uploadStart)
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/uploads/{upload}", a.uploadStatus)
	m.HandleFunc("PUT "+apiPrefix+"libraries/{library}/uploads/{upload}/chunks/{offset}", a.uploadChunk)
	m.HandleFunc("POST "+apiPrefix+"libraries/{library}/uploads/{upload}/complete", a.uploadComplete)
	m.HandleFunc("DELETE "+apiPrefix+"libraries/{library}/uploads/{upload}", a.uploadCancel)
	m.HandleFunc("GET "+apiPrefix+"operations/{operation}", a.operation)
	m.HandleFunc("POST "+apiPrefix+"operations/{operation}/{action}", a.operationAction)
	m.HandleFunc("GET "+apiPrefix+"libraries/{library}/events", a.events)
	return a
}
func (a *AuthoringAPI) Serve(session identity.Session, w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" && r.URL.Path == apiPrefix+"libraries" && !session.CanCreateLibrary {
		authoringError(w, authoring.ErrAuthority)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		select {
		case a.mutations <- struct{}{}:
			defer func() { <-a.mutations }()
		default:
			writeError(w, 429, "ingress_busy")
			return
		}
	}
	a.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, authoring.Actor{Issuer: session.Issuer, Subject: session.Subject})))
}
func actor(r *http.Request) authoring.Actor { return r.Context().Value(actorKey{}).(authoring.Actor) }
func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, 415, "json_required")
		return false
	}
	// Text saves may contain escaped UTF-8, bounded separately after decoding.
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 7*authoring.MaxTextBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil || d.Decode(new(any)) != io.EOF {
		writeError(w, 400, "invalid_json")
		return false
	}
	return true
}
func authoringError(w http.ResponseWriter, err error) {
	var finding manifest.Finding
	if errors.As(err, &finding) {
		writeJSON(w, 422, map[string]any{"error": "profile_validation_failed", "message": finding.Error(), "findings": []manifest.Finding{finding}})
		return
	}
	switch {
	case errors.Is(err, authoring.ErrAuthority):
		writeError(w, 403, "authority_required")
	case errors.Is(err, authoring.ErrInvalid):
		writeError(w, 422, "invalid_input")
	case errors.Is(err, authoring.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, authoring.ErrConflict):
		writeError(w, 409, "revision_or_target_conflict")
	case errors.Is(err, authoring.ErrBusy):
		writeError(w, 409, "unfinished_work")
	case errors.Is(err, authoring.ErrQuota):
		writeError(w, 507, "storage_quota_exceeded")
	default:
		writeError(w, 500, "storage_unavailable")
	}
}
func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		authoringError(w, err)
		return
	}
	writeJSON(w, status, value)
}
func revision(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
func (a *AuthoringAPI) libraries(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mode") == "workbench" {
		p, err := a.Store.WorkbenchCatalog(r.Context(), actor(r), workbenchQuery(r))
		respond(w, 200, p, err)
		return
	}
	list, err := a.Store.Libraries(r.Context(), actor(r), r.URL.Query().Get("after"))
	next := ""
	if len(list) == 200 {
		next = list[199].ID
	}
	respond(w, 200, map[string]any{"libraries": list, "next": next}, err)
}
func (a *AuthoringAPI) create(w http.ResponseWriter, r *http.Request) {
	var b authoring.CreateInput
	if !decodeBody(w, r, &b) {
		return
	}
	l, err := a.Store.Create(r.Context(), actor(r), b)
	respond(w, 201, l, err)
}
func (a *AuthoringAPI) library(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mode") == "workbench" {
		p, err := a.Store.Summary(r.Context(), r.PathValue("library"))
		respond(w, 200, p, err)
		return
	}
	l, err := a.Store.Library(r.Context(), r.PathValue("library"))
	respond(w, 200, l, err)
}
func (a *AuthoringAPI) metadata(w http.ResponseWriter, r *http.Request) {
	var b authoring.MetadataInput
	if !decodeBody(w, r, &b) {
		return
	}
	l, err := a.Store.Update(r.Context(), actor(r), r.PathValue("library"), b)
	respond(w, 200, l, err)
}
func (a *AuthoringAPI) publish(w http.ResponseWriter, r *http.Request) {
	var b authoring.ReleaseInput
	if !decodeBody(w, r, &b) {
		return
	}
	v, err := a.Store.Release(r.Context(), actor(r), r.PathValue("library"), b)
	respond(w, 202, v, err)
}
func (a *AuthoringAPI) versions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mode") == "workbench" {
		p, err := a.Store.VersionCatalog(r.Context(), r.PathValue("library"), workbenchQuery(r))
		respond(w, 200, p, err)
		return
	}
	list, err := a.Store.Versions(r.Context(), r.PathValue("library"), r.URL.Query().Get("after"))
	next := ""
	if len(list) == 200 {
		next = list[199].ID
	}
	respond(w, 200, map[string]any{"versions": list, "next": next}, err)
}
func (a *AuthoringAPI) version(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.Version(r.Context(), r.PathValue("version"))
	if err == nil && v.LibraryID != r.PathValue("library") {
		err = authoring.ErrNotFound
	}
	respond(w, 200, v, err)
}
func (a *AuthoringAPI) versionTree(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mode") == "workbench" {
		p, err := a.Store.WorkbenchTree(r.Context(), r.PathValue("library"), r.PathValue("version"), workbenchQuery(r), false)
		respond(w, 200, p, err)
		return
	}
	v, err := a.Store.Version(r.Context(), r.PathValue("version"))
	if err != nil {
		authoringError(w, err)
		return
	}
	if v.LibraryID != r.PathValue("library") {
		authoringError(w, authoring.ErrNotFound)
		return
	}
	list, next, err := a.Store.VersionTree(r.Context(), v.ID, r.URL.Query().Get("after"))
	respond(w, 200, map[string]any{"entries": list, "next": next}, err)
}
func (a *AuthoringAPI) content(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Range"), ",") {
		writeError(w, 416, "single_range_required")
		return
	}
	reader, e, err := a.Store.Content(r.Context(), r.PathValue("library"), r.URL.Query().Get("path"), r.PathValue("version"))
	if err != nil {
		authoringError(w, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(e.Path)}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", `"`+e.Digest+`"`)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	http.ServeContent(w, r, path.Base(e.Path), time.Time{}, reader)
}
func (a *AuthoringAPI) uploads(w http.ResponseWriter, r *http.Request) {
	list, err := a.Store.BranchUploads(r.Context(), actor(r), r.PathValue("library"), r.URL.Query().Get("branch_id"))
	respond(w, 200, map[string]any{"uploads": list}, err)
}
func (a *AuthoringAPI) uploadStart(w http.ResponseWriter, r *http.Request) {
	var b authoring.UploadInput
	if !decodeBody(w, r, &b) {
		return
	}
	u, err := a.Store.StartUpload(r.Context(), actor(r), r.PathValue("library"), b)
	respond(w, 201, u, err)
}
func (a *AuthoringAPI) upload(w http.ResponseWriter, r *http.Request) (authoring.Upload, bool) {
	u, err := a.Store.ActorUpload(r.Context(), actor(r), r.PathValue("library"), r.PathValue("upload"))
	if err == nil && u.LibraryID != r.PathValue("library") {
		err = authoring.ErrNotFound
	}
	if err != nil {
		authoringError(w, err)
		return u, false
	}
	return u, true
}
func (a *AuthoringAPI) uploadStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.upload(w, r); !ok {
		return
	}
	u, err := a.Store.UploadStatus(r.Context(), r.PathValue("upload"))
	respond(w, 200, u, err)
}
func (a *AuthoringAPI) uploadChunk(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.upload(w, r); !ok {
		return
	}
	offset, err := strconv.ParseInt(r.PathValue("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, 422, "invalid_offset")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, authoring.MaxChunkBytes))
	if err != nil {
		writeError(w, 413, "chunk_too_large")
		return
	}
	u, err := a.Store.Chunk(r.Context(), actor(r), r.PathValue("upload"), offset, data, r.Header.Get("X-Chunk-Digest"))
	respond(w, 200, u, err)
}
func (a *AuthoringAPI) uploadComplete(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.upload(w, r); !ok {
		return
	}
	var b authoring.CompleteInput
	if !decodeBody(w, r, &b) {
		return
	}
	result, err := a.Store.CompleteUpload(r.Context(), actor(r), r.PathValue("upload"), b)
	respond(w, 200, result, err)
}
func (a *AuthoringAPI) uploadCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.upload(w, r); !ok {
		return
	}
	var b struct {
		RequestID string `json:"request_id"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if err := a.Store.CancelUpload(r.Context(), actor(r), r.PathValue("upload"), b.RequestID); err != nil {
		authoringError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *AuthoringAPI) operation(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.Version(r.Context(), r.PathValue("operation"))
	if err == nil {
		err = a.Store.AuthorizeLibrary(r.Context(), actor(r), v.LibraryID, "volume.read")
	}
	respond(w, 200, v, err)
}
func (a *AuthoringAPI) operationAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "retry" && action != "cancel" {
		http.NotFound(w, r)
		return
	}
	var b struct {
		RequestID string `json:"request_id"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	version, err := a.Store.Version(r.Context(), r.PathValue("operation"))
	if err != nil {
		authoringError(w, err)
		return
	}
	if err = a.Store.AuthorizeLibrary(r.Context(), actor(r), version.LibraryID, "volume.publish"); err != nil {
		authoringError(w, err)
		return
	}
	v, err := a.Store.Operation(r.Context(), actor(r), r.PathValue("operation"), authoring.OperationInput{RequestID: b.RequestID, Action: action})
	respond(w, 200, v, err)
}
func (a *AuthoringAPI) lifecycle(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.Version(r.Context(), r.PathValue("version"))
	if err != nil {
		authoringError(w, err)
		return
	}
	if v.LibraryID != r.PathValue("library") {
		authoringError(w, authoring.ErrNotFound)
		return
	}
	var b authoring.OperationInput
	if !decodeBody(w, r, &b) {
		return
	}
	if b.Action != "archive" && b.Action != "retire" {
		writeError(w, 422, "unsupported_lifecycle")
		return
	}
	if b.Action == "retire" {
		v, err = a.Store.Retire(r.Context(), actor(r), v.ID, b.RequestID)
	} else {
		v, err = a.Store.Operation(r.Context(), actor(r), v.ID, b)
	}
	respond(w, 200, v, err)
}

func (a *AuthoringAPI) retention(w http.ResponseWriter, r *http.Request) {
	v, err := a.Store.Version(r.Context(), r.PathValue("version"))
	if err != nil {
		authoringError(w, err)
		return
	}
	if v.LibraryID != r.PathValue("library") {
		authoringError(w, authoring.ErrNotFound)
		return
	}
	retention, err := a.Store.Retention(r.Context(), v.ID)
	respond(w, 200, retention, err)
}

// Bounded cursor polling is the durable invalidation path. It does not hold a
// session authority decision open indefinitely as an unaudited event stream.
func (a *AuthoringAPI) events(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = "0"
	}
	after, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || after < 0 || strconv.FormatInt(after, 10) != raw {
		writeError(w, 422, "invalid_cursor")
		return
	}
	events, err := a.Store.Events(r.Context(), actor(r), r.PathValue("library"), after)
	next := after
	if len(events) > 0 {
		next = events[len(events)-1].Sequence
	}
	respond(w, 200, map[string]any{"events": events, "next": next, "resnapshot": len(events) == 200}, err)
}

func (a *AuthoringAPI) validation(w http.ResponseWriter, r *http.Request) {
	result, err := a.Store.Validation(r.Context(), r.PathValue("library"), r.PathValue("version"))
	respond(w, 200, result, err)
}
