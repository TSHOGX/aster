package manifest

import (
	"fmt"
	"strings"
)

// ValidAuthoringPath adds repository-reserved names to the distribution path
// grammar. Apply before accepting a draft, output reservation or generated patch.
func ValidAuthoringPath(value string, directory bool) error {
	if err := ValidPath(value, directory); err != nil {
		return err
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return fmt.Errorf("reserved Git path: %s", value)
		}
	}
	return nil
}
