package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogRevisionPaginationKeepsEveryRevision(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"alpha", "beta"} {
		for revision := int64(1); revision <= 2; revision++ {
			service := evolution.Service{Ref: evolution.Revision{ID: id, Revision: revision}, Kind: "evolution", ConnectionID: "private-connection", CredentialRef: "private-credential", CredentialGeneration: 1, RunnerDigest: evolution.Digest([]byte("runner")), Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
			service.Ref.Digest, _ = evolution.DefinitionDigest(service)
			raw, _ := json.Marshal(service)
			if err = s.PublishDefinition(ctx, "service", raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	request := CatalogListInput{Kind: "service", Limit: 1}
	seen := []evolution.Revision{}
	for {
		page, err := s.CatalogRevisions(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "private-") {
			t.Fatal("private metadata leaked")
		}
		seen = append(seen, page.Revisions...)
		if !page.HasMore {
			break
		}
		request.AfterID = page.NextID
		request.AfterRevision = page.NextRevision
	}
	if len(seen) != 4 || seen[0].ID != "alpha" || seen[1].Revision != 2 || seen[2].ID != "beta" {
		t.Fatal(seen)
	}
	request.Kind = "unknown"
	if _, err = s.CatalogRevisions(ctx, request); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
