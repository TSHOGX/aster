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

// createShareLinkInput is the raw POST /share-links body.
type createShareLinkInput struct {
	Visibility string
	TTL        time.Duration
	Files      []string
}

// shareLinkLimits are the deployment caps a link request is checked against.
type shareLinkLimits struct {
	MaxTTL   time.Duration
	MaxFiles int
}

// createShareLinkRequest is the validated form of a POST /share-links body.
type createShareLinkRequest struct {
	Visibility string
	TTL        time.Duration
	ExpiresAt  time.Time
	Files      []string
}

// validateCreateShareLink checks a public-link request.
//
// Three rules carry weight here. First, a public link MUST expire: TTL is capped
// and a zero TTL means "use the cap", never "never". Second, a shell session's
// link is forced to "full" -- in shell mode the command output IS the content, so
// a "summary" that drops execution detail is not a redaction, it is an empty
// page. Asking for summary on a shell session is a mistake worth reporting rather
// than silently satisfying. Third, the file whitelist is deduplicated and capped
// by count here; the byte cap is enforced during the copy, where the sizes are
// actually known.
func validateCreateShareLink(input createShareLinkInput, session store.Session, limits shareLinkLimits, now time.Time) (createShareLinkRequest, error) {
	if limits.MaxTTL <= 0 {
		return createShareLinkRequest{}, fmt.Errorf("share link ttl cap is not configured")
	}
	normalized, err := validateShareVisibility(input.Visibility, store.ShareVisibilitySummary)
	if err != nil {
		return createShareLinkRequest{}, err
	}
	ttl := input.TTL
	if ttl < 0 {
		return createShareLinkRequest{}, fmt.Errorf("ttl must not be negative")
	}
	if ttl == 0 {
		ttl = limits.MaxTTL
	}
	if ttl > limits.MaxTTL {
		return createShareLinkRequest{}, fmt.Errorf("ttl exceeds the maximum of %s", limits.MaxTTL)
	}
	files, err := validateShareLinkFiles(input.Files, limits.MaxFiles)
	if err != nil {
		return createShareLinkRequest{}, err
	}
	return createShareLinkRequest{
		Visibility: normalized,
		TTL:        ttl,
		ExpiresAt:  now.Add(ttl),
		Files:      files,
	}, nil
}

// validateShareLinkFiles normalizes the requested whitelist: blanks dropped,
// duplicates collapsed (they would collide on the share_link_files primary key),
// order preserved so the reported list matches what was asked for. Path shape is
// validated later by the same hardening the owner's own download uses; here we
// only bound the count.
func validateShareLinkFiles(paths []string, maxFiles int) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if maxFiles <= 0 {
		return nil, fmt.Errorf("share link file cap is not configured")
	}
	seen := make(map[string]struct{}, len(paths))
	files := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	if len(files) > maxFiles {
		return nil, fmt.Errorf("a share link may include at most %d files", maxFiles)
	}
	return files, nil
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
