package server

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"harness-platform/orchestrator/internal/shareblob"
	"harness-platform/orchestrator/internal/store"
)

// sharedFile serves GET /shared/{token}/files/{path...}: the frozen bytes of one
// whitelisted file.
//
// Three things make this path structurally different from the owner's artifact
// download, and all three are the point of the snapshot design:
//
//   - The path is looked up in share_link_files, an explicit whitelist. It is
//     never resolved against the workspace, so there is no traversal surface to
//     harden here -- a path that is not a whitelist key simply does not exist.
//   - The bytes come from the content-addressed store, so an agent overwriting
//     the file later cannot change what a published URL returns.
//   - The live workspace is never touched, so a destroyed session's link keeps
//     working and VerifySessionWorkspaceVolume is not in the loop.
func (s *Server) sharedFile(w http.ResponseWriter, r *http.Request) {
	s.applySharedHeaders(w, r)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/shared/")
	token, remainder, found := strings.Cut(rest, "/files/")
	if !found || token == "" || remainder == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	link, _, ok := s.resolveSharedLink(w, r, token)
	if !ok {
		return
	}
	if s.shareBlobs == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	file, ok := s.whitelistedFile(w, r, link, remainder)
	if !ok {
		return
	}

	blob, _, err := s.shareBlobs.Open(file.BlobSHA256)
	if errors.Is(err, shareblob.ErrNotFound) {
		// The row outlived its bytes (an interrupted reclamation, a restored
		// database). Render it as a missing file rather than a server error.
		s.log.Warn("share link references a missing blob",
			"link_id", link.ID, "path", file.Path)
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer blob.Close()

	s.applySharedFileContentHeaders(w, file.Path)
	// The snapshot is immutable, so its modtime is meaningless as a cache
	// validator and the link's creation time is the honest answer. Served through
	// ServeContent for range support on large PDFs.
	http.ServeContent(w, r, filepath.Base(file.Path), link.CreatedAt, blob)
}

// whitelistedFile resolves a request path against the link's whitelist. A miss is
// a 404 with no distinction from an unknown token.
func (s *Server) whitelistedFile(w http.ResponseWriter, r *http.Request, link store.ShareLink, requestPath string) (store.ShareLinkFile, bool) {
	files, err := s.store.ListShareLinkFiles(r.Context(), link.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return store.ShareLinkFile{}, false
	}
	for _, file := range files {
		if file.Path == requestPath {
			return file, true
		}
	}
	writeError(w, http.StatusNotFound, "not found")
	return store.ShareLinkFile{}, false
}

// applySharedFileContentHeaders mirrors the owner-side artifact CSP, with one
// difference that matters: frame-ancestors stays 'self' because the public page
// previews HTML and PDF in a same-origin iframe, and the app's blanket
// X-Frame-Options: DENY has to be excepted for this path in next.config.mjs or
// the two collide and the preview shows the browser's "refused to connect".
//
// Anonymous exposure makes the rest of the policy more important, not less:
// connect-src 'none' and form-action 'none' mean a shared HTML artifact can
// render but cannot phone home or post anywhere.
func (s *Server) applySharedFileContentHeaders(w http.ResponseWriter, path string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		s.setPreviewFraming(w, []string{
			"default-src 'self'",
			"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data: blob:",
			"font-src 'self' data:",
			"media-src 'self' data: blob:",
			"connect-src 'none'",
			"form-action 'none'",
			"frame-src 'none'",
			"object-src 'none'",
			"base-uri 'none'",
		})
	case ".pdf":
		s.setPreviewFraming(w, []string{
			"default-src 'none'",
			"img-src 'self' data: blob:",
			"object-src 'self'",
			"plugin-types application/pdf",
		})
	default:
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("X-Frame-Options", "DENY")
	}
}

// reclaimShareBlobs unlinks blob digests that the sweep reported as having lost
// their last reference.
//
// Ordering is the whole safety argument: SweepExpiredShareLinks decides
// "unreferenced" and deletes the referencing rows inside ONE write transaction,
// and only digests it returns are unlinked, only after that transaction
// committed. A concurrent CreateShareLink that hashes to the same digest either
// commits its reference before the sweep's snapshot (so the digest is not
// reported) or after the sweep committed (so it re-Puts the bytes). Deciding
// reachability outside that transaction is what would let a live link's file be
// deleted underneath it.
func (s *Server) reclaimShareBlobs(digests []string) {
	if s.shareBlobs == nil || len(digests) == 0 {
		return
	}
	reclaimed := 0
	for _, digest := range digests {
		if err := s.shareBlobs.Remove(digest); err != nil {
			// Leaving bytes behind wastes disk; deleting the wrong thing loses
			// data. Log and move on: the next sweep will not re-report this digest,
			// so surface it now rather than silently.
			s.log.Warn("failed to reclaim share blob", "error", err)
			continue
		}
		reclaimed++
	}
	if reclaimed > 0 {
		s.log.Info("reclaimed share link blobs", "count", reclaimed)
	}
}
