package authoring

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func released(t *testing.T, s *Store) (Library, Version) {
	t.Helper()
	l := library(t, s)
	mutate(t, s, l.ID, fixtureMutation{RequestID: "file", ExpectedRevision: 1, Action: "text", Path: "file", Text: "portable bytes"})
	v := publish(t, s, l.ID, 2)
	if _, err := s.ValidateNext(context.Background(), allow); err != nil {
		t.Fatal(err)
	}
	v, err := s.Version(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	return l, v
}
func TestExportPinsMembershipArchiveAndRetirement(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l, v := released(t, s)
	grant := ServiceGrant{ClientID: "consumer", Revision: 1, Libraries: []string{l.ID}}
	input := ExportInput{RequestID: "export", VersionID: v.ID, DescriptorDigest: v.DescriptorDigest}
	e, err := s.Export(ctx, grant, input)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.Export(ctx, grant, input); err != nil || again.ID != e.ID {
		t.Fatal("export receipt changed", err)
	}
	if _, _, err := s.ExportBlob(ctx, grant, e.ID, digestBytes([]byte("foreign"))); !errors.Is(err, ErrNotFound) {
		t.Fatal("arbitrary blob read", err)
	}
	reader, _, err := s.ExportBlob(ctx, grant, e.ID, digestBytes([]byte("portable bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Operation(ctx, testActor, v.ID, OperationInput{RequestID: "archive", Action: "archive"}); err != nil {
		t.Fatal(err)
	}
	input.RequestID = "new-export"
	if _, err = s.Export(ctx, grant, input); !errors.Is(err, ErrConflict) {
		t.Fatal("archived source exported", err)
	}
	if _, err = s.RenewExport(ctx, grant, e.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("archived source renewed", err)
	}
	if _, err = s.Retire(ctx, testActor, v.ID, "retire"); !errors.Is(err, ErrBusy) {
		t.Fatal("live export ignored", err)
	}
	if err = s.CloseExport(ctx, grant, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ExportBlob(ctx, grant, e.ID, digestBytes([]byte("portable bytes"))); !errors.Is(err, ErrConflict) {
		t.Fatal("closed export admitted reader", err)
	}
	if _, err = s.Retire(ctx, testActor, v.ID, "retire"); !errors.Is(err, ErrBusy) {
		t.Fatal("active reader ignored", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "portable bytes" {
		t.Fatal("admitted read canceled by export close", err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Retire(ctx, testActor, v.ID, "retire"); err != nil {
		t.Fatal(err)
	}
	// Draft references independently retain the bytes even after source retirement.
	if n, err := s.Collect(ctx); err != nil || n != 0 {
		t.Fatal("draft bytes collected", n, err)
	}
	if changes, _, err := s.Diff(ctx, l.ID, "", 2); err != nil || len(changes) != 0 {
		t.Fatal("retirement erased base diff", changes, err)
	}
	mutate(t, s, l.ID, fixtureMutation{RequestID: "remove", ExpectedRevision: 2, Action: "remove", Path: "file"})
	if n, err := s.Collect(ctx); err != nil || n != 1 {
		t.Fatal("unreferenced source not collected", n, err)
	}
}
func TestExportExpiryAndServiceGrantRevocation(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	l, v := released(t, s)
	grant := ServiceGrant{ClientID: "consumer", Revision: 1, Libraries: []string{l.ID}}
	e, err := s.Export(ctx, grant, ExportInput{RequestID: "export", VersionID: v.ID, DescriptorDigest: v.DescriptorDigest})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []ServiceGrant{{ClientID: "different", Revision: 1, Libraries: grant.Libraries}, {ClientID: grant.ClientID, Revision: 2, Libraries: grant.Libraries}, {ClientID: grant.ClientID, Revision: 1, Libraries: []string{"other"}}} {
		if _, _, err := s.ExportBlob(ctx, bad, e.ID, digestBytes([]byte("portable bytes"))); !errors.Is(err, ErrNotFound) {
			t.Fatal("revoked grant accepted", err)
		}
	}
	reader, _, err := s.ExportBlob(ctx, grant, e.ID, digestBytes([]byte("portable bytes")))
	if err != nil {
		t.Fatal(err)
	}
	now := s.now().Add(ExportLifetime + time.Second)
	s.now = func() time.Time { return now }
	if _, _, err := s.ExportBlob(ctx, grant, e.ID, digestBytes([]byte("portable bytes"))); !errors.Is(err, ErrConflict) {
		t.Fatal("expired export admitted read", err)
	}
	if _, err := s.RenewExport(ctx, grant, e.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("expired export renewed", err)
	}
	if err = s.ExpireExports(ctx); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(reader); err != nil || len(b) == 0 {
		t.Fatal("admitted bounded read lost pin", err)
	}
	reader.Close()
}
func TestExportPinsReconcileOnlyAfterOwnerLoss(t *testing.T) {
	s, c := testStore(t)
	ctx := context.Background()
	l, v := released(t, s)
	grant := ServiceGrant{ClientID: "consumer", Revision: 1, Libraries: []string{l.ID}}
	e, err := s.Export(ctx, grant, ExportInput{RequestID: "export", VersionID: v.ID, DescriptorDigest: v.DescriptorDigest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO reader_pins VALUES('interrupted',?,?)", e.ID, digestBytes([]byte("portable bytes"))); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, c); err == nil {
		reopened.Close()
		t.Fatal("second owner reconciled live pins")
	}
	s.Close()
	s, err = Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err = s.db.QueryRow("SELECT count(*) FROM reader_pins").Scan(&n); err != nil || n != 0 {
		t.Fatal("old owner pin not reconciled", n, err)
	}
	if _, err = s.ExportStatus(ctx, grant, e.ID); err != nil {
		t.Fatal("export identity lost on restart", err)
	}
}
