package evolution

import (
	"strings"
	"testing"
)

func preparedFixture() PreparedIdentity {
	ref := func(c string) ContentRef {
		return ContentRef{LibraryID: "library", CommitID: strings.Repeat(c, 64), Profile: "authored-v1", TreeDigest: Digest([]byte(c))}
	}
	return PreparedIdentity{LibraryID: "library", Main: ref("a"), Sources: []ContentRef{ref("b")}, Result: ref("c"), PolicyRevision: 1, PolicyDigest: Digest([]byte("policy")), Checks: []CheckRequirement{}}
}
func TestApprovalBindsIntegratedIdentity(t *testing.T) {
	p := preparedFixture()
	original, err := p.ApprovalDigest()
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*PreparedIdentity){"main": func(p *PreparedIdentity) { p.Main.CommitID = strings.Repeat("d", 64) }, "source": func(p *PreparedIdentity) { p.Sources[0].CommitID = strings.Repeat("d", 64) }, "tree": func(p *PreparedIdentity) { p.Result.TreeDigest = Digest([]byte("changed")) }, "policy": func(p *PreparedIdentity) { p.PolicyRevision++ }} {
		t.Run(name, func(t *testing.T) {
			q := preparedFixture()
			change(&q)
			digest, err := q.ApprovalDigest()
			if err != nil || digest == original {
				t.Fatal("approval identity not invalidated", err)
			}
		})
	}
	p.Sources[0].LibraryID = "other"
	if _, err := p.ApprovalDigest(); err == nil {
		t.Fatal("cross-library parent accepted")
	}
}

func TestPartialSelectionBindsCandidateAncestryAndSourceProvenance(t *testing.T) {
	p := preparedFixture()
	candidate := strings.Repeat("d", 64)
	p.Selections = []PreparedSelection{{BranchID: "source", Head: p.Sources[0].CommitID, CandidateCommit: candidate, SelectedPaths: []string{"selected"}, DeferredPaths: []string{"deferred"}}}
	digest, err := p.ApprovalDigest()
	if err != nil {
		t.Fatal(err)
	}
	parents := p.ParentCommits()
	if len(parents) != 2 || parents[0] != p.Main.CommitID || parents[1] != candidate {
		t.Fatal("full source was used as parent", parents)
	}
	q := p
	q.Selections = append([]PreparedSelection{}, p.Selections...)
	q.Selections[0].CandidateCommit = strings.Repeat("e", 64)
	changed, err := q.ApprovalDigest()
	if err != nil || changed == digest {
		t.Fatal("candidate not bound to approval", err)
	}
	for _, candidate := range []string{"", p.Main.CommitID, p.Sources[0].CommitID} {
		q.Selections[0].CandidateCommit = candidate
		if _, err := q.ApprovalDigest(); err == nil {
			t.Fatal("invalid partial candidate accepted", candidate)
		}
	}
	q.Selections[0].CandidateCommit = p.Sources[0].CommitID
	q.Selections[0].DeferredPaths = nil
	if _, err := q.ApprovalDigest(); err != nil {
		t.Fatal("full selection should retain source ancestry", err)
	}
	if q.ParentCommits()[1] != p.Sources[0].CommitID {
		t.Fatal("full source ancestry lost")
	}
}
