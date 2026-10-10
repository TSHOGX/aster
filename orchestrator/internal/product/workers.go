package product

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// Workers compose the application independently of HTTP. Each workload class
// has its own bounded loop so file recovery and host-only work cannot stall
// admission, process attachments or lifecycle observations.
type Workers struct {
	Store          *Store
	Lifecycle      *Lifecycle
	Agents         *Agents
	Terminals      *Terminals
	Files          *Files
	Shares         *Shares
	Captures       *Captures
	Recovery       *AgentRecovery
	Retirement     *ChildRetirement
	Titles         TitleGenerator
	IdleAfter      time.Duration
	IdleAgentAfter time.Duration
	Interval       time.Duration
	Report         func(string, error)
	cursor         string
	displayCursor  string
}

func (w *Workers) report(kind string, err error) {
	if errors.Is(err, ErrDraining) {
		return
	}
	if err != nil && !errors.Is(err, ErrPending) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrConflict) && !errors.Is(err, context.Canceled) && w.Report != nil {
		w.Report(kind, err)
	}
}
func (w *Workers) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	var group sync.WaitGroup
	loop := func(kind string, interval time.Duration, step func(context.Context) error) {
		group.Add(1)
		go func() {
			defer group.Done()
			timer := time.NewTicker(interval)
			defer timer.Stop()
			for {
				attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
				w.report(kind, step(attempt))
				cancel()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
			}
		}()
	}
	loop("runtime", interval, w.Step)
	loop("release deadline", time.Second, w.Store.ExpireReleaseDrain)
	loop("conversation display", 100*time.Millisecond, w.displayStep)
	loop("agent recovery", time.Second, func(ctx context.Context) error {
		if w.Recovery != nil {
			return w.Recovery.Step(ctx, 8)
		}
		return nil
	})
	loop("files", time.Second, func(ctx context.Context) error {
		if w.Files != nil {
			return w.Files.Reconcile(ctx, 8)
		}
		return nil
	})
	loop("capture", time.Second, w.captureStep)
	loop("metadata retention", time.Second, w.Store.PruneMetadata)
	loop("adjacent", time.Second, func(ctx context.Context) error {
		_, scheduleErr := w.Store.TickSchedules(ctx, 16)
		_, titleErr := w.Store.GenerateTitles(ctx, w.Titles, 8)
		var shareErr error
		if w.Shares != nil {
			shareErr = w.Shares.Reconcile(ctx, 16)
		}
		return errors.Join(scheduleErr, titleErr, shareErr)
	})
	group.Wait()
}

func (w *Workers) displayStep(ctx context.Context) error {
	rows, err := w.Store.reader.QueryContext(ctx, `SELECT p.session_id FROM conversation_pending_sessions p JOIN sessions s ON s.id=p.session_id LEFT JOIN conversation_checkpoints c ON c.session_id=p.session_id WHERE p.session_id>? AND s.deletion_intent<2 AND coalesce(c.state,'')<>'failed' ORDER BY p.session_id LIMIT 8`, w.displayCursor)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		w.displayCursor = ""
		return nil
	}
	w.displayCursor = ids[len(ids)-1]
	var results []error
	for _, id := range ids {
		results = append(results, w.Store.ProjectConversation(ctx, id))
	}
	return errors.Join(results...)
}
func (w *Workers) Step(ctx context.Context) error {
	rows, err := w.Store.reader.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE state<>'deleted' AND id>? ORDER BY id LIMIT 16`, w.cursor)
	if err != nil {
		return err
	}
	projects := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		w.cursor = ""
		return nil
	}
	w.cursor = projects[len(projects)-1].ID
	// At most four Projects consume remote control work concurrently. Attachments
	// run independently, and one Project never holds a global workspace mutex.
	slots := make(chan struct{}, 4)
	var group sync.WaitGroup
	defer group.Wait()
	for _, p := range projects {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		group.Add(1)
		go func(p Project) {
			defer group.Done()
			defer func() { <-slots }()
			attempt, done := context.WithTimeout(ctx, 10*time.Second)
			defer done()
			w.report("project "+p.ID, w.project(attempt, p))
		}(p)
	}
	group.Wait()
	return nil
}
func (w *Workers) project(ctx context.Context, p Project) error {
	scope := Scope{p.OwnerID, p.ID}
	w.report("placement observation", w.Lifecycle.Observe(ctx, p.ID))
	if w.Terminals != nil {
		w.report("terminal reconciliation", w.Terminals.Reconcile(ctx, p.ID))
	}
	// Child cleanup is part of Project deletion too, and precedes placement
	// teardown. A deletion intent never substitutes for process-exit evidence.
	children, err := w.Store.projectChildrenForCleanup(ctx, p.ID, p.State == "deleting")
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.Kind == "session" && w.Agents != nil {
			w.report("stop agent", w.Agents.Stop(ctx, p.ID, child.ID))
		}
		if child.Kind == "terminal" && w.Terminals != nil {
			w.report("stop terminal", w.Terminals.Stop(ctx, p.ID, child.ID))
		}
		if p.State != "deleting" && w.Retirement != nil {
			w.report("child cleanup", w.Retirement.Step(ctx, child))
		} else if p.State != "deleting" {
			w.report("child cleanup", w.Store.SettleChild(ctx, child))
		}
	}
	w.report("lifecycle", w.Lifecycle.Reconcile(ctx, p.ID))
	if w.Agents != nil {
		w.report("agent reconciliation", w.Agents.Reconcile(ctx, p.ID))
		w.report("agent controls", w.Agents.ReconcileControls(ctx, p.ID))
	}
	if p.State == "deleting" {
		return nil
	}
	rows, err := w.Store.reader.QueryContext(ctx, `SELECT s.id FROM sessions s WHERE s.project_id=? AND s.deletion_intent=0 AND s.state<>'blocked' AND (EXISTS(SELECT 1 FROM turns t WHERE t.session_id=s.id AND t.disposition='pending' AND t.queue_policy='released') OR EXISTS(SELECT 1 FROM execution_runs r WHERE r.session_id=s.id AND (r.state IN ('starting','running','stopping') OR (r.state='unknown' AND r.acknowledged=0))) OR EXISTS(SELECT 1 FROM agent_incarnations i WHERE i.session_id=s.id AND i.state IN ('starting','running','stopping'))) ORDER BY s.updated_at,s.id LIMIT 32`, p.ID)
	if err != nil {
		return err
	}
	var sessions []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if w.Agents != nil {
		for _, id := range sessions {
			w.report("agent dispatch", w.Agents.Step(ctx, scope, id))
		}
	}
	if w.Terminals != nil {
		live, err := w.Store.LiveTerminals(ctx, p.ID, 64)
		if err != nil {
			return err
		}
		for _, terminal := range live {
			_, err = w.Terminals.Start(ctx, scope, terminal.TerminalID, terminal.Rows, terminal.Cols)
			w.report("terminal adoption", err)
		}
	}
	if w.Agents != nil {
		w.report("idle Agent quiescence", w.Agents.QuiesceIdle(ctx, p.ID, w.IdleAgentAfter))
	}
	p, err = ownedProject(ctx, w.Store.reader, scope)
	if err != nil {
		return err
	}
	if p.State == "active" && w.IdleAfter > 0 {
		if p.IdleDeadline == "" {
			_, err = w.Store.ArmIdle(ctx, scope, w.IdleAfter)
			if errors.Is(err, ErrBusy) {
				return nil
			}
			return err
		}
		if p.IdleDeadline <= w.Store.timestamp() {
			return w.Lifecycle.PauseIdle(ctx, scope, p)
		}
	}
	return nil
}
func (s *Store) projectChildrenForCleanup(ctx context.Context, project string, deleting bool) ([]Child, error) {
	if deleting {
		if err := s.write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET deletion_intent=1 WHERE project_id=? AND deletion_intent=0`, project); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE terminals SET deletion_intent=1 WHERE project_id=? AND deletion_intent=0`, project)
			return err
		}); err != nil {
			return nil, err
		}
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,'session' FROM sessions WHERE project_id=? AND deletion_intent=1 UNION ALL SELECT id,'terminal' FROM terminals WHERE project_id=? AND deletion_intent=1 LIMIT 64`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Child{}
	for rows.Next() {
		c := Child{ProjectID: project, Deletion: Requested}
		if err = rows.Scan(&c.ID, &c.Kind); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// One Session's slow source never serializes unrelated exports. The selection
// places one item per Session first; at most eight bounded streams run at once.
func (w *Workers) captureStep(ctx context.Context) error {
	if w.Captures == nil {
		return nil
	}
	pending, err := w.Store.PendingCaptures(ctx, 8)
	if err != nil {
		return err
	}
	errs := make([]error, len(pending))
	var group sync.WaitGroup
	for index, capture := range pending {
		group.Add(1)
		go func(index int, capture NativeCapture) {
			defer group.Done()
			attempt, done := context.WithTimeout(ctx, 5*time.Second)
			defer done()
			errs[index] = w.Captures.Export(attempt, capture)
		}(index, capture)
	}
	group.Wait()
	return errors.Join(errs...)
}
