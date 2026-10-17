package authoring

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"aster/contracts/evolution"
)

func TestBackupAndCollectionRetainConcurrentMachineExportAndReview(t *testing.T) {
	for iteration := 0; iteration < 4; iteration++ {
		t.Run(fmt.Sprint(iteration), func(t *testing.T) {
			ctx := context.Background()
			s, cfg, scope, patch, _, artifacts := resultFixture(t)
			l, version := released(t, s)
			accepted, err := s.MainCommit(ctx, testActor, l.ID)
			if err != nil {
				t.Fatal(err)
			}
			human := composeBranch(t, s, l.ID, accepted.CommitID, "pending human", map[string]string{"human.txt": "reviewed human work"})
			prepared, err := s.PrepareMerge(ctx, testActor, l.ID, PrepareInput{RequestID: "pending-human", BranchID: human.ID, ExpectedHead: human.Head, ExpectedMain: accepted.CommitID, ExpectedPolicyRevision: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Review(ctx, testActor, l.ID, ReviewInput{"human-approval", prepared.Digest, "approve", ""}); err != nil {
				t.Fatal(err)
			}
			grant := ServiceGrant{ClientID: "consumer", Revision: 1, Libraries: []string{l.ID}}
			export, err := s.Export(ctx, grant, ExportInput{RequestID: "reader-export", VersionID: version.ID, DescriptorDigest: version.DescriptorDigest})
			if err != nil {
				t.Fatal(err)
			}
			reader, _, err := s.ExportBlob(ctx, grant, export.ID, digestBytes([]byte("portable bytes")))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			s.mu.Lock()
			_, err = s.install(ctx, []byte("unreferenced garbage"))
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "backup")
			start := make(chan struct{})
			outcomes := make(chan error, 4)
			var machine evolution.RunResultReceipt
			go func() { <-start; var err error; machine, err = s.SubmitRunResult(ctx, scope, patch); outcomes <- err }()
			go func() { <-start; outcomes <- s.Backup(ctx, target) }()
			go func() { <-start; _, err := s.Collect(ctx); outcomes <- err }()
			go func() {
				<-start
				raw, err := io.ReadAll(reader)
				if err == nil && string(raw) != "portable bytes" {
					err = fmt.Errorf("export reader changed bytes")
				}
				outcomes <- err
			}()
			close(start)
			for n := 0; n < 4; n++ {
				if err := <-outcomes; err != nil {
					t.Fatal(err)
				}
			}
			if machine.BranchID == "" {
				t.Fatal("machine intake did not settle")
			}
			cfg.Directory = target
			restored, err := Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if _, err = restored.Collect(ctx); err != nil {
				t.Fatal(err)
			}
			main, err := restored.MainCommit(ctx, testActor, l.ID)
			if err != nil || main != accepted {
				t.Fatal("snapshot advanced accepted main", main, err)
			}
			exported, _, err := restored.ExportBlob(ctx, grant, export.ID, digestBytes([]byte("portable bytes")))
			if err != nil {
				t.Fatal("restored export lost pinned bytes", err)
			}
			raw, err := io.ReadAll(exported)
			exported.Close()
			if err != nil || string(raw) != "portable bytes" {
				t.Fatal("restored export changed bytes", err)
			}
			// The snapshot may precede or follow the atomic machine intake. Replaying
			// its exact assignment must converge to one branch with immutable lineage.
			replay, err := restored.SubmitRunResult(ctx, scope, patch)
			if err != nil || replay.State != "submitted" {
				t.Fatal(replay, err)
			}
			again, err := restored.SubmitRunResult(ctx, scope, patch)
			if err != nil || again != replay {
				t.Fatal("restored intake duplicated result", again, err)
			}
			file, err := restored.BranchHeadFile(ctx, testActor, l.ID, replay.BranchID, replay.CommitID, "lessons/note.md")
			if err != nil || file.Text == nil || *file.Text != string(artifacts.data["file"]) {
				t.Fatal("machine bytes lost", file, err)
			}
			view, err := restored.Integration(ctx, testActor, l.ID, prepared.Digest)
			if err != nil || len(view.Reviews) != 1 {
				t.Fatal("prepared review lost", view, err)
			}
			restored.config.Authority = reviewRevokedAuthority{}
			if _, err = restored.Merge(ctx, testActor, l.ID, MergeInput{"revoked-after-restore", prepared.Digest, accepted.CommitID}); !errors.Is(err, ErrAuthority) {
				t.Fatal("backup revived revoked reviewer", err)
			}
			restored.config.Authority = cfg.Authority
			// Accepting the recovered machine candidate advances main and makes the
			// restored human approval stale; it cannot overwrite the accepted machine.
			machineBranch, err := restored.Branch(ctx, testActor, l.ID, replay.BranchID)
			if err != nil {
				t.Fatal(err)
			}
			machineMerge := approvedIntegration(t, restored, l, machineBranch)
			merged, err := restored.Merge(ctx, testActor, l.ID, machineMerge)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = restored.Merge(ctx, testActor, l.ID, MergeInput{"stale-after-restore", prepared.Digest, accepted.CommitID}); !errors.Is(err, ErrConflict) {
				t.Fatal("restored stale approval advanced main", err)
			}
			final, err := restored.MainCommit(ctx, testActor, l.ID)
			if err != nil || final.CommitID != merged.AcceptedCommit {
				t.Fatal("stale review changed main", final, err)
			}
			original, err := s.MainCommit(ctx, testActor, l.ID)
			if err != nil || original != accepted {
				t.Fatal("restore shared writable history", original, err)
			}
		})
	}
}
