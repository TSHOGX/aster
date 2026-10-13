package product

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func completedSharedHistory(t *testing.T, s *Store, scope Scope, session string) {
	t.Helper()
	seedMessage(t, s, scope, session, "question", "Historical capacity is 42 mAh.")
	var turn string
	must(t, s.reader.QueryRow(`SELECT turn_id FROM messages WHERE id='question'`).Scan(&turn))
	_, err := s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,sequence,created_at) VALUES('answer',?,?,?,'assistant','Recorded capacity: 42 mAh.',2,?)`, scope.ProjectID, session, turn, s.timestamp())
	must(t, err)
	item := DisplayItem{ID: "answer", TurnID: turn, Position: 2, Revision: 1, Kind: "assistant", Content: "Recorded capacity: 42 mAh.", MessageID: "answer", State: "complete"}
	raw, _ := json.Marshal(item)
	_, err = s.db.Exec(`INSERT INTO conversation_items(session_id,id,turn_id,position,block_index,item_revision,source_key,data,message_id) VALUES(?,'answer',?,2,0,1,'fixture/answer',?,'answer')`, session, turn, string(raw))
	must(t, err)
	summary := TurnSummary{ID: turn, Sequence: 1, State: "completed", FinalMessageID: "answer"}
	raw, _ = json.Marshal(summary)
	_, err = s.db.Exec(`INSERT OR REPLACE INTO conversation_turns(session_id,turn_id,sequence,revision,data) VALUES(?,?,1,1,?)`, session, turn, string(raw))
	must(t, err)
	_, err = s.db.Exec(`INSERT OR REPLACE INTO conversation_checkpoints(session_id,generation,state,updated_at,content_revision,index_revision,next_position) VALUES(?,'fixture','ready',?,1,1,2)`, session, s.timestamp())
	must(t, err)
	_, err = s.db.Exec(`DELETE FROM conversation_dirty_turns WHERE session_id=?`, session)
	must(t, err)
	_, err = s.db.Exec(`DELETE FROM conversation_pending_sessions WHERE session_id=?`, session)
	must(t, err)
}
func TestReceivedViewFilesAndRevocationBoundaries(t *testing.T) {
	files, scope, backend := fileFixture(t)
	s := files.Store
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	must(t, s.GrantConversation(ctx, scope, "shared", "bob"))
	sources, err := s.ReceivedSources(ctx, "bob", "", 50)
	must(t, err)
	if len(sources) != 1 || sources[0].ID != scope.Actor {
		t.Fatal(sources)
	}
	page, err := s.ConversationPage(ReceivedViewContext(ctx), Scope{Actor: "bob"}, "shared", ViewSelector{})
	must(t, err)
	if len(page.Items) != 2 || page.Head.Session.ProjectID != "" || page.Head.ActiveRun != nil || len(page.Head.Recovery) != 0 {
		t.Fatal(page)
	}
	_, err = s.ConversationPage(ctx, Scope{Actor: "bob", ProjectID: scope.ProjectID}, "shared", ViewSelector{})
	wantError(t, err, ErrNotFound)
	must(t, os.WriteFile(filepath.Join(backend.root, "report.txt"), []byte("first"), 0600))
	must(t, s.SetReaderAccess(ctx, scope, "shared", "bob", ReaderAccess{Revision: 1, Paths: []string{"report.txt"}}))
	_, err = files.CaptureReceived(ctx, "bob", "shared", "report.txt", true)
	wantError(t, err, ErrForbidden)
	read, err := files.CaptureReceived(ctx, "bob", "shared", "report.txt", false)
	must(t, err)
	data, err := io.ReadAll(read)
	read.Close()
	must(t, err)
	if string(data) != "first" {
		t.Fatal(string(data))
	}
	must(t, os.WriteFile(filepath.Join(backend.root, "report.txt"), []byte("second"), 0600))
	read, err = files.CaptureReceived(ctx, "bob", "shared", "report.txt", false)
	must(t, err)
	data, err = io.ReadAll(read)
	read.Close()
	must(t, err)
	if string(data) != "second" {
		t.Fatal(string(data))
	}
	_, err = files.CaptureReceived(ctx, "bob", "shared", "../report.txt", false)
	wantError(t, err, ErrNotFound)
	must(t, s.RevokeConversation(ctx, scope, "shared", "bob"))
	_, err = s.ConversationDetail(ReceivedViewContext(ctx), Scope{Actor: "bob"}, "shared", "answer", "1", "")
	wantError(t, err, ErrNotFound)
	_, err = files.CaptureReceived(ctx, "bob", "shared", "report.txt", false)
	wantError(t, err, ErrNotFound)
	sources, err = s.ReceivedSources(ctx, "bob", "", 50)
	must(t, err)
	if len(sources) != 0 {
		t.Fatal(sources)
	}
}
func TestPublicBundleAndIndependentCopy(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	service := &Shares{Store: s, Directory: t.TempDir()}
	share, token, err := service.Create(ctx, scope, "shared", nil, time.Now().Add(time.Hour))
	must(t, err)
	if share.AllowDownload {
		t.Fatal("public downloads defaulted on")
	}
	_, err = s.db.Exec(`UPDATE messages SET content='changed source' WHERE id='answer'`)
	must(t, err)
	page, err := s.BundlePage(ctx, token, ViewSelector{})
	must(t, err)
	if len(page.Items) != 2 || page.Items[1].Content != "Recorded capacity: 42 mAh." {
		t.Fatal(page)
	}
	input := CopyRequest{ID: "copy-request", Kind: "publicSnapshot", Token: token, ProjectID: "bob-copy", ProjectTitle: "My project", Title: "Independent"}
	copy, err := s.CreateConversationCopy(ctx, "bob", input)
	must(t, err)
	again, err := s.CreateConversationCopy(ctx, "bob", input)
	must(t, err)
	if copy.ID != again.ID || copy.SessionID != again.SessionID {
		t.Fatal(copy, again)
	}
	must(t, service.CopyStep(ctx))
	copy, err = s.ConversationCopy(ctx, "bob", input.ID)
	must(t, err)
	if copy.State != "complete" {
		t.Fatal(copy)
	}
	must(t, s.RevokeShare(ctx, scope, share.ID))
	own := Scope{Actor: "bob", ProjectID: copy.ProjectID}
	page, err = s.ConversationPage(ctx, own, copy.SessionID, ViewSelector{})
	must(t, err)
	if len(page.Items) != 2 || page.Items[1].Content != "Recorded capacity: 42 mAh." {
		t.Fatal(page)
	}
	native, err := s.ImportedNativeHistory(ctx, copy.SessionID, "/workspace")

	must(t, err)
	if !strings.Contains(string(native), "42 mAh") || strings.Contains(string(native), "private") || strings.Contains(string(native), "toolCall") {
		t.Fatal(string(native))
	}
	nativePath := filepath.Join(t.TempDir(), "import.jsonl")
	must(t, os.WriteFile(nativePath, native, 0600))
	if output, err := exec.Command("node", "../runtimerelease/history-import.test.mjs", nativePath).CombinedOutput(); err != nil {
		t.Fatalf("native imported continuation: %v\n%s", err, output)
	}
	must(t, s.RebuildConversation(ctx, own, copy.SessionID))
	must(t, s.ProjectConversation(ctx, copy.SessionID))
	page, err = s.ConversationPage(ctx, own, copy.SessionID, ViewSelector{})
	must(t, err)
	if len(page.Items) != 2 {
		t.Fatal(page)
	}
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM messages WHERE session_id=?`, copy.SessionID).Scan(&count))
	if count != 2 {
		t.Fatal(count)
	}
}
func TestCopyChecksSourceBeforeOwnershipCommit(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	must(t, s.GrantConversation(ctx, scope, "shared", "bob"))
	input := CopyRequest{ID: "pending-copy", Kind: "received", Session: "shared", ProjectID: "bob-target", ProjectTitle: "Target", Title: "Copy"}
	copy, err := s.CreateConversationCopy(ctx, "bob", input)
	must(t, err)
	must(t, s.RevokeConversation(ctx, scope, "shared", "bob"))
	service := &Shares{Store: s, Directory: t.TempDir()}
	wantError(t, service.CopyStep(ctx), ErrNotFound)
	_, err = s.Session(ctx, Scope{Actor: "bob", ProjectID: copy.ProjectID}, copy.SessionID)
	wantError(t, err, ErrNotFound)
	var committed bool
	must(t, s.reader.QueryRow(`SELECT committed FROM conversation_copies WHERE id=?`, copy.ID).Scan(&committed))
	if committed {
		t.Fatal("revoked copy committed")
	}
	// Every original Project mutation still requires ownership, including after
	// the request has acquired a shared read context.
	_, err = s.CreateSession(ReceivedViewContext(ctx), Scope{Actor: "bob", ProjectID: scope.ProjectID}, "unauthorized", "No")
	wantError(t, err, ErrNotFound)
}

func TestCopyFilesIntoExistingProjectAndKeepIndependentBytes(t *testing.T) {
	for _, mode := range []string{"active", "paused", "created"} {
		t.Run(mode, func(t *testing.T) {
			files, scope, backend := fileFixture(t)
			s := files.Store
			_, err := s.CreateSession(ctx, scope, "shared", "Shared")
			must(t, err)
			completedSharedHistory(t, s, scope, "shared")
			must(t, os.WriteFile(filepath.Join(backend.root, "report.txt"), []byte("original file"), 0600))
			must(t, s.GrantConversationAccess(ctx, scope, "shared", "bob", ReaderAccess{Paths: []string{"report.txt", "not-selected.txt"}, AllowDownload: true}))
			target := Scope{Actor: "bob", ProjectID: "bob-files"}
			_, err = s.CreateProject(ctx, "bob", target.ProjectID, "Target")
			must(t, err)
			if mode != "created" {
				must(t, files.Lifecycle.EnsureActive(ctx, target, files.Bindings))
				drive(t, files.Lifecycle, target.ProjectID)
			}
			if mode == "paused" {
				p, e := s.Project(ctx, target)
				must(t, e)
				must(t, files.Lifecycle.Pause(ctx, target, p.Revision))
				drive(t, files.Lifecycle, target.ProjectID)
			}
			input := CopyRequest{ID: "copy-files", Kind: "received", Session: "shared", ProjectID: target.ProjectID, Title: "Copy", Paths: []string{"report.txt"}}
			job, err := s.CreateConversationCopy(ctx, "bob", input)
			must(t, err)
			service := &Shares{Store: s, Files: files, Directory: t.TempDir()}
			must(t, service.CopyStep(ctx))
			job, err = s.ConversationCopy(ctx, "bob", job.ID)
			must(t, err)
			if job.State != "complete" {
				t.Fatal(job)
			}
			data, err := os.ReadFile(filepath.Join(backend.root, "Shared imports", job.ID, "report.txt"))
			must(t, err)
			if string(data) != "original file" {
				t.Fatal(string(data))
			}
			must(t, s.RevokeConversation(ctx, scope, "shared", "bob"))
			must(t, os.WriteFile(filepath.Join(backend.root, "report.txt"), []byte("source changed"), 0600))
			data, err = os.ReadFile(filepath.Join(backend.root, "Shared imports", job.ID, "report.txt"))
			must(t, err)
			if string(data) != "original file" {
				t.Fatal(string(data))
			}
			head, err := s.ConversationHead(ctx, target, job.SessionID)
			must(t, err)
			if head.Session.ImportedFiles["report.txt"] != "Shared imports/copy-files/report.txt" || head.Session.ImportedFiles["not-selected.txt"] != "" {
				t.Fatal(head.Session.ImportedFiles)
			}
			if mode != "active" {
				p, e := s.Project(ctx, target)
				must(t, e)
				if p.State != mode {
					t.Fatal("copy started compute", p.State)
				}
			}
		})
	}
}

func TestCopyRetryAfterCommitRetainsStagedFilesAcrossSourceRevocation(t *testing.T) {
	files, scope, backend := fileFixture(t)
	s := files.Store
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	must(t, os.WriteFile(filepath.Join(backend.root, "report.txt"), []byte("stable bytes"), 0600))
	must(t, s.GrantConversationAccess(ctx, scope, "shared", "bob", ReaderAccess{Paths: []string{"report.txt"}, AllowDownload: true}))
	target := Scope{Actor: "bob", ProjectID: "retry-target"}
	_, err = s.CreateProject(ctx, "bob", target.ProjectID, "Target")
	must(t, err)
	must(t, files.Lifecycle.EnsureActive(ctx, target, files.Bindings))
	drive(t, files.Lifecycle, target.ProjectID)
	job, err := s.CreateConversationCopy(ctx, "bob", CopyRequest{ID: "retry-copy", Kind: "received", Session: "shared", ProjectID: target.ProjectID, Title: "Retry", Paths: []string{"report.txt"}})
	must(t, err)
	request := writeRequest("Shared imports/retry-copy/report.txt", "stable bytes")
	raw, _ := json.Marshal(request)
	_, err = s.db.Exec(`INSERT INTO file_operations(id,project_id,request,store_id,sandbox_id,epoch,handle,state,created_at,updated_at,action,settled,error) VALUES(?,?,?,'store','sandbox',1,'failed','failed',?,?,'files.upload',1,'injected failure')`, copyIdentity(job.ID, "write/report.txt"), target.ProjectID, string(raw), s.timestamp(), s.timestamp())
	must(t, err)
	service := &Shares{Store: s, Files: files, Directory: t.TempDir()}
	if err = service.CopyStep(ctx); err == nil {
		t.Fatal("expected file failure")
	}
	job, err = s.ConversationCopy(ctx, "bob", job.ID)
	must(t, err)
	if !job.Committed || job.State != "failed" {
		t.Fatal(job)
	}
	must(t, s.RevokeConversation(ctx, scope, "shared", "bob"))
	must(t, os.Remove(filepath.Join(backend.root, "report.txt")))
	must(t, s.RetryConversationCopy(ctx, "bob", job.ID))
	// A new service instance models worker restart: all copy and operation state
	// must be recovered from the database and committed host blobs.
	restarted := &Shares{Store: s, Files: files, Directory: service.Directory}
	must(t, restarted.CopyStep(ctx))
	job, err = s.ConversationCopy(ctx, "bob", job.ID)
	must(t, err)
	if job.State != "complete" {
		t.Fatal(job)
	}
	data, err := os.ReadFile(filepath.Join(backend.root, "Shared imports", job.ID, "report.txt"))
	must(t, err)
	if string(data) != "stable bytes" {
		t.Fatal(string(data))
	}
}

func TestCopyExcludesActiveTurnAndReceivedCursorSurvivesRevocation(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	seedMessage(t, s, scope, "shared", "still-running", "DO NOT COPY THIS ACTIVE TURN")
	must(t, s.GrantConversation(ctx, scope, "shared", "bob"))
	_, err = s.CreateSession(ctx, scope, "other", "Other")
	must(t, err)
	must(t, s.GrantConversation(ctx, scope, "other", "bob"))
	first, err := s.ReceivedConversations(ctx, "bob", scope.Actor, "", 1)
	must(t, err)
	if len(first) != 1 {
		t.Fatal(first)
	}
	must(t, s.RevokeConversation(ctx, scope, first[0].SessionID, "bob"))
	next, err := s.ReceivedConversations(ctx, "bob", scope.Actor, first[0].Cursor, 1)
	must(t, err)
	if len(next) != 1 || next[0].SessionID == first[0].SessionID {
		t.Fatal(next)
	}
	must(t, s.GrantConversation(ctx, scope, "shared", "bob"))
	job, err := s.CreateConversationCopy(ctx, "bob", CopyRequest{ID: "boundary-copy", Kind: "received", Session: "shared", ProjectID: "boundary-target", ProjectTitle: "Target", Title: "Copy"})
	must(t, err)
	service := &Shares{Store: s, Directory: t.TempDir()}
	must(t, service.CopyStep(ctx))
	native, err := s.ImportedNativeHistory(ctx, job.SessionID, "/workspace")
	must(t, err)
	if strings.Contains(string(native), "DO NOT COPY") {
		t.Fatal("active turn crossed copy frontier")
	}
}

func TestCopyNeverOverwritesExistingDestination(t *testing.T) {
	files, scope, backend := fileFixture(t)
	s := files.Store
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	must(t, os.WriteFile(filepath.Join(backend.root, "report.txt"), []byte("shared"), 0600))
	must(t, s.GrantConversationAccess(ctx, scope, "shared", "bob", ReaderAccess{Paths: []string{"report.txt"}, AllowDownload: true}))
	target := Scope{Actor: "bob", ProjectID: "target"}
	_, err = s.CreateProject(ctx, "bob", target.ProjectID, "Target")
	must(t, err)
	must(t, files.Lifecycle.EnsureActive(ctx, target, files.Bindings))
	drive(t, files.Lifecycle, target.ProjectID)
	job, err := s.CreateConversationCopy(ctx, "bob", CopyRequest{ID: "existing-copy", Kind: "received", Session: "shared", ProjectID: target.ProjectID, Title: "Copy", Paths: []string{"report.txt"}})
	must(t, err)
	destination := filepath.Join(backend.root, "Shared imports", job.ID, "report.txt")
	must(t, os.MkdirAll(filepath.Dir(destination), 0700))
	must(t, os.WriteFile(destination, []byte("my existing content"), 0600))
	service := &Shares{Store: s, Files: files, Directory: t.TempDir()}
	wantError(t, service.CopyStep(ctx), ErrConflict)
	bytes, err := os.ReadFile(destination)
	must(t, err)
	if string(bytes) != "my existing content" {
		t.Fatal("copy overwrote an existing file")
	}
	job, err = s.ConversationCopy(ctx, "bob", job.ID)
	must(t, err)
	if job.State != "failed" {
		t.Fatal(job)
	}
}

func TestCompletedCopySurvivesCleanupFailure(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	completedSharedHistory(t, s, scope, "shared")
	must(t, s.GrantConversation(ctx, scope, "shared", "bob"))
	job, err := s.CreateConversationCopy(ctx, "bob", CopyRequest{ID: "cleanup-copy", Kind: "received", Session: "shared", ProjectID: "target", ProjectTitle: "Target", Title: "Copy"})
	must(t, err)
	root := t.TempDir()
	service := &Shares{Store: s, Directory: root}
	must(t, service.CopyStep(ctx))
	// A temporary invalid cleanup configuration cannot change the committed result.
	service.Directory = "relative-directory"
	wantError(t, service.CopyStep(ctx), ErrInvalid)
	job, err = s.ConversationCopy(ctx, "bob", job.ID)
	must(t, err)
	if job.State != "complete" {
		t.Fatal(job)
	}
	service.Directory = root
	must(t, service.CopyStep(ctx))
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM session_imports WHERE session_id=?`, job.SessionID).Scan(&count))
	if count != 1 {
		t.Fatal(count)
	}
}
