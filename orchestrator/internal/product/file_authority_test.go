package product

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/projectfiles"
)

func fixtureFileGrants(t *testing.T, s *Store, grants map[ProductAction]PolicyEffect) {
	t.Helper()
	must(t, s.writeIdentity(ctx, func(tx *sql.Tx) error {
		for action, effect := range grants {
			if _, err := tx.ExecContext(ctx, `UPDATE permission_policies SET statements=json_set(statements,?,?) WHERE id='fixture-policy'`, `$."`+string(action)+`"`, effect); err != nil {
				return err
			}
		}
		return nil
	}))
}
func TestFileFeatureActionsAreIndependent(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "paused"}[paused], func(t *testing.T) {
			f, scope, b := fileFixture(t)
			s := f.Store
			if paused {
				p, err := s.Project(ctx, scope)
				must(t, err)
				must(t, f.Lifecycle.Pause(ctx, scope, p.Revision))
				drive(t, f.Lifecycle, scope.ProjectID)
			}
			must(t, os.WriteFile(filepath.Join(b.root, "file.txt"), []byte("preview bytes"), 0600))
			fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionFilesUpload: PolicyDeny, ActionFilesManage: PolicyDeny, ActionFilesExport: PolicyDeny})
			capture, err := f.Capture(ctx, scope, "file.txt")
			must(t, err)
			bytes, err := io.ReadAll(capture)
			must(t, err)
			must(t, capture.Close())
			if string(bytes) != "preview bytes" {
				t.Fatal(string(bytes))
			}
			_, err = f.Download(ctx, scope, "file.txt")
			wantError(t, err, ErrForbidden)
			_, err = f.Upload(ctx, scope, "upload", writeRequest("file.txt", "upload"), strings.NewReader("upload"))
			wantError(t, err, ErrForbidden)
			_, err = f.Manage(ctx, scope, "remove", projectfiles.Request{Operation: "remove", Path: "file.txt"})
			wantError(t, err, ErrForbidden)
			edit, err := f.Edit(ctx, scope, "edit", writeRequest("file.txt", "edited"), strings.NewReader("edited"))
			must(t, err)
			if edit.Action != ActionFilesEdit {
				t.Fatal(edit)
			}
			_, err = f.Manage(ctx, scope, "forged", writeRequest("file.txt", "wrong action"))
			wantError(t, err, ErrInvalid)
			fixtureFileGrants(t, s, map[ProductAction]PolicyEffect{ActionFilesUpload: PolicyAllow, ActionFilesEdit: PolicyDeny, ActionFilesManage: PolicyAllow, ActionFilesExport: PolicyAllow, ActionFilesRead: PolicyDeny, ActionFilesList: PolicyDeny})
			_, err = f.Edit(ctx, scope, "edit", writeRequest("file.txt", "edited"), strings.NewReader("edited"))
			wantError(t, err, ErrForbidden)
			// The same mutation ID cannot cross the upload/editor boundary on replay.
			_, err = f.Upload(ctx, scope, "edit", writeRequest("file.txt", "edited"), strings.NewReader("edited"))
			wantError(t, err, ErrConflict)
			_, err = f.Capture(ctx, scope, "file.txt")
			wantError(t, err, ErrForbidden)
			_, err = f.Download(ctx, scope, "file.txt")
			wantError(t, err, ErrForbidden)
			_, err = f.List(ctx, scope, projectfiles.DirectoryQuery{Limit: 10})
			wantError(t, err, ErrForbidden)
			_, err = f.Upload(ctx, scope, "upload", writeRequest("file.txt", "uploaded"), strings.NewReader("uploaded"))
			must(t, err)
			_, err = f.Manage(ctx, scope, "remove", projectfiles.Request{Operation: "remove", Path: "file.txt"})
			must(t, err)
			_, err = f.Capture(ctx, Scope{Actor: "bob", ProjectID: scope.ProjectID}, "file.txt")
			wantError(t, err, ErrNotFound)
			outcome, err := s.FileOperation(ctx, scope, "edit")
			must(t, err)
			if outcome.Action != ActionFilesEdit || outcome.State != "succeeded" {
				t.Fatal(outcome)
			}
		})
	}
}
func TestCachedPreviewRechecksReadButReleaseRetainsAuthority(t *testing.T) {
	f, scope, b := fileFixture(t)
	must(t, os.WriteFile(filepath.Join(b.root, "file.txt"), []byte("bytes"), 0600))
	snapshots := f.PreviewSnapshots()
	preview, err := snapshots.Open(ctx, scope, "file.txt", 1024)
	must(t, err)
	fixtureFileGrants(t, f.Store, map[ProductAction]PolicyEffect{ActionFilesRead: PolicyDeny})
	_, _, _, err = snapshots.Acquire(ctx, scope, preview.ID)
	wantError(t, err, ErrForbidden)
	_, err = snapshots.Open(ctx, scope, "file.txt", 1024)
	wantError(t, err, ErrForbidden)
	must(t, snapshots.Release(ctx, scope, preview.ID))
}
