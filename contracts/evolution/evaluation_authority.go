package evolution

import (
	"encoding/json"
	"fmt"
	"strings"

	manifest "aster/contracts/volumemanifest"
)

func BindingPolicyID(group, binding string) string {
	return "binding-" + strings.TrimPrefix(Digest([]byte(group+"\x00"+binding)), "sha256:")
}

// VolumePathPolicyID identifies Studio-owned policy independently of any writer.
// An empty path selects the whole Volume; resolved checks still name actual paths.
func VolumePathPolicyID(group, library, path string) string {
	return "volume-policy-" + strings.TrimPrefix(Digest([]byte(group+"\x00"+library+"\x00"+path)), "sha256:")
}

// EvaluationAssignment is a private cross-service authorization request. Studio
// supplies the verified preparation; the central authority checks current grants.
type EvaluationAssignment struct {
	JobID             string            `json:"job_id"`
	GroupID           string            `json:"group_id"`
	Requester         Principal         `json:"requester"`
	Prepared          PreparedIdentity  `json:"prepared"`
	RequirementDigest string            `json:"requirement_digest"`
	Profile           EvaluationProfile `json:"profile"`
	Budget            Budget            `json:"budget"`
	Seed              int64             `json:"seed"`
}

func (a EvaluationAssignment) Subject() (EvaluationSubject, CheckRequirement, error) {
	var subject EvaluationSubject
	var requirement CheckRequirement
	if !manifest.ValidID(a.JobID) || !manifest.ValidID(a.GroupID) || !manifest.ValidID(a.Requester.Subject) || a.Requester.Kind != "human" || a.Requester.Issuer == "" || a.Budget.Validate() != nil {
		return subject, requirement, fmt.Errorf("invalid evaluation assignment")
	}
	prepared, err := a.Prepared.ApprovalDigest()
	if err != nil {
		return subject, requirement, err
	}
	found := false
	for _, check := range a.Prepared.Checks {
		if check.Digest == a.RequirementDigest {
			requirement = check
			found = true
			break
		}
	}
	if !found || requirement.Profile != a.Profile.Ref || a.Profile.Dataset.GroupID != a.GroupID {
		return subject, requirement, fmt.Errorf("evaluation requirement/profile mismatch")
	}
	subject = EvaluationSubject{Candidate: a.Prepared.Result, PreparedDigest: prepared, RequirementDigest: requirement.Digest, Reference: requirement.Reference, Profile: a.Profile.Ref, Dataset: a.Profile.Dataset, Scorer: a.Profile.Scorer, Runtime: a.Profile.Runtime, Model: a.Profile.Model, Tools: a.Profile.Tools, Seed: a.Seed}
	return subject, requirement, subject.MatchesProfile(a.Profile)
}
func (a EvaluationAssignment) Digest() (string, error) {
	if _, _, err := a.Subject(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	if len(raw) > MaxDocumentBytes {
		return "", fmt.Errorf("evaluation assignment too large")
	}
	return Digest(raw), nil
}

type EvaluationGrant struct {
	ID               string   `json:"id"`
	JobID            string   `json:"job_id"`
	GroupID          string   `json:"group_id"`
	AssignmentDigest string   `json:"assignment_digest"`
	SubjectDigest    string   `json:"subject_digest"`
	Service          Revision `json:"service"`
}
type EvaluationGrantScope struct {
	Grant  EvaluationGrant `json:"grant"`
	Action string          `json:"action"`
}

func (g EvaluationGrant) Validate() error {
	if !manifest.ValidID(g.ID) || !manifest.ValidID(g.JobID) || !manifest.ValidID(g.GroupID) || !manifest.ValidDigest(g.AssignmentDigest) || !manifest.ValidDigest(g.SubjectDigest) || g.Service.Validate() != nil {
		return fmt.Errorf("invalid evaluation grant")
	}
	return nil
}

type EvaluationProfileScope struct {
	GroupID   string    `json:"group_id"`
	Requester Principal `json:"requester"`
	LibraryID string    `json:"library_id"`
	Profile   Revision  `json:"profile"`
}
