package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/config"
)

// Previews must be framable from anywhere, matching the app shell's own
// frame-ancestors *. A 'self' here would validate the whole ancestor chain and
// blank the preview iframe inside a third-party embed, and any X-Frame-Options at
// all would disagree with the CSP and make browsers block the frame outright.
func TestPreviewFramingAllowsAnyAncestorAndOmitsXFrameOptions(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	rec := httptest.NewRecorder()
	s.setPreviewFraming(rec, []string{"default-src 'none'"})

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors *") {
		t.Fatalf("CSP = %q, want frame-ancestors *", csp)
	}
	// The caller's own directives must survive alongside the appended one.
	if !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP = %q, dropped the caller's directives", csp)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want it omitted entirely", got)
	}
}

// A shared HTML artifact is anonymous and now framable by anyone, so the
// exfiltration blocks are the whole defense: connect-src 'none' is what stops a
// shared page phoning home once a hostile parent has framed it.
func TestSharedHTMLKeepsExfiltrationBlocksWhenFramingIsOpen(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	rec := httptest.NewRecorder()
	s.applySharedFileContentHeaders(rec, "report.html")

	csp := rec.Header().Get("Content-Security-Policy")
	for _, must := range []string{"connect-src 'none'", "form-action 'none'", "frame-ancestors *"} {
		if !strings.Contains(csp, must) {
			t.Fatalf("CSP = %q, want it to contain %q", csp, must)
		}
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// Only HTML and PDF are framed by the UI. Everything else stays hard-denied:
// opening framing for previews is not a reason to make a CSV framable.
func TestNonPreviewableStaysDenied(t *testing.T) {
	s := &Server{cfg: config.Config{}}
	rec := httptest.NewRecorder()
	s.applySharedFileContentHeaders(rec, "data.csv")

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatalf("CSP = %q, want sandbox", csp)
	}
}
