package product

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedMessage(t *testing.T, s *Store, scope Scope, session, request, text string) {
	t.Helper()
	turn, err := s.AdmitTurn(ctx, scope, session, request, text, false)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO messages(id,project_id,session_id,turn_id,role,content,native_message,sequence,created_at) VALUES(?,?,?,?,?,?,?,(SELECT coalesce(max(sequence),0)+1 FROM messages WHERE session_id=?),?)`, request, scope.ProjectID, session, turn.ID, "user", text, `{"private":"native"}`, session, s.timestamp())
	must(t, err)
	// Read-model fixtures supply their projection explicitly. Production never
	// promotes uncorrelated message rows to consumed input evidence.
	var position int64
	must(t, s.reader.QueryRow(`SELECT sequence FROM messages WHERE id=?`, request).Scan(&position))
	item := DisplayItem{ID: request, TurnID: turn.ID, SegmentID: "fixture/" + turn.ID, Position: position, Kind: "user", Content: text, MessageID: request, State: "consumed", Revision: 1}
	raw, err := json.Marshal(item)
	must(t, err)
	_, err = s.db.Exec(`INSERT INTO conversation_items(session_id,id,turn_id,position,block_index,item_revision,source_key,data,message_id) VALUES(?,?,?,?,0,1,?,?,?)`, session, request, turn.ID, position, "fixture/"+request, string(raw), request)
	must(t, err)
}
func TestFrozenSharesFrontierFilesRotationAndCleanup(t *testing.T) {
	files, scope, backend := fileFixture(t)
	s := files.Store
	_, err := s.CreateSession(ctx, scope, "shared", "Conversation")
	must(t, err)
	seedMessage(t, s, scope, "shared", "before", "before publication")
	must(t, os.WriteFile(filepath.Join(backend.root, "selected"), []byte("frozen bytes"), 0600))
	service := &Shares{Store: s, Files: files, Directory: t.TempDir()}
	share, token, err := service.Create(ctx, scope, "shared", []string{"selected"}, time.Now().Add(time.Hour))
	must(t, err)
	if len(token) != 43 || share.State != "published" {
		t.Fatal(share, token)
	}
	must(t, os.WriteFile(filepath.Join(backend.root, "selected"), []byte("later workspace edit"), 0600))
	seedMessage(t, s, scope, "shared", "after", "after publication")
	messages, err := s.PublicMessages(ctx, token, 0, 100)
	must(t, err)
	if len(messages) != 1 || messages[0].Content != "before publication" || messages[0].Native != nil || messages[0].ProjectID != "" {
		t.Fatal(messages)
	}
	file, meta, err := service.OpenFile(ctx, token, "selected")
	must(t, err)
	data, err := io.ReadAll(file)
	file.Close()
	must(t, err)
	if string(data) != "frozen bytes" || meta.Size != 12 {
		t.Fatal(string(data), meta)
	}
	_, _, err = service.OpenFile(ctx, token, "../selected")
	wantError(t, err, ErrNotFound)
	_, err = s.Project(ctx, Scope{Actor: "reader", ProjectID: scope.ProjectID})
	wantError(t, err, ErrNotFound)
	successor, err := s.RotateShare(ctx, scope, share.ID)
	must(t, err)
	_, err = s.PublicShare(ctx, token)
	wantError(t, err, ErrNotFound)
	_, err = s.PublicShare(ctx, successor)
	must(t, err)
	must(t, s.RevokeShare(ctx, scope, share.ID))
	_, err = s.PublicShare(ctx, successor)
	wantError(t, err, ErrNotFound)
	must(t, service.Reconcile(ctx, 32))
	if _, err = os.Stat(filepath.Join(service.Directory, share.ID)); !os.IsNotExist(err) {
		t.Fatal("owned frozen bytes retained", err)
	}
}
func TestShareParentDeletionExpiryAndInterruptedPublication(t *testing.T) {
	files, scope, _ := fileFixture(t)
	s := files.Store
	_, err := s.CreateSession(ctx, scope, "shared", "Shared")
	must(t, err)
	service := &Shares{Store: s, Files: files, Directory: t.TempDir()}
	share, token, err := service.Create(ctx, scope, "shared", nil, time.Now().Add(time.Hour))
	must(t, err)
	child, err := s.Session(ctx, scope, "shared")
	must(t, err)
	must(t, s.DeleteSession(ctx, scope, child.ID, child.Revision))
	_, err = s.PublicShare(ctx, token)
	wantError(t, err, ErrNotFound)
	must(t, service.Reconcile(ctx, 32))
	if _, err = os.Stat(filepath.Join(service.Directory, share.ID)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	_, err = s.CreateSession(ctx, scope, "next", "Next")
	must(t, err)
	share, token, err = service.Create(ctx, scope, "next", nil, time.Now().Add(time.Hour))
	must(t, err)
	clock := s.now()
	s.now = func() time.Time { return clock.Add(2 * time.Hour) }
	_, err = s.PublicShare(ctx, token)
	wantError(t, err, ErrNotFound)
	must(t, service.Reconcile(ctx, 32))
	// A fresh application owner sees durable pending intent and reclaims it.
	_, err = s.db.Exec(`UPDATE shares SET state='pending',cleaned=0 WHERE id=?`, share.ID)
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(service.Directory, share.ID), 0700))
	must(t, os.WriteFile(filepath.Join(service.Directory, share.ID, "partial"), []byte("partial"), 0600))
	restarted := &Shares{Store: s, Files: files, Directory: service.Directory}
	must(t, restarted.Reconcile(ctx, 32))
	if _, err = os.Stat(filepath.Join(service.Directory, share.ID)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

type racingTitle struct {
	s       *Store
	scope   Scope
	session string
}

func (g racingTitle) GenerateTitle(ctx context.Context, _ string) (string, error) {
	child, err := g.s.Session(ctx, g.scope, g.session)
	if err != nil {
		return "", err
	}
	if err = g.s.RenameSession(ctx, g.scope, g.session, child.Revision, "User title"); err != nil {
		return "", err
	}
	return "Late generated title", nil
}
func TestTitlesSearchAndConversationOnlyPermission(t *testing.T) {
	s, a, b := fixture(t)
	_, err := s.CreateSession(ctx, a, "s", "")
	must(t, err)
	seedMessage(t, s, a, "s", "m", "battery impedance diagnosis")
	count, err := s.GenerateTitles(ctx, racingTitle{s, a, "s"}, 32)
	must(t, err)
	if count != 0 {
		t.Fatal("manual rename overwritten")
	}
	child, err := s.Session(ctx, a, "s")
	must(t, err)
	if child.Title != "User title" {
		t.Fatal(child)
	}
	_, err = s.CreateSession(ctx, a, "auto", "")
	must(t, err)
	_, err = s.AdmitTurn(ctx, a, "auto", "first", "  First  useful\n prompt  ", false)
	must(t, err)
	count, err = s.GenerateTitles(ctx, nil, 32)
	must(t, err)
	if count != 1 {
		t.Fatal(count)
	}
	child, err = s.Session(ctx, a, "auto")
	must(t, err)
	if child.Title != "First useful prompt" {
		t.Fatal(child)
	}
	results, err := s.Search(ctx, b.Actor, "impedance", "", 50)
	must(t, err)
	if len(results) != 0 {
		t.Fatal(results)
	}
	must(t, s.GrantConversation(ctx, a, "s", b.Actor))
	results, err = s.Search(ctx, b.Actor, "impedance", "", 50)
	must(t, err)
	if len(results) != 1 || results[0].ProjectID != a.ProjectID || results[0].SessionID != "s" || results[0].MessageID != "m" {
		t.Fatal(results)
	}
	messages, err := s.ConversationMessages(ctx, b.Actor, "s", 0, 100)
	must(t, err)
	if len(messages) != 1 || messages[0].Native != nil {
		t.Fatal(messages)
	}
	_, err = s.Events(ctx, Scope{b.Actor, a.ProjectID}, 0, 100)
	wantError(t, err, ErrNotFound)
	must(t, s.RevokeConversation(ctx, a, "s", b.Actor))
	_, err = s.ConversationMessages(ctx, b.Actor, "s", 0, 100)
	wantError(t, err, ErrNotFound)
	results, err = s.Search(ctx, b.Actor, "impedance", "", 50)
	must(t, err)
	if len(results) != 0 {
		t.Fatal(results)
	}
}
func TestNotificationAudienceAndDurableReads(t *testing.T) {
	s, _, _ := fixture(t)
	must(t, s.Notify(ctx, "", "Broadcast", "All users"))
	must(t, s.Notify(ctx, "alice", "Directed", "Only alice"))
	alice, err := s.Notifications(ctx, "alice", "", 100)
	must(t, err)
	bob, err := s.Notifications(ctx, "bob", "", 100)
	must(t, err)
	if len(alice) != 2 || len(bob) != 1 {
		t.Fatal(alice, bob)
	}
	for _, n := range alice {
		if n.Title == "Directed" {
			wantError(t, s.ReadNotification(ctx, "bob", n.ID), ErrNotFound)
		}
		must(t, s.ReadNotification(ctx, "alice", n.ID))
	}
	alice, err = s.Notifications(ctx, "alice", "", 100)
	must(t, err)
	for _, n := range alice {
		if !n.Read {
			t.Fatal(n)
		}
	}
	bob, err = s.Notifications(ctx, "bob", "", 100)
	must(t, err)
	if bob[0].Read {
		t.Fatal("broadcast read leaked across actors")
	}
	// Arbitrary FTS syntax is treated as quoted text, not an executable query.
	_, err = s.Search(ctx, "alice", strings.Repeat(`"`, 10), "", 50)
	must(t, err)
}

func TestNotificationPagingKeepsNewestFirstAndCountsAudience(t *testing.T) {
	s, _, _ := fixture(t)
	stamp := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return stamp }
	for _, title := range []string{"First", "Second", "Third"} {
		must(t, s.Notify(ctx, "alice", title, "body"))
		stamp = stamp.Add(time.Second)
	}
	must(t, s.Notify(ctx, "bob", "Private", "body"))
	first, err := s.Notifications(ctx, "alice", "", 2)
	must(t, err)
	if len(first) != 2 || first[0].Title != "Third" || first[1].Title != "Second" {
		t.Fatal(first)
	}
	rest, err := s.Notifications(ctx, "alice", first[1].ID, 2)
	must(t, err)
	if len(rest) != 1 || rest[0].Title != "First" {
		t.Fatal(rest)
	}
	must(t, s.ReadNotification(ctx, "alice", first[0].ID))
	count, err := s.UnreadNotifications(ctx, "alice")
	must(t, err)
	if count != 2 {
		t.Fatal(count)
	}
}

func TestNotificationTracksResolvedNativeOutcome(t *testing.T) {
	s, scope, _, command := agentFixture(t)
	must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{AgentRunOutcome: true, ConversationShared: true, OperatorDirected: true, OperatorBroadcast: true}))
	_, err := s.db.Exec(`UPDATE execution_runs SET state='unknown' WHERE id=?`, command.CommandID)
	must(t, err)
	notices, err := s.Notifications(ctx, scope.Actor, "", 100)
	must(t, err)
	if len(notices) != 1 || notices[0].Title != "Agent turn unknown" {
		t.Fatal(notices)
	}
	must(t, s.ReadNotification(ctx, scope.Actor, notices[0].ID))
	_, err = s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE id=?`, command.CommandID)
	must(t, err)
	notices, err = s.Notifications(ctx, scope.Actor, "", 100)
	must(t, err)
	if len(notices) != 1 || notices[0].Title != "Agent turn completed" || notices[0].Read {
		t.Fatal("resolved evidence left a stale notification", notices)
	}
	must(t, s.ReadNotification(ctx, scope.Actor, notices[0].ID))
	must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{}))
	_, err = s.db.Exec(`UPDATE execution_runs SET state='failed',outcome='failed' WHERE id=?`, command.CommandID)
	must(t, err)
	notices, err = s.Notifications(ctx, scope.Actor, "", 100)
	must(t, err)
	if len(notices) != 1 || notices[0].Title != "Agent turn completed" || !notices[0].Read {
		t.Fatal("disabled source changed historical notice or read record", notices)
	}
}

func TestRunOutcomeNotificationsDisabledByDefault(t *testing.T) {
	s, scope, _, command := agentFixture(t)
	for _, outcome := range []string{"unknown", "failed", "completed", "canceled"} {
		state := outcome
		if outcome == "completed" || outcome == "canceled" {
			state = "settled"
		}
		_, err := s.db.Exec(`UPDATE execution_runs SET state=?,outcome=? WHERE id=?`, state, outcome, command.CommandID)
		must(t, err)
		notices, err := s.Notifications(ctx, scope.Actor, "", 100)
		must(t, err)
		unread, err := s.UnreadNotifications(ctx, scope.Actor)
		must(t, err)
		if len(notices) != 0 || unread != 0 {
			t.Fatal("run outcome notification was enabled by default", outcome, notices, unread)
		}
	}

}

func TestConversationShareNotificationPolicyPreservesGrantAndHistory(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "session", "Conversation")
	must(t, err)
	must(t, s.GrantConversation(ctx, scope, "session", "bob"))
	notices, err := s.Notifications(ctx, "bob", "", 100)
	must(t, err)
	if len(notices) != 1 || notices[0].Title != "Conversation shared with you" {
		t.Fatal("default share notification missing", notices)
	}
	must(t, s.ReadNotification(ctx, "bob", notices[0].ID))
	must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{}))
	must(t, s.GrantConversation(ctx, scope, "session", "bob"))
	must(t, s.GrantConversation(ctx, scope, "session", "carol"))
	must(t, s.CanReadConversation(ctx, "carol", "session"))
	carol, err := s.Notifications(ctx, "carol", "", 100)
	must(t, err)
	if len(carol) != 0 {
		t.Fatal("disabled share notification was written", carol)
	}
	notices, err = s.Notifications(ctx, "bob", "", 100)
	must(t, err)
	if len(notices) != 1 || !notices[0].Read {
		t.Fatal("policy change removed share history or read record", notices)
	}
	must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{ConversationShared: true}))
	must(t, s.GrantConversation(ctx, scope, "session", "carol"))
	carol, err = s.Notifications(ctx, "carol", "", 100)
	must(t, err)
	if len(carol) != 1 {
		t.Fatal("re-enabled share notification missing", carol)
	}
}

func TestOperatorNotificationPoliciesAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name                string
		directed, broadcast bool
	}{
		{"both", true, true},
		{"directed", true, false},
		{"broadcast", false, true},
		{"neither", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := fixture(t)
			must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{OperatorDirected: tc.directed, OperatorBroadcast: tc.broadcast}))
			for _, notice := range []struct {
				audience string
				enabled  bool
			}{{"alice", tc.directed}, {"", tc.broadcast}} {
				err := s.Notify(ctx, notice.audience, "Notice", "body")
				if notice.enabled {
					must(t, err)
				} else {
					wantError(t, err, ErrNotificationDisabled)
				}
			}
			alice, err := s.Notifications(ctx, "alice", "", 100)
			must(t, err)
			bob, err := s.Notifications(ctx, "bob", "", 100)
			must(t, err)
			wantAlice, wantBob := 0, 0
			if tc.directed {
				wantAlice++
			}
			if tc.broadcast {
				wantAlice++
				wantBob++
			}
			if len(alice) != wantAlice || len(bob) != wantBob {
				t.Fatal("operator source policy or audience isolation failed", alice, bob)
			}
			for _, notice := range alice {
				must(t, s.ReadNotification(ctx, "alice", notice.ID))
			}
			must(t, s.ConfigureNotificationPolicy(ctx, NotificationPolicy{}))
			retained, err := s.Notifications(ctx, "alice", "", 100)
			must(t, err)
			if len(retained) != len(alice) {
				t.Fatal("operator history removed", retained)
			}
			for _, notice := range retained {
				if !notice.Read {
					t.Fatal("operator read record removed", notice)
				}
			}
		})
	}
}
