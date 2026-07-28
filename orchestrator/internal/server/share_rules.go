package server

import (
	"fmt"
	"strings"

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
