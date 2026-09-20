package bridge

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/store"
	"strings"
	"testing"
)

type pinnedProtocol struct{ digest string }

func (p pinnedProtocol) BridgeProtocolEvidence(context.Context, string, string) (store.BridgeProtocolEvidence, error) {
	return store.BridgeProtocolEvidence{DriverID: "sh", ProtocolVersion: 2, TurnInputSchema: "RunTurn", AgentManifestDigest: p.digest}, nil
}
func TestRetainedProtocolRequiresPinnedGuestManifest(t *testing.T) {
	want := "sha256:" + strings.Repeat("a", 64)
	for _, actual := range []string{"", "sha256:" + strings.Repeat("b", 64), want} {
		raw, _ := json.Marshal(helloPayload{DriverID: "sh", ProtocolVersion: 2, TurnInputSchema: "RunTurn", ManifestDigest: actual})
		_, _, err := ValidateHelloPayload(context.Background(), pinnedProtocol{want}, Envelope{SessionID: "session", GenerationID: "generation", Payload: raw}, 2, "RunTurn")
		if (err == nil) != (actual == want) {
			t.Fatalf("manifest %q validation: %v", actual, err)
		}
	}
}
