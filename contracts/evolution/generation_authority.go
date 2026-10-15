package evolution

import (
	manifest "aster/contracts/volumemanifest"
	"encoding/json"
	"fmt"
)

type GenerationAssignment struct {
	RunID        string    `json:"run_id"`
	GroupID      string    `json:"group_id"`
	BindingID    string    `json:"binding_id"`
	OccurrenceID string    `json:"occurrence_id"`
	Requester    Principal `json:"requester"`
	Automatic    bool      `json:"automatic"`
	Pins         RunPins   `json:"pins"`
	Budget       Budget    `json:"budget"`
}

func (a GenerationAssignment) Digest() (string, error) {
	for _, id := range []string{a.RunID, a.GroupID, a.BindingID, a.OccurrenceID, a.Requester.Subject} {
		if !manifest.ValidID(id) {
			return "", fmt.Errorf("invalid generation identity")
		}
	}
	if a.Requester.Kind != "human" || a.Requester.Issuer == "" || a.Budget.Validate() != nil || a.Pins.Base.Validate() != nil || a.Pins.Output.Validate() != nil {
		return "", fmt.Errorf("invalid generation pins")
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > MaxDocumentBytes {
		return "", fmt.Errorf("invalid generation request size")
	}
	return Digest(raw), nil
}

type GenerationGrant struct {
	ID               string `json:"id"`
	RunID            string `json:"run_id"`
	GroupID          string `json:"group_id"`
	AssignmentDigest string `json:"assignment_digest"`
}

func (g GenerationGrant) Validate() error {
	if !manifest.ValidID(g.ID) || !manifest.ValidID(g.RunID) || !manifest.ValidID(g.GroupID) || !manifest.ValidDigest(g.AssignmentDigest) {
		return fmt.Errorf("invalid generation grant")
	}
	return nil
}

type GenerationGrantScope struct {
	Grant  GenerationGrant `json:"grant"`
	Action string          `json:"action"`
	Fence  int64           `json:"fence"`
}
type GenerationObservation struct {
	Grant GenerationGrant `json:"grant"`
	Fence int64           `json:"fence"`
	State string          `json:"state"`
}
