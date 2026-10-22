package evolution

import (
	"encoding/json"
	"fmt"
)

// DefinitionDigest hashes a typed catalog definition with only its own digest
// cleared. Referenced revision digests remain included. This avoids a circular
// self-hash while binding the definition's ID, revision and complete payload.
func DefinitionDigest(value any) (string, error) {
	switch v := value.(type) {
	case Service:
		v.Ref.Digest = ""
		value = v
	case Template:
		v.Ref.Digest = ""
		value = v
	case EvaluationProfile:
		v.Ref.Digest = ""
		value = v
	default:
		return "", fmt.Errorf("unsupported catalog definition")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return Digest(raw), nil
}
