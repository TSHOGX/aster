package trajectory

import (
	"path/filepath"
	"strings"
)

// Capturer writes immutable native snapshots. Completion receipts belong to the product store.
type Capturer struct{ archiveRoot string }

func NewCapturer(root string) *Capturer {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	return &Capturer{archiveRoot: filepath.Join(root, "raw")}
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	repl := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}
	out := strings.Map(repl, s)
	if out == "" {
		return "session"
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}
