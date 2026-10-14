package authoring

import (
	"context"
	"testing"
)

func TestPreparedReceiptRecoversPinAfterCrash(t *testing.T) {
	s, l, b, _ := reviewFixture(t)
	ctx := context.Background()
	cfg := s.config
	main, err := s.MainCommit(ctx, testActor, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := PrepareInput{RequestID: "recover-prepare", BranchID: b.ID, ExpectedHead: b.Head, ExpectedMain: main.CommitID, ExpectedPolicyRevision: 1}
	s.branchCrash = func(point string) error {
		if point == "prepare_after_intent" {
			return ErrBusy
		}
		return nil
	}
	original, err := s.PrepareMerge(ctx, testActor, l.ID, input)
	if err != ErrBusy || original.Identity == nil {
		t.Fatal("missing injected crash", original, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	replay, err := restored.PrepareMerge(ctx, testActor, l.ID, input)
	if err != nil || replay.Digest != original.Digest || replay.Identity.Result != original.Identity.Result {
		t.Fatal("prepared identity changed", replay, err)
	}
	repo, err := restored.historyRepository(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := repo.Head(ctx, "refs/prepared/"+original.Digest[7:])
	if err != nil || pin != original.Identity.Result.CommitID {
		t.Fatal("prepared pin not restored", err)
	}
	if err = repo.GC(ctx); err != nil {
		t.Fatal(err)
	}
	file, err := restored.IntegrationFile(ctx, testActor, l.ID, original.Digest, "result", "note")
	if err != nil || file.Text == nil || *file.Text != "candidate" {
		t.Fatal("prepared content lost", file, err)
	}
	var count int
	if err = restored.db.QueryRowContext(ctx, "SELECT count(*) FROM receipts WHERE request_id='recover-prepare'").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate preparation receipt", count, err)
	}
}
