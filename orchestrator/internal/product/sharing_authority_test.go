package product

import (
	"database/sql"
	"testing"
	"time"
)

func TestConversationGrantsRequireCurrentActionAndCanonicalRecipient(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	seedMessage(t, s, scope, "shared", "input", "searchable sample")
	recipients, err := s.ConversationRecipients(ctx, scope, "shared", "fixture-bob", "", 10)
	must(t, err)
	if len(recipients) != 1 || recipients[0].ID != "bob" || recipients[0].Username != "fixture-bob" {
		t.Fatal(recipients)
	}
	wantError(t, s.GrantConversation(ctx, scope, "shared", "fixture-bob"), ErrNotFound)
	wantError(t, s.GrantConversation(ctx, scope, "shared", scope.Actor), ErrNotFound)
	must(t, s.GrantConversation(ctx, scope, "shared", "bob"))
	fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionConversationsShare: PolicyDeny, ActionProjectsRead: PolicyDeny})
	wantError(t, s.GrantConversation(ctx, scope, "shared", "carol"), ErrForbidden)
	_, err = s.ConversationRecipients(ctx, scope, "shared", "", "", 10)
	wantError(t, err, ErrForbidden)
	readers, err := s.ConversationReaders(ctx, scope, "shared", "", 10)
	must(t, err)
	if len(readers) != 1 || readers[0].ID != "bob" {
		t.Fatal(readers)
	}
	must(t, s.CanReadConversation(ctx, "bob", "shared"))
	_, err = s.Project(ctx, Scope{Actor: "bob", ProjectID: scope.ProjectID})
	wantError(t, err, ErrNotFound)
	// Even an invalid self-grant row cannot turn reader authority into an owner
	// read bypass through direct display, search or the shared-reader listing.
	_, err = s.db.Exec(`INSERT INTO conversation_grants(session_id,reader_id) VALUES('shared',?)`, scope.Actor)
	must(t, err)
	wantError(t, s.CanReadConversation(ctx, scope.Actor, "shared"), ErrForbidden)
	_, err = s.SharedConversation(ctx, scope.Actor, "shared")
	wantError(t, err, ErrNotFound)
	results, err := s.Search(ctx, scope.Actor, "searchable", "", 10)
	must(t, err)
	if len(results) != 0 {
		t.Fatal("search bypassed owner denial", results)
	}
	must(t, s.RevokeConversation(ctx, scope, "shared", "bob"))
	wantError(t, s.CanReadConversation(ctx, "bob", "shared"), ErrNotFound)
}

func TestPublicShareGrantRevocationDoesNotRevokeExistingLink(t *testing.T) {
	files, scope, _ := fileFixture(t)
	s := files.Store
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	service := &Shares{Store: s, Files: files, Directory: t.TempDir()}
	share, token, err := service.Create(ctx, scope, "shared", nil, time.Now().Add(time.Hour))
	must(t, err)
	fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionPublicSharesCreate: PolicyDeny})
	_, _, err = service.Create(ctx, scope, "shared", nil, time.Now().Add(time.Hour))
	wantError(t, err, ErrForbidden)
	_, err = s.RotateShare(ctx, scope, share.ID)
	wantError(t, err, ErrForbidden)
	_, err = s.PublicShare(ctx, token)
	must(t, err)
	_, err = s.Shares(ctx, scope, "shared", "", 10)
	must(t, err)
	must(t, s.RevokeShare(ctx, scope, share.ID))
	_, err = s.PublicShare(ctx, token)
	wantError(t, err, ErrNotFound)
	// Suspension is different from functional grant revocation: public content
	// access must stop at the account-status boundary even before reconciliation.
	fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionPublicSharesCreate: PolicyAllow})
	_, token, err = service.Create(ctx, scope, "shared", nil, time.Now().Add(time.Hour))
	must(t, err)
	must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET status='disabled' WHERE id=?`, scope.Actor)
		return err
	}))
	_, err = s.PublicShare(ctx, token)
	wantError(t, err, ErrNotFound)
}
