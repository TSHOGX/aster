package agents

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// PiCompletedSnapshotPath names a completed state independently of the mutable
// native selector. Guest root can edit either; consumers verify recorded bytes.
func PiCompletedSnapshotPath(stateDigest string) (string, error) {
	value := strings.TrimPrefix(stateDigest, "sha256:")
	if !strings.HasPrefix(stateDigest, "sha256:") || len(value) != 64 || strings.ToLower(value) != value {
		return "", fmt.Errorf("canonical native state digest required")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", err
	}
	return ".harness-completed/" + value + ".jsonl", nil
}
