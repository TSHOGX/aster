package product

import (
	"fmt"
	"testing"
)

func TestSnapshotPagesAndRecordAuthorization(t *testing.T) {
	s, a, b := fixture(t)
	_, err := s.CreateSession(ctx, a, "session", "Agent")
	must(t, err)
	for i := 0; i < 125; i++ {
		seedMessage(t, s, a, "session", fmt.Sprintf("m-%03d", i), fmt.Sprint("message ", i))
	}
	snapshot, err := s.SessionSnapshot(ctx, a, "session", 0)
	must(t, err)
	if len(snapshot.Messages) != 100 || snapshot.Messages[0].Sequence != 26 || !snapshot.HasEarlier || len(snapshot.Turns) != 100 {
		t.Fatal(snapshot)
	}
	before, err := s.SessionSnapshot(ctx, a, "session", 26)
	must(t, err)
	if len(before.Messages) != 25 || before.HasEarlier || before.Session.Revision != snapshot.Session.Revision {
		t.Fatal(before)
	}
	seq, err := s.MessageSequence(ctx, a, "session", "m-005")
	must(t, err)
	if seq != 6 {
		t.Fatal(seq)
	}
	_, err = s.SessionSnapshot(ctx, b, "session", 0)
	wantError(t, err, ErrNotFound)
	_, err = s.AgentRecords(ctx, Scope{b.Actor, a.ProjectID}, "session", 0, 200)
	wantError(t, err, ErrNotFound)
	turn := snapshot.Turns[0]
	must(t, s.ChangeQueuedTurn(ctx, a, "session", turn.ID, "held", snapshot.Session.Revision))
	wantError(t, s.ChangeQueuedTurn(ctx, a, "session", turn.ID, "canceled", snapshot.Session.Revision), ErrConflict)
}
func TestPlacementLossFencesOldAgentOwner(t *testing.T) {
	s, a, _ := fixture(t)
	backend := fakeBackend()
	l := &Lifecycle{Store: s, Backend: backend, MaxProjects: 1}
	must(t, l.EnsureActive(ctx, a, testProfile()))
	drive(t, l, a.ProjectID)
	_, err := s.CreateSession(ctx, a, "session", "Agent")
	must(t, err)
	incarnation, err := s.reserveAgent(ctx, a, "session")
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_incarnations SET state='running' WHERE id=?`, incarnation.ID)
	must(t, err)
	_, err = s.db.Exec(`UPDATE sessions SET state='idle' WHERE id=?`, incarnation.SessionID)
	must(t, err)
	p, err := s.Project(ctx, a)
	must(t, err)
	must(t, l.Pause(ctx, a, p.Revision))
	drive(t, l, a.ProjectID)
	old, err := scanIncarnation(s.reader.QueryRowContext(ctx, `SELECT `+incarnationColumns+` FROM agent_incarnations WHERE id=?`, incarnation.ID))
	must(t, err)
	if old.State != "exited" || old.Owner <= incarnation.Owner {
		t.Fatal(old)
	}
	agents := NewAgents(ctx, s, l, nil, testProfile(), 4)
	defer agents.Close()
	agents.recordExit(ctx, incarnation, 1)
	child, err := s.Session(ctx, a, "session")
	must(t, err)
	if child.State != "idle" {
		t.Fatal("old owner changed successor state", child)
	}
}
