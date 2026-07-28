package server

import (
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/store"
)

// Input validation for the sharing endpoints lives here, next to
// session_rules.go, for the same reason those rules moved out of the store: a
// bad grantee or an unknown visibility level is a 400 about what the product
// accepts, not a 500 from a violated table constraint. The store keeps only the
// invariants it can enforce itself (the CHECK constraints and the live-grant
// unique index).

// createShareRequest is the validated form of a POST /shares body.
type createShareRequest struct {
	Grantee    string
	Visibility string
}

// validateCreateShare normalizes and checks a share request against the session
// being shared and the set of known usernames. knownUsers may be nil in open
// mode (no credentials configured), in which case grantee existence is not
// checked -- there is no user list to check against.
func validateCreateShare(grantee, visibility string, session store.Session, owner string, knownUsers map[string]struct{}) (createShareRequest, error) {
	grantee = strings.TrimSpace(grantee)
	if grantee == "" {
		return createShareRequest{}, fmt.Errorf("grantee is required")
	}
	if grantee == owner {
		return createShareRequest{}, fmt.Errorf("cannot share a session with its owner")
	}
	if grantee == session.UserID {
		return createShareRequest{}, fmt.Errorf("cannot share a session with its owner")
	}
	if knownUsers != nil {
		if _, ok := knownUsers[grantee]; !ok {
			return createShareRequest{}, fmt.Errorf("unknown user %q", grantee)
		}
	}
	normalized, err := validateShareVisibility(visibility, store.ShareVisibilityFull)
	if err != nil {
		return createShareRequest{}, err
	}
	return createShareRequest{Grantee: grantee, Visibility: normalized}, nil
}

// createShareLinkRequest is the validated form of a POST /share-links body.
type createShareLinkRequest struct {
	Visibility string
	TTL        time.Duration
	ExpiresAt  time.Time
}

// validateCreateShareLink checks a public-link request.
//
// Two rules carry weight here. First, a public link MUST expire: TTL is capped
// and a zero TTL means "use the cap", never "never". Second, a shell session's
// link is forced to "full" -- in shell mode the command output IS the content, so
// a "summary" that drops execution detail is not a redaction, it is an empty
// page. Asking for summary on a shell session is a mistake worth reporting
// rather than silently satisfying.
func validateCreateShareLink(visibility string, ttl time.Duration, session store.Session, maxTTL time.Duration, now time.Time) (createShareLinkRequest, error) {
	if maxTTL <= 0 {
		return createShareLinkRequest{}, fmt.Errorf("share link ttl cap is not configured")
	}
	normalized, err := validateShareVisibility(visibility, store.ShareVisibilitySummary)
	if err != nil {
		return createShareLinkRequest{}, err
	}
	if isShellSession(session) {
		if strings.TrimSpace(visibility) == store.ShareVisibilitySummary {
			return createShareLinkRequest{}, fmt.Errorf(
				"shell sessions cannot be shared with %q visibility: the command output is the content",
				store.ShareVisibilitySummary)
		}
		normalized = store.ShareVisibilityFull
	}
	if ttl < 0 {
		return createShareLinkRequest{}, fmt.Errorf("ttl must not be negative")
	}
	if ttl == 0 {
		ttl = maxTTL
	}
	if ttl > maxTTL {
		return createShareLinkRequest{}, fmt.Errorf("ttl exceeds the maximum of %s", maxTTL)
	}
	return createShareLinkRequest{Visibility: normalized, TTL: ttl, ExpiresAt: now.Add(ttl)}, nil
}

// isShellSession reports whether a session is a shell session, by mode with a
// driver fallback for rows written before mode was populated.
func isShellSession(session store.Session) bool {
	mode := strings.TrimSpace(session.Mode)
	if mode == "" {
		mode = store.ModeForDriver(session.Agent)
	}
	return mode == "shell"
}

// validateShareVisibility normalizes the visibility enum, substituting fallback
// for an empty value. Identity shares default to "full" (inside the org, seeing
// the work is the point); public links default to "summary".
func validateShareVisibility(visibility, fallback string) (string, error) {
	visibility = strings.TrimSpace(visibility)
	if visibility == "" {
		return fallback, nil
	}
	switch visibility {
	case store.ShareVisibilitySummary, store.ShareVisibilityFull:
		return visibility, nil
	default:
		return "", fmt.Errorf("visibility must be %q or %q", store.ShareVisibilitySummary, store.ShareVisibilityFull)
	}
}
