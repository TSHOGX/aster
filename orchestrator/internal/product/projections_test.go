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
	snapshot, err := s.SessionSnapshot(ctx, a, "session", 0, false)
	must(t, err)
	if len(snapshot.Messages) != 100 || snapshot.Messages[0].Sequence != 26 || !snapshot.HasEarlier || len(snapshot.Turns) != 100 {
		t.Fatal(snapshot)
	}
	before, err := s.SessionSnapshot(ctx, a, "session", 26, false)
	must(t, err)
	if len(before.Messages) != 25 || before.HasEarlier || before.Session.Revision != snapshot.Session.Revision {
		t.Fatal(before)
	}
	seq, err := s.MessageSequence(ctx, a, "session", "m-005")
	must(t, err)
	if seq != 6 {
		t.Fatal(seq)
	}
	_, err = s.SessionSnapshot(ctx, b, "session", 0, false)
	wantError(t, err, ErrNotFound)
	_, err = s.AgentRecords(ctx, Scope{b.Actor, a.ProjectID}, "session", 0, 200)
	wantError(t, err, ErrNotFound)
	turn := snapshot.Turns[0]
	must(t, s.ChangeQueuedTurn(ctx, a, "session", turn.ID, "held", snapshot.Session.ControlRevision))
	wantError(t, s.ChangeQueuedTurn(ctx, a, "session", turn.ID, "canceled", snapshot.Session.ControlRevision), ErrConflict)
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

func TestStreamingOutputDoesNotInvalidateExecutionControl(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, command, 1, "command.applied", `{"success":true}`))
	must(t, err)
	before, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	for sequence := uint64(2); sequence < 102; sequence++ {
		_, err = s.IngestAgent(ctx, i, record(i, command, sequence, "native", `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"text"}}`))
		must(t, err)
	}
	after, err := s.Session(ctx, scope, i.SessionID)
	must(t, err)
	if after.Revision <= before.Revision || after.ControlRevision != before.ControlRevision {
		t.Fatal("streaming changed control authority", before, after)
	}
	must(t, s.Interrupt(ctx, scope, i.SessionID, "stop-during-stream", before.ControlRevision))
}
func TestSnapshotKeepsActiveTurnVisibleWithLargeQueue(t *testing.T) {
	s, scope, i, command := agentFixture(t)
	_, err := s.IngestAgent(ctx, i, record(i, command, 1, "command.applied", `{"success":true}`))
	must(t, err)
	for n := 0; n < 150; n++ {
		_, err = s.AdmitTurn(ctx, scope, i.SessionID, fmt.Sprintf("queued-%d", n), "next work", false)
		must(t, err)
	}
	snapshot, err := s.SessionSnapshot(ctx, scope, i.SessionID, 0, false)
	must(t, err)
	if len(snapshot.Turns) != 100 || !snapshot.HasMoreTurns {
		t.Fatal(len(snapshot.Turns), snapshot.HasMoreTurns)
	}
	if snapshot.Turns[0].ID != command.TurnID || snapshot.Turns[0].State != "running" {
		t.Fatal("active turn disappeared behind queued work", snapshot.Turns[0])
	}
}
