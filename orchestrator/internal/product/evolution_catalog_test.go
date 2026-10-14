package product

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEvolutionCatalogImmutableRevisionsAndBindings(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	body := json.RawMessage(`{"schema_version":1,"goal":"lesson"}`)
	first, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "lesson", Revision: 1, Digest: "digest-one", Body: body, Enabled: true})
	must(t, err)
	if first.Revision != 1 {
		t.Fatal(first)
	}
	if _, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "lesson", Revision: 1, Digest: "digest-other", Body: body, Enabled: true}); err != ErrConflict {
		t.Fatal("revision rewrote", err)
	}
	if _, err := s.BindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template", "lesson", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "lesson", Revision: 2, Digest: "digest-two", Body: body, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template", "lesson", 2); err != ErrConflict {
		t.Fatal("disabled catalog bound", err)
	}
	if err := s.UnbindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template"); err != nil {
		t.Fatal(err)
	}
}
