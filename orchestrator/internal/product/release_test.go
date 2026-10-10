package product

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"harness-platform/orchestrator/internal/content"
)

func TestReleaseFenceBlocksWorkButPreservesStopAndIngest(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.AdmitTurn(ctx, scope, i.SessionID, "queued-before", "queued", false)
	must(t, err)
	held, err := s.AdmitTurn(ctx, scope, i.SessionID, "held-before", "held", true)
	must(t, err)
	_, err = s.CreateSession(ctx, scope, "new-agent", "New agent")
	must(t, err)
	_, err = s.CreateTerminal(ctx, scope, "new-terminal", "New terminal", 24, 80)
	must(t, err)
	state, err := s.BeginReleaseDrain(ctx, "coordinator-1", time.Minute)
	must(t, err)
	if state.Phase != "draining" || state.Generation != 1 {
		t.Fatal(state)
	}
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "request", "hello", false)
	must(t, err) // original admission remains idempotent
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "new", "new work", false)
	wantError(t, err, ErrDraining)
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "held-during", "held", true)
	must(t, err)
	wantError(t, mutationErr(s, scope, i.SessionID, held, "release-during", "release"), ErrDraining)
	wantError(t, controlErr(s, scope, i.SessionID, targetControl(i, c, held, "steer-during")), ErrDraining)
	_, err = s.reserveAgent(ctx, scope, "new-agent")
	wantError(t, err, ErrDraining)
	_, err = s.reserveTerminal(ctx, scope, "new-terminal", 24, 80)
	wantError(t, err, ErrDraining)
	_, _, err = s.reserveFile(ctx, scope, "new-file", writeRequest("new.txt", "new"))
	wantError(t, err, ErrDraining)
	// Genuine evidence and Stop remain writable while new execution is fenced.
	_, err = s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	must(t, controlErr(s, scope, i.SessionID, stopControl(i, c, "stop-during")))
	stop, err := s.agentCommand(ctx, i)
	must(t, err)
	if string(stop.Payload) != fmt.Sprintf(`{"run_id":%q,"type":"abort"}`, c.CommandID) {
		t.Fatal(string(stop.Payload))
	}
	status, err := s.ReleaseDrainStatus(ctx)
	must(t, err)
	if status.DurableBlockers["turn"] == 0 || status.DurableBlockers["command"] == 0 {
		t.Fatal(status)
	}
	_, err = s.AbortReleaseDrain(ctx, "coordinator-1", state.Generation)
	must(t, err)
	_, err = s.AdmitTurn(ctx, scope, i.SessionID, "after", "after", false)
	must(t, err)
}

func TestReleaseFenceStopsQueuedAndPreexistingPendingDispatch(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	// A steer intent can precede closure while its dispatch has not yet happened.
	held, err := s.AdmitTurn(ctx, scope, i.SessionID, "held", "held", true)
	must(t, err)
	must(t, controlErr(s, scope, i.SessionID, targetControl(i, c, held, "steer")))
	_, err = s.BeginReleaseDrain(ctx, "dispatch-fence", time.Minute)
	must(t, err)
	_, err = s.agentCommand(ctx, i)
	wantError(t, err, ErrDraining)
	// Completion is allowed; queued work cannot create the next prompt command.
	_, err = s.IngestAgent(ctx, i, record(i, c, 1, "command.applied", `{"success":true}`))
	must(t, err)
	must(t, controlErr(s, scope, i.SessionID, stopControl(i, c, "stop-before-held-steer")))
	stop, err := s.agentCommand(ctx, i)
	must(t, err)
	if string(stop.Payload) != fmt.Sprintf(`{"run_id":%q,"type":"abort"}`, c.CommandID) {
		t.Fatal("held steering blocked Stop", stop)
	}
	_, err = s.db.Exec(`UPDATE agent_commands SET state='rejected' WHERE kind='steer'`)
	must(t, err)
	_, err = s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE id=?`, c.CommandID)
	must(t, err)
	// Existing pending commands are durable blockers even without an active turn.
	_, err = s.db.Exec(`UPDATE agent_commands SET state='unknown' WHERE id=?`, c.CommandID)
	must(t, err)
	status, err := s.ReleaseDrainStatus(ctx)
	must(t, err)
	if status.DurableBlockers["command"] == 0 {
		t.Fatal("unknown treated as idle", status)
	}
}

func TestReleaseDeadlineAndStaleCoordinatorSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.db")
	s, err := Open(ctx, path)
	must(t, err)
	state, err := s.BeginReleaseDrain(ctx, "first", time.Second)
	must(t, err)
	same, err := s.BeginReleaseDrain(ctx, "first", time.Hour)
	must(t, err)
	if same.Deadline != state.Deadline || same.Generation != state.Generation {
		t.Fatal("retry extended drain", same, state)
	}
	must(t, s.Close())
	s, err = Open(ctx, path)
	must(t, err)
	defer s.Close()
	restored, err := s.ReleaseState(ctx)
	must(t, err)
	if restored != state {
		t.Fatal("restart reopened gate", restored, state)
	}
	clock, err := time.Parse(time.RFC3339Nano, state.Deadline)
	must(t, err)
	s.now = func() time.Time { return clock.Add(time.Second) }
	must(t, s.ExpireReleaseDrain(ctx))
	expired, err := s.ReleaseState(ctx)
	must(t, err)
	if expired.Phase != "open" || expired.Reason != "deadline" {
		t.Fatal(expired)
	}
	next, err := s.BeginReleaseDrain(ctx, "second", time.Minute)
	must(t, err)
	_, err = s.AbortReleaseDrain(ctx, "first", state.Generation)
	wantError(t, err, ErrConflict)
	latest, err := s.ReleaseState(ctx)
	must(t, err)
	if latest != next {
		t.Fatal("stale abort changed release", latest, next)
	}
	_, err = s.db.Exec(`UPDATE application_release SET phase='activating',deadline=? WHERE id=1`, state.Deadline)
	must(t, err)
	must(t, s.ExpireReleaseDrain(ctx))
	latest, err = s.ReleaseState(ctx)
	must(t, err)
	if latest.Phase != "activating" {
		t.Fatal("expiry reopened a later release phase", latest)
	}
}

func TestReleaseFenceSerializesConcurrentAdmission(t *testing.T) {
	s, scope, _ := fixture(t)
	_, err := s.CreateSession(ctx, scope, "session", "Session")
	must(t, err)
	start := make(chan struct{})
	var group sync.WaitGroup
	for n := 0; n < 16; n++ {
		group.Add(1)
		go func(n int) {
			defer group.Done()
			<-start
			_, e := s.AdmitTurn(ctx, scope, "session", fmt.Sprintf("racing-%d", n), "intent", false)
			if e != nil && !errors.Is(e, ErrDraining) {
				t.Error(e)
			}
		}(n)
	}
	close(start)
	_, err = s.BeginReleaseDrain(ctx, "race", time.Minute)
	must(t, err)
	group.Wait()
	for n := 0; n < 16; n++ {
		_, err = s.AdmitTurn(ctx, scope, "session", fmt.Sprintf("closed-%d", n), "intent", false)
		wantError(t, err, ErrDraining)
	}
	turns, err := s.Turns(ctx, scope, "session", 0, 100)
	must(t, err)
	for _, turn := range turns {
		if len(turn.RequestID) >= 7 && turn.RequestID[:7] == "closed-" {
			t.Fatal(turn)
		}
	}
}

func TestReleaseFenceLeavesScheduledOccurrenceUnconsumed(t *testing.T) {
	s, scope, _ := fixture(t)
	clock := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	schedule, err := s.CreateSchedule(ctx, scope, Schedule{TriggerKind: "cron", ID: "schedule", Title: "Daily", Prompt: "inspect", Cron: "* * * * *", Timezone: "UTC", State: "active"})
	must(t, err)
	_, err = s.BeginReleaseDrain(ctx, "schedules", time.Hour)
	must(t, err)
	clock = clock.Add(2 * time.Minute)
	_, err = s.TickSchedules(ctx, 16)
	must(t, err)
	next, err := s.Schedule(ctx, scope, schedule.ID)
	must(t, err)
	if next.NextAt != schedule.NextAt {
		t.Fatal("drain consumed occurrence", schedule, next)
	}
	runs, err := s.ScheduleRuns(ctx, scope, schedule.ID, "", 100)
	must(t, err)
	if len(runs) != 0 {
		t.Fatal(runs)
	}
}

func TestReleaseFenceBlocksProjectActivationAndQueuedDispatch(t *testing.T) {
	l, scope, _ := lifecycleFixture(t)
	state, err := l.Store.BeginReleaseDrain(ctx, "activation", time.Minute)
	must(t, err)
	wantError(t, l.EnsureActive(ctx, scope, testProfile()), ErrDraining)
	_, err = l.Store.Resources(ctx, scope)
	wantError(t, err, ErrNotFound)
	_, err = l.Store.AbortReleaseDrain(ctx, state.Owner, state.Generation)
	must(t, err)
	must(t, l.EnsureActive(ctx, scope, testProfile()))
	drive(t, l, scope.ProjectID)
	p, err := l.Store.Project(ctx, scope)
	must(t, err)
	must(t, l.Pause(ctx, scope, p.Revision))
	drive(t, l, scope.ProjectID)
	_, err = l.Store.BeginReleaseDrain(ctx, "resume", time.Minute)
	must(t, err)
	wantError(t, l.EnsureActive(ctx, scope, testProfile()), ErrDraining)
	p, err = l.Store.Project(ctx, scope)
	must(t, err)
	if p.State != "paused" {
		t.Fatal(p)
	}
	// Deletion/observation is cleanup and must not be held by the new-work fence.
	must(t, l.Store.DeleteProject(ctx, scope, p.Revision))
}

func TestReleaseFenceDoesNotDispatchPreviouslyQueuedPrompt(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.AdmitTurn(ctx, scope, i.SessionID, "next", "later work", false)
	must(t, err)
	_, err = s.db.Exec(`UPDATE agent_commands SET state='completed' WHERE id=?`, c.CommandID)
	must(t, err)
	_, err = s.db.Exec(`UPDATE execution_runs SET state='settled',outcome='completed' WHERE id=?`, c.CommandID)
	must(t, err)
	_, err = s.BeginReleaseDrain(ctx, "queued", time.Minute)
	must(t, err)
	_, err = s.agentCommand(ctx, i)
	wantError(t, err, ErrDraining)
	var count int
	must(t, s.reader.QueryRow(`SELECT count(*) FROM agent_commands`).Scan(&count))
	if count != 1 {
		t.Fatal("created a command beyond fence", count)
	}
}

func TestReleaseFenceAllowsExistingPublicationButRejectsNewRevision(t *testing.T) {
	s, _, _ := fixture(t)
	entries := []content.Entry{{Path: "assets", Directory: true}}
	input := content.Publication{StoreID: "engine", Purpose: "assets", RevisionDigest: content.Digest(entries), Capacity: 1024, Entries: entries}
	_, err := s.ClaimContentPublication(ctx, input)
	must(t, err)
	_, err = s.BeginReleaseDrain(ctx, "publish", time.Minute)
	must(t, err)
	_, err = s.ClaimContentPublication(ctx, input)
	must(t, err)
	input.Entries = []content.Entry{{Path: "new-assets", Directory: true}}
	input.RevisionDigest = content.Digest(input.Entries)
	_, err = s.ClaimContentPublication(ctx, input)
	wantError(t, err, ErrDraining)
	status, err := s.ReleaseDrainStatus(ctx)
	must(t, err)
	if status.DurableBlockers["content_publication"] != 1 {
		t.Fatal(status)
	}
}

func TestReleaseStopDispatchBypassesUnknownCommand(t *testing.T) {
	s, scope, i, c := agentFixture(t)
	_, err := s.db.Exec(`UPDATE agent_commands SET state='unknown' WHERE id=?`, c.CommandID)
	must(t, err)
	_, err = s.BeginReleaseDrain(ctx, "unknown-stop", time.Minute)
	must(t, err)
	must(t, controlErr(s, scope, i.SessionID, stopControl(i, c, "abort-unknown")))
	stop, err := s.agentCommand(ctx, i)
	must(t, err)
	if string(stop.Payload) != fmt.Sprintf(`{"run_id":%q,"type":"abort"}`, c.CommandID) {
		t.Fatal(stop)
	}
	status, err := s.ReleaseDrainStatus(ctx)
	must(t, err)
	if status.DurableBlockers["command"] < 2 {
		t.Fatal("unknown evidence was discarded", status)
	}
}

func TestReleaseDrainKeepsAlreadyAdmittedModelAuthority(t *testing.T) {
	s, scope, i, c := agentServiceFixture(t)
	_, token, err := s.GrantService(ctx, scope, i.SessionID, i.ID, "model", time.Hour)
	must(t, err)
	before, err := authorizeService(ctx, s.reader, s.timestamp(), "model", token)
	must(t, err)
	_, err = s.BeginReleaseDrain(ctx, "model-drain", time.Minute)
	must(t, err)
	after, err := authorizeService(ctx, s.reader, s.timestamp(), "model", token)
	must(t, err)
	if after.TurnID != c.TurnID || after != before {
		t.Fatal(before, after)
	}
	must(t, holdsAttribution(ctx, s.reader, s.timestamp(), before))
}
