package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/sandbox"
)

type agentAttachment struct {
	incarnation Incarnation
	cancel      context.CancelFunc
	input       sync.Mutex
	ready       bool
	err         error
}

// Agents owns one output attachment per runner and fans committed projections
// out through the product outbox. HTTP requests never own this stream context.
type Agents struct {
	ServiceAudience string
	Store           *Store
	Lifecycle       *Lifecycle
	Backend         sandbox.Processes
	RunnerBinary    string
	Profile         Profile
	MaxProcesses    int
	mu              sync.Mutex
	attachments     map[string]*agentAttachment
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

func NewAgents(ctx context.Context, s *Store, l *Lifecycle, b sandbox.Processes, profile Profile, max int) *Agents {
	ctx, cancel := context.WithCancel(ctx)
	return &Agents{Store: s, Lifecycle: l, Backend: b, RunnerBinary: "/usr/local/bin/agent-runner", Profile: profile, MaxProcesses: max, attachments: map[string]*agentAttachment{}, ctx: ctx, cancel: cancel}
}
func (a *Agents) Close() {
	a.cancel()
	a.mu.Lock()
	for _, attachment := range a.attachments {
		attachment.cancel()
	}
	a.mu.Unlock()
	a.wg.Wait()
}

// Step admits no turns itself. It drives at most one durable command for a
// Session selected by a bounded fair worker. Startup doesn't allocate a sandbox
// for the Session: all children resolve the same Project lifecycle reservation.
func (a *Agents) Step(ctx context.Context, scope Scope, session string) error {
	if _, err := a.Store.Session(ctx, scope, session); err != nil {
		return err
	}
	if err := a.Lifecycle.EnsureActive(ctx, scope, a.Profile); err != nil {
		return err
	}
	if _, err := a.Lifecycle.ActiveResources(ctx, scope); err != nil {
		return err
	}
	a.mu.Lock()
	attachment := a.attachments[session]
	if attachment != nil && attachment.err != nil {
		attachment.cancel()
		delete(a.attachments, session)
		attachment = nil
	}
	if attachment == nil {
		if a.MaxProcesses < 1 || len(a.attachments) >= a.MaxProcesses {
			a.mu.Unlock()
			return ErrCapacity
		}
		// Reserve a local slot before asynchronous attachment. Durable process
		// reservation below is independent from this acceleration map.
		attachctx, cancel := context.WithCancel(a.ctx)
		attachment = &agentAttachment{cancel: cancel}
		a.attachments[session] = attachment
		a.mu.Unlock()
		i, err := a.Store.reserveAgent(ctx, scope, session)
		if err != nil {
			a.mu.Lock()
			delete(a.attachments, session)
			a.mu.Unlock()
			cancel()
			return err
		}
		i, err = a.takeOwnership(ctx, i)
		if err != nil {
			a.mu.Lock()
			delete(a.attachments, session)
			a.mu.Unlock()
			cancel()
			return err
		}
		attachment.incarnation = i
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			err := a.attach(attachctx, attachment)
			a.mu.Lock()
			attachment.err = err
			attachment.ready = false
			a.mu.Unlock()
		}()
		return ErrPending
	}
	ready, attachmentErr := attachment.ready, attachment.err
	a.mu.Unlock()
	if attachmentErr != nil {
		return attachmentErr
	}
	if !ready {
		return ErrPending
	}
	f, err := a.Store.agentCommand(ctx, attachment.incarnation)
	if err != nil {
		return err
	}
	if a.ServiceAudience != "" {
		var command struct{ Type string }
		if err := json.Unmarshal(f.Payload, &command); err != nil {
			return err
		}
		if command.Type == "prompt" {
			_, token, err := a.Store.GrantService(ctx, scope, session, f.TurnID, attachment.incarnation.ID, a.ServiceAudience, time.Hour)
			if err != nil {
				return err
			}
			f.Credential = token
		}
	}
	if err = a.send(ctx, attachment, f); err != nil {
		// SendInput may have delivered a prefix. No automatic resend is safe on this
		// stream; preserve the original command and let durable evidence settle it.
		settle, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		_ = a.Store.write(settle, func(tx *sql.Tx) error {
			if _, e := tx.ExecContext(settle, `UPDATE agent_commands SET state='unknown',error=? WHERE id=? AND state='sending'`, err.Error(), f.CommandID); e != nil {
				return e
			}
			if _, e := tx.ExecContext(settle, `UPDATE sessions SET state='blocked',failure='native input outcome unknown' WHERE id=? AND state NOT IN ('deleting','deleted')`, session); e != nil {
				return e
			}
			return a.Store.bumpSession(settle, tx, scope.ProjectID, session, "session.blocked")
		})
		return err
	}
	return nil
}
func (a *Agents) takeOwnership(ctx context.Context, i Incarnation) (Incarnation, error) {
	err := a.Store.write(ctx, func(tx *sql.Tx) error {
		current, err := liveIncarnation(ctx, tx, i.SessionID)
		if err != nil {
			return err
		}
		if current.ID != i.ID {
			return ErrConflict
		}
		if err := tx.QueryRowContext(ctx, `UPDATE agent_incarnations SET owner=owner+1 WHERE id=? RETURNING owner,ingested_sequence`, i.ID).Scan(&i.Owner, &i.Ingested); err != nil {
			return err
		}
		// Outstanding sending commands are not replayed merely because the previous
		// owner died. Journal replay can still provide their definitive results.
		_, err = tx.ExecContext(ctx, `UPDATE agent_commands SET state='unknown',error='application owner changed during send' WHERE incarnation_id=? AND state='sending'`, i.ID)
		return err
	})
	return i, err
}
func (a *Agents) send(ctx context.Context, attachment *agentAttachment, f agentwire.Frame) error {
	attachment.input.Lock()
	defer attachment.input.Unlock()
	var buf bytes.Buffer
	f.Session, f.Incarnation, f.Owner = attachment.incarnation.SessionID, attachment.incarnation.ID, attachment.incarnation.Owner
	if err := agentwire.Encode(&buf, f); err != nil {
		return err
	}
	return a.Backend.SendProcessInput(ctx, attachment.incarnation.Reference, buf.Bytes())
}
func (a *Agents) attach(ctx context.Context, attachment *agentAttachment) error {
	i := attachment.incarnation
	var stream sandbox.ProcessStream
	var err error
	if i.State == "starting" {
		stream, err = a.Backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: i.Reference, IdempotencyKey: i.ID, Command: a.RunnerBinary, Arguments: []string{"--session", i.SessionID, "--incarnation", i.ID}, Directory: "/workspace", Environment: []string{"HOME=/root", "PATH=/opt/node-v24.15.0/bin:/usr/local/bin:/usr/bin:/bin"}})
	} else {
		stream, err = a.Backend.ConnectProcess(ctx, i.Reference, 0)
	}
	if err != nil {
		return err
	}
	defer stream.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	streamctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		defer writer.Close()
		for {
			f, err := stream.Next()
			if err != nil {
				writer.CloseWithError(err)
				return
			}
			if f.Kind == "end" {
				writer.CloseWithError(&sandbox.ProcessExitError{Code: f.ExitCode})
				return
			}
			if f.Gap {
				select {
				case <-streamctx.Done():
					return
				default:
				}
				if err := a.send(ctx, attachment, agentwire.Frame{Kind: "sync", After: i.Ingested}); err != nil {
					writer.CloseWithError(err)
					return
				}
				continue
			}
			if f.Kind == "data" && f.Stream != "stderr" {
				if _, err = writer.Write(f.Data); err != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(); reader.Close(); stream.Close(); <-pumpDone }()
	if err = a.send(ctx, attachment, agentwire.Frame{Kind: "sync", After: i.Ingested}); err != nil {
		return err
	}
	decoder := agentwire.NewDecoder(reader)
	for {
		f, err := decoder.Next()
		if err != nil {
			var exit *sandbox.ProcessExitError
			if errors.As(err, &exit) {
				a.recordExit(ctx, i, exit.Code)
			}
			return err
		}
		if f.Incarnation != i.ID || f.Session != i.SessionID {
			return ErrConflict
		}
		if f.Owner != i.Owner {
			continue
		} // engine ring can contain previous-owner frames.
		switch f.Kind {
		case "synced":
			err = a.Store.write(ctx, func(tx *sql.Tx) error {
				if err := changed(tx.ExecContext(ctx, `UPDATE agent_incarnations SET state='running',error='' WHERE id=? AND owner=? AND state IN ('starting','running')`, i.ID, i.Owner)); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state='idle' WHERE id=? AND state='starting'`, i.SessionID); err != nil {
					return err
				}
				return a.Store.bumpSession(ctx, tx, i.ProjectID, i.SessionID, "session.attached")
			})
			if err != nil {
				return err
			}
			a.mu.Lock()
			attachment.ready = true
			a.mu.Unlock()
		case "record":
			sequence, err := a.Store.IngestAgent(ctx, i, f)
			if err != nil {
				return err
			}
			if err = a.send(ctx, attachment, agentwire.Frame{Kind: "ack", Sequence: sequence}); err != nil {
				return err
			}
		case "command_status": // Transport status never creates a user message.
			if f.State == "dispatching" {
				_, err = a.Store.db.ExecContext(ctx, `UPDATE agent_commands SET state='dispatching' WHERE id=? AND incarnation_id=? AND state='sending'`, f.CommandID, i.ID)
				if err != nil {
					return err
				}
			}
		case "error":
			return fmt.Errorf("runner: %s", f.Error)
		default:
			return ErrInvalid
		}
	}
}
func (a *Agents) recordExit(ctx context.Context, i Incarnation, code int32) {
	settle, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer done()
	_ = a.Store.write(settle, func(tx *sql.Tx) error {
		// Exit 0 is emitted only after the runner verifies descendant cleanup and
		// has no unresolved native commands. Nonzero exit retains ambiguity.
		if code == 0 {
			var pending int
			if err := tx.QueryRowContext(settle, `SELECT count(*) FROM turns WHERE session_id=? AND state IN ('starting','running','stopping','unknown')`, i.SessionID).Scan(&pending); err != nil {
				return err
			}
			if pending == 0 {
				if _, err := tx.ExecContext(settle, `UPDATE agent_incarnations SET state='exited',error='' WHERE id=? AND owner=?`, i.ID, i.Owner); err != nil {
					return err
				}
				if _, err := tx.ExecContext(settle, `UPDATE sessions SET state='idle',failure='' WHERE id=? AND state NOT IN ('deleting','deleted')`, i.SessionID); err != nil {
					return err
				}
				return a.Store.bumpSession(settle, tx, i.ProjectID, i.SessionID, "session.exited")
			}
		}
		// Nonzero exit doesn't establish escaped descendants or native settlement.
		if _, err := tx.ExecContext(settle, `INSERT INTO project_activity(id,project_id,session_id,kind,state,detail,created_at,updated_at) VALUES(?,?,?,'descendants','unknown','runner exited before verified descendant settlement',?,?) ON CONFLICT(id) DO NOTHING`, "exit/"+i.ID, i.ProjectID, i.SessionID, a.Store.timestamp(), a.Store.timestamp()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(settle, `UPDATE agent_incarnations SET state='exited',error=? WHERE id=? AND owner=?`, fmt.Sprintf("runner exit %d", code), i.ID, i.Owner); err != nil {
			return err
		}
		if _, err := tx.ExecContext(settle, `UPDATE turns SET state='unknown',updated_at=? WHERE session_id=? AND state IN ('starting','running','stopping')`, a.Store.timestamp(), i.SessionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(settle, `UPDATE sessions SET state='blocked',failure='runner exited; reconcile journal and descendants' WHERE id=? AND state NOT IN ('deleting','deleted')`, i.SessionID); err != nil {
			return err
		}
		return a.Store.bumpSession(settle, tx, i.ProjectID, i.SessionID, "session.blocked")
	})
}
