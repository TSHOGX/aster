package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"fmt"
	"strings"
)

// Project/session exclusions select whole evidence records, not substrings in
// model prose. Volume selectors remain relative paths for explicitly curated input.
func (s SourceBinding) ValidateExclusions() error {
	seen := map[string]bool{}
	for _, value := range s.Exclusions {
		if seen[value] {
			return fmt.Errorf("duplicate source exclusion")
		}
		seen[value] = true
		if s.Kind == "volume" {
			if err := manifest.ValidPath(value, false); err != nil {
				return err
			}
			continue
		}
		parts := strings.Split(value, "/")
		if len(parts) != 2 || !manifest.ValidID(parts[1]) || (parts[0] != "sessions" && parts[0] != "turns" && parts[0] != "executions") {
			return fmt.Errorf("source exclusion requires sessions, turns or executions and an exact ID")
		}
	}
	return nil
}
