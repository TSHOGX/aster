package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"harness-platform/orchestrator/internal/store"
)

func hasRecordedRunscBinaryPin(details store.RuntimeGenerationDetails) bool {
	return strings.TrimSpace(details.RunscBinaryPath) != "" && strings.TrimSpace(details.RunscBinaryDigest) != ""
}

func cleanupRunscPinMatches(current, pinned runscPin) bool {
	checks := []struct {
		current string
		pinned  string
	}{
		{current.Platform, pinned.Platform},
		{current.Version, pinned.Version},
		{current.BinaryPath, pinned.BinaryPath},
		{current.BinaryDigest, pinned.BinaryDigest},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.pinned) != "" && check.current != check.pinned {
			return false
		}
	}
	return true
}

func cleanupRunscPinMismatchEvidence(current, pinned runscPin, cleanupBinary string) string {
	return fmt.Sprintf("runsc_pin:mismatch; current_platform=%s; pinned_platform=%s; current_version=%s; pinned_version=%s; current_binary_path=%s; pinned_binary_path=%s; current_binary_digest=%s; pinned_binary_digest=%s; cleanup_binary=%s",
		current.Platform,
		pinned.Platform,
		current.Version,
		pinned.Version,
		current.BinaryPath,
		pinned.BinaryPath,
		current.BinaryDigest,
		pinned.BinaryDigest,
		cleanupBinary,
	)
}

func verifiedRecordedRunscBinary(pinned runscPin) (string, error) {
	path := strings.TrimSpace(pinned.BinaryPath)
	if path == "" {
		return "", fmt.Errorf("recorded runsc binary path is required")
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("recorded runsc binary path %q must be absolute", path)
	}
	cleaned := filepath.Clean(path)
	canonical, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve recorded runsc binary path %q: %w", path, err)
	}
	canonical = filepath.Clean(canonical)
	if canonical != cleaned {
		return "", fmt.Errorf("recorded runsc binary path %q is not canonical, resolves to %q", cleaned, canonical)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat recorded runsc binary %q: %w", canonical, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("recorded runsc binary %q is not a regular file", canonical)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("recorded runsc binary %q is not executable", canonical)
	}
	digest, err := fileSHA256(canonical)
	if err != nil {
		return "", fmt.Errorf("digest recorded runsc binary %q: %w", canonical, err)
	}
	if got, want := "sha256:"+digest, strings.TrimSpace(pinned.BinaryDigest); got != want {
		return "", fmt.Errorf("recorded runsc binary digest %s, want %s", got, want)
	}
	return canonical, nil
}

func runscPinFromArtifacts(details store.RuntimeGenerationDetails, artifacts GenerationArtifacts) runscPin {
	return runscPin{
		Platform:     effectiveRunscPlatform(details),
		Version:      artifacts.RunscVersion,
		BinaryPath:   artifacts.RunscBinaryPath,
		BinaryDigest: artifacts.RunscBinaryDigest,
	}
}

func runscPinFromDetails(details store.RuntimeGenerationDetails) runscPin {
	return runscPin{
		Platform:     effectiveRunscPlatform(details),
		Version:      details.RunscVersion,
		BinaryPath:   details.RunscBinaryPath,
		BinaryDigest: details.RunscBinaryDigest,
	}
}

func (r *Runtime) verifyLaunchRunscPin(ctx context.Context, operation string, details store.RuntimeGenerationDetails, artifacts GenerationArtifacts) (runscPin, error) {
	current := r.currentRunscPin(ctx)
	if err := verifyRunscPin(operation, "prepared artifacts", current, runscPinFromArtifacts(details, artifacts), true); err != nil {
		return current, err
	}
	if err := verifyRunscPin(operation, "resource instance", current, runscPinFromDetails(details), false); err != nil {
		return current, err
	}
	return current, nil
}

// verifyRunscPin compares the current runsc pin against a recorded pin field by
// field. When requireAll is true, an empty recorded field is a hard error
// (every field must be pinned before the operation); when false, empty recorded
// fields are skipped and only present fields are enforced.
func verifyRunscPin(operation, source string, current, pinned runscPin, requireAll bool) error {
	checks := []struct {
		field   string
		current string
		pinned  string
	}{
		{"runsc_platform", current.Platform, pinned.Platform},
		{"runsc_version", current.Version, pinned.Version},
		{"runsc_binary_path", current.BinaryPath, pinned.BinaryPath},
		{"runsc_binary_digest", current.BinaryDigest, pinned.BinaryDigest},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.pinned) == "" {
			if requireAll {
				return fmt.Errorf("runsc pin missing before %s: %s %s", operation, source, check.field)
			}
			continue
		}
		if check.current != check.pinned {
			return fmt.Errorf("runsc pin mismatch before %s: %s %s current %q pinned %q", operation, source, check.field, check.current, check.pinned)
		}
	}
	return nil
}
