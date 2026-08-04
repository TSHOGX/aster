package server

import (
	"net/http"
	"strings"
)

// setPreviewFraming writes the CSP for a previewable response (HTML or PDF
// artifact, or the same via a share link) with frame-ancestors appended here
// rather than by the caller, so no call site can reintroduce a 'self' that would
// break previews inside an embed.
//
// Deliberately sets no X-Frame-Options: it has no wildcard form, and a browser
// that sees it disagree with frame-ancestors blocks the frame regardless of the
// CSP — the "<host> refused to connect" preview bug.
//
// directives are the response's other CSP directives.
func (s *Server) setPreviewFraming(w http.ResponseWriter, directives []string) {
	w.Header().Set("Content-Security-Policy",
		strings.Join(append(directives, previewFrameAncestors), "; "))
}

// previewFrameAncestors is the frame-ancestors value for previewable content.
//
// It must stay as permissive as the app shell's own policy: 'self' validates the
// ENTIRE ancestor chain, not just the immediate parent, so inside a third-party
// embed a 'self' preview iframe fails the check and renders blank even though the
// app around it loaded fine. Matching the shell's `*` is what keeps HTML and PDF
// previews working when embedded.
//
// Loosening framing does not loosen anything else: the surrounding CSP for these
// responses still pins connect-src 'none' and form-action 'none', so a previewed
// artifact cannot phone home or post anywhere regardless of who framed it.
const previewFrameAncestors = "frame-ancestors *"
