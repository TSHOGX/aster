package evolution

import (
	"encoding/json"
	"errors"
	"fmt"

	manifest "aster/contracts/volumemanifest"
)

// RunResultScope is verified by the authoritative run service. A submitted patch
// or a service credential alone cannot grant writes to a Volume or prefix.
type RunResultScope struct {
	GroupID     string        `json:"group_id"`
	RunID       string        `json:"run_id"`
	BindingID   string        `json:"binding_id"`
	Fence       int64         `json:"fence"`
	Base        ContentRef    `json:"base"`
	Output      OutputBinding `json:"output"`
	PatchDigest string        `json:"patch_digest"`
}

func (s RunResultScope) Validate() error {
	if !manifest.ValidID(s.GroupID) || !manifest.ValidID(s.RunID) || !manifest.ValidID(s.BindingID) || s.Fence < 1 || s.Base.Validate() != nil || s.Output.Validate() != nil || s.Base.LibraryID != s.Output.LibraryID || s.Base.Profile != s.Output.Profile || !manifest.ValidDigest(s.PatchDigest) {
		return fmt.Errorf("invalid run result scope")
	}
	return nil
}
func (s RunResultScope) Matches(p Patch) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if p.RunID != s.RunID || p.Fence != s.Fence || p.Base != s.Base || p.Validate(s.Output) != nil {
		return fmt.Errorf("run result identity mismatch")
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxDocumentBytes || Digest(raw) != s.PatchDigest {
		return fmt.Errorf("run result patch digest mismatch")
	}
	for _, entry := range p.Entries {
		if entry.Artifact != nil && entry.Artifact.GroupID != s.GroupID {
			return fmt.Errorf("run result artifact group mismatch")
		}
	}
	return nil
}

type RunResultReceipt struct {
	RunID       string `json:"run_id"`
	PatchDigest string `json:"patch_digest"`
	State       string `json:"state"`
	BranchID    string `json:"branch_id"`
	CommitID    string `json:"commit_id"`
}

func (r RunResultReceipt) Validate(scope RunResultScope) error {
	if r.RunID != scope.RunID || r.PatchDigest != scope.PatchDigest {
		return fmt.Errorf("run result receipt mismatch")
	}
	switch r.State {
	case "submitted":
		if !manifest.ValidID(r.BranchID) || !ObjectID(r.CommitID) {
			return fmt.Errorf("invalid accepted result branch")
		}
	case "no_change":
		if r.BranchID != "" || r.CommitID != scope.Base.CommitID {
			return fmt.Errorf("invalid unchanged result")
		}
	default:
		return fmt.Errorf("result acceptance incomplete")
	}
	return nil
}

var ErrRunResultNotFound = errors.New("Studio_result_not_found")

type AcceptedRunResult struct {
	RunID       string
	PatchDigest string
	BranchID    string
	CommitID    string
}
