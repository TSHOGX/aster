package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func TestEvolutionCatalogImmutableRevisionsAndBindings(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	body := json.RawMessage(`{"schema_version":1,"goal":"lesson"}`)
	first, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "lesson", Revision: 1, Digest: evolution.Digest(body), Body: body, Enabled: true})
	must(t, err)
	if first.Revision != 1 {
		t.Fatal(first)
	}
	if _, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "lesson", Revision: 1, Digest: evolution.Digest(body), Body: body, Enabled: true}); err != ErrConflict {
		t.Fatal("revision rewrote", err)
	}
	if _, err := s.BindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template", "lesson", 1); err != nil {
		t.Fatal(err)
	}
	body = json.RawMessage(`{"schema_version":1,"goal":"revised lesson"}`)
	if _, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "lesson", Revision: 2, Digest: evolution.Digest(body), Body: body, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template", "lesson", 2); err != ErrConflict {
		t.Fatal("disabled catalog bound", err)
	}
	if err := s.UnbindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template"); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRejectsForgedDigestAndDuplicateFields(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	body := json.RawMessage(`{"goal":"one"}`)
	if _, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "bad", Revision: 1, Digest: evolution.Digest([]byte("different")), Body: body}); err != ErrInvalid {
		t.Fatal("forged digest accepted", err)
	}
	body = json.RawMessage(`{"goal":"one","goal":"two"}`)
	if _, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "duplicate", Revision: 1, Digest: evolution.Digest(body), Body: body}); err != ErrInvalid {
		t.Fatal("ambiguous body accepted", err)
	}
}

func TestCatalogReadAuthorityAndRetainedIntegrity(t *testing.T) {
	s, admin := accountStore(t)
	ctx := context.Background()
	token := adminTestToken(t, s, admin)
	body := json.RawMessage(`{"goal":"lesson"}`)
	_, err := s.PublishCatalogRevision(ctx, token, CatalogRevision{Kind: "template", ID: "lesson", Revision: 1, Digest: evolution.Digest(body), Body: body, Enabled: true})
	must(t, err)
	if _, err = s.CatalogRevision(ctx, "", "template", "lesson", 1); err == nil {
		t.Fatal("unauthenticated catalog read")
	}
	read, err := s.CatalogRevision(ctx, token, "template", "lesson", 1)
	must(t, err)
	if string(read.Body) != string(body) {
		t.Fatal("catalog bytes changed")
	}
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE evolution_catalog_revisions SET body='{"goal":"tampered"}' WHERE id='lesson'`)
		return err
	}))
	if _, err = s.CatalogRevision(ctx, token, "template", "lesson", 1); err != ErrInvalid {
		t.Fatal("corrupt definition read", err)
	}
	if _, err = s.BindCatalogRevision(ctx, token, StandardUsersGroup, "binding", "template", "lesson", 1); err != ErrInvalid {
		t.Fatal("corrupt definition bound", err)
	}
}
