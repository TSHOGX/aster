// Package strutil holds small, dependency-free string helpers shared across the
// orchestrator packages.
package strutil

import "strings"

// Default returns the trimmed value, or fallback when the value is empty or
// only whitespace. The returned non-fallback value is always whitespace-trimmed.
func Default(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
