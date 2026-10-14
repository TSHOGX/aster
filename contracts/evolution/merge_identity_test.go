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
