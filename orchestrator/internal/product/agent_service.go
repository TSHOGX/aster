package product

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/sandbox"
)

type agentAttachment struct {
	scope         Scope
	projectID     string
	statusQueried time.Time
	incarnation   Incarnation
	// credential is this incarnation's service token, delivered once in the
	// start environment. It is empty when the deployment declares no service.
	credential string
	cancel     context.CancelFunc
	input      chan struct{}
	inputOnce  sync.Once
	ready      bool
	err        error
	pressure   agentwire.Pressure
}

// Agents owns one output attachment per runner and fans committed projections
// out through the product outbox. HTTP requests never own this stream context.
type AgentBackend interface {
	sandbox.Processes
	InspectProcess(context.Context, sandbox.ProcessReference) (sandbox.ProcessObservation, error)
}
type Agents struct {
	// ServiceAudience names the Project profile service that agents may reach.
	// Empty means this deployment issues no service credentials at all, which is
	// a supported zero-gateway configuration.
	PlatformEndpoint func(context.Context, Scope) string
	ServiceAudience  string
	ServiceTTL       time.Duration
	Store            *Store
	Lifecycle        *Lifecycle
	Backend          AgentBackend
	RunnerBinary     string
	Bindings         RuntimeBindings
	MaxProcesses     int
	MaxPerProject    int
	mu               sync.Mutex
	attachments      map[string]*agentAttachment
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

func NewAgents(ctx context.Context, s *Store, l *Lifecycle, b AgentBackend, bindings RuntimeBindings, max int) *Agents {
	ctx, cancel := context.WithCancel(ctx)
	return &Agents{Store: s, Lifecycle: l, Backend: b, RunnerBinary: "/usr/local/bin/agent-runner", Bindings: bindings, MaxProcesses: max, attachments: map[string]*agentAttachment{}, ctx: ctx, cancel: cancel}
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
	if err := a.Store.agentAdmission(ctx, scope, session); err != nil {
		return err
	}
	if err := a.Lifecycle.EnsureActive(ctx, scope, a.Bindings); err != nil {
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
		projectCount := 0
		for _, current := range a.attachments {
			if current.projectID == scope.ProjectID {
				projectCount++
			}
		}
		perProject := a.MaxPerProject
		if perProject < 1 {
			perProject = min(a.MaxProcesses, max(2, a.MaxProcesses/2))
		}
		if a.MaxProcesses < 1 || len(a.attachments) >= a.MaxProcesses || projectCount >= perProject {
			a.mu.Unlock()
			return ErrCapacity
		}
		// Reserve a local slot before asynchronous attachment. Durable process
		// reservation below is independent from this acceleration map.
		attachctx, cancel := context.WithCancel(a.ctx)
		attachment = &agentAttachment{cancel: cancel, projectID: scope.ProjectID, scope: scope}
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
		a.mu.Lock()
		attachment.incarnation = i
		a.mu.Unlock()
		// The credential is issued before the runner is started, so Pi's authored
		// configuration already names it when the first turn dispatches. A failure
		// here is a Project-level problem, not a per-turn one.
		attachment.credential, err = a.serviceCredential(ctx, scope, i)
		if err != nil {
			a.mu.Lock()
			delete(a.attachments, session)
			a.mu.Unlock()
			cancel()
			return err
		}
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
	if err := a.queryUncertainCommands(ctx, attachment); err != nil {
		return err
	}
	var stopping bool
	if err := a.Store.reader.QueryRowContext(ctx, `SELECT state='stopping' FROM agent_incarnations WHERE id=? AND owner=?`, attachment.incarnation.ID, attachment.incarnation.Owner).Scan(&stopping); err != nil {
		return missing(err)
	}
	if stopping {
		return a.Stop(ctx, scope.ProjectID, session)
	}
	if a.ServiceAudience != "" {
		resources, e := a.Store.Resources(ctx, scope)
		if e != nil {
			return e
		}
		if _, declared := resources.Profile.Reachability.Audience(a.ServiceAudience); declared {
			if e = a.Store.RenewService(ctx, attachment.incarnation, a.ServiceAudience, a.grantTTL()); e != nil {
				_ = a.Stop(ctx, scope.ProjectID, session)
				return e
			}
		}
	}
	f, err := a.Store.agentCommand(ctx, attachment.incarnation)
	if err != nil {
		return err
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
			if e := markRunUnknown(settle, tx, session); e != nil {
				return e
			}
			if _, e := tx.ExecContext(settle, `UPDATE agent_incarnations SET state='stopping',error='native input outcome unknown; terminating original process' WHERE id=? AND owner=? AND state IN ('starting','running')`, attachment.incarnation.ID, attachment.incarnation.Owner); e != nil {
				return e
			}
			if _, e := tx.ExecContext(settle, `UPDATE sessions SET state='blocked',failure='native input outcome unknown' WHERE id=? AND deletion_intent<2`, session); e != nil {
				return e
			}
			return a.Store.bumpSession(settle, tx, scope.ProjectID, session, "session.blocked")
		})
		_ = a.Stop(settle, scope.ProjectID, session)
		return err
	}
	return nil
}

// serviceCredential mints this incarnation's grant for the configured audience.
// It is issued once per incarnation, before Pi starts, and revoked when the
// incarnation stops running. Its authority still requires an admitted turn at
// every request, so holding it while idle reaches nothing.
// Stop ends one Session's runner and waits for its termination evidence. It is
// what child deletion and Session replacement use: it signals that runner's own
// process group and never a sibling's, and never the Project placement.
//
// A signal is a request. Only the runner's exit frame settles the incarnation, so
// a caller must still observe the durable state rather than assume success.
func (a *Agents) Stop(ctx context.Context, project, session string) error {
	releaseMaintenance, admissionErr := a.Store.beginRuntimeWork(ctx, runtimeMaintenance)
	if admissionErr != nil {
		return admissionErr
	}
	defer releaseMaintenance()
	a.mu.Lock()
	attachment := a.attachments[session]
	incarnation := Incarnation{}
	if attachment != nil {
		incarnation = attachment.incarnation
	}
	a.mu.Unlock()
	if incarnation.ID == "" {
		// No local attachment. A live incarnation from a previous application owner is
		// still signalled, using its own durable process reference.
		i, err := liveIncarnation(ctx, a.Store.reader, session)
		if errors.Is(err, ErrNotFound) {
			// Nothing is running for this Session. That is the settled state Stop is
			// asking for, so it is success rather than a failure to signal.
			return nil
		}
		if err != nil {
			return err
		}
		incarnation = i
		// Restart cleanup must adopt the original output stream before signalling;
		// otherwise its exit frame has no owner to commit the termination evidence.
		a.mu.Lock()
		if current := a.attachments[session]; current != nil {
			incarnation = current.incarnation
			a.mu.Unlock()
		} else {
			owned, e := a.takeOwnership(ctx, i)
			if e != nil {
				a.mu.Unlock()
				return e
			}
			if e = a.Store.write(ctx, func(tx *sql.Tx) error {
				return changed(tx.ExecContext(ctx, `UPDATE agent_incarnations SET state='stopping' WHERE id=? AND owner=? AND state<>'exited'`, owned.ID, owned.Owner))
			}); e != nil {
				a.mu.Unlock()
				return e
			}
			attachctx, cancel := context.WithCancel(a.ctx)
			attachment := &agentAttachment{projectID: project, incarnation: owned, cancel: cancel}
			a.attachments[session] = attachment
			incarnation = owned
			a.wg.Add(1)
			go func() {
				defer a.wg.Done()
				e := a.attach(attachctx, attachment)
				a.mu.Lock()
				attachment.err = e
				attachment.ready = false
				a.mu.Unlock()
			}()
			a.mu.Unlock()
		}
	}
	if incarnation.ProjectID != project {
		return ErrNotFound
	}
	// SIGTERM lets the runner settle its journal and enumerate descendants before
	// exiting. The attachment is deliberately left in place: its own stream must
	// observe the terminal exit frame, which is the only evidence that records this
	// incarnation as exited. Cancelling here would replace that evidence with a
	// transport cancellation, and an unobserved exit is not a settled process.
	if err := a.Backend.SignalProcess(ctx, incarnation.Reference, 15); err != nil {
		// A process the engine no longer holds is already gone. Its durable exit is
		// still recorded by the attachment's stream, never manufactured here.
		if sandbox.Is(err, sandbox.NotFound) || sandbox.Is(err, sandbox.Precondition) {
			a.inspectEnded(ctx, incarnation, err)
			return nil
		}
		return err
	}
	return nil
}
func (a *Agents) serviceCredential(ctx context.Context, scope Scope, i Incarnation) (string, error) {
	if a.ServiceAudience == "" {
		return "", nil
	}
	_, token, err := a.Store.GrantService(ctx, scope, i.SessionID, i.ID, a.ServiceAudience, a.grantTTL())
	if errors.Is(err, ErrConflict) {
		// A live grant already exists for this incarnation and audience. Its secret
		// was only ever known to the guest, so the durable credential the runner
		// already materialized stays authoritative and no second one is issued.
		return "", nil
	}
	if errors.Is(err, ErrInvalid) {
		// The Project profile does not declare this audience. That is a deployment
		// choice, not a Session failure: the agent simply has no service access.
		return "", nil
	}
	return token, err
}
func (a *Agents) grantTTL() time.Duration {
	if a.ServiceTTL > 0 {
		return a.ServiceTTL
	}
	return 12 * time.Hour
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
		if _, err = tx.ExecContext(ctx, `UPDATE input_deliveries SET state='unknown',error='application owner changed during send' WHERE command_id IN (SELECT id FROM agent_commands WHERE incarnation_id=? AND state='sending') AND state='dispatching'`, i.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_commands SET state='unknown',error='application owner changed during send' WHERE incarnation_id=? AND state='sending'`, i.ID)
		return err
	})
	return i, err
}
func (a *Agents) send(ctx context.Context, attachment *agentAttachment, f agentwire.Frame) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	attachment.inputOnce.Do(func() { attachment.input = make(chan struct{}, 1) })
	select {
	case attachment.input <- struct{}{}:
		defer func() { <-attachment.input }()
	case <-ctx.Done():
		return ctx.Err()
	}
	var buf bytes.Buffer
	f.Session, f.Incarnation, f.Owner = attachment.incarnation.SessionID, attachment.incarnation.ID, attachment.incarnation.Owner
	if err := agentwire.Encode(&buf, f); err != nil {
		return err
	}
	return a.Backend.SendProcessInput(ctx, attachment.incarnation.Reference, buf.Bytes())
}
func (a *Agents) attach(ctx context.Context, attachment *agentAttachment) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Bound connection establishment, initial sync send, and journal handshake
	// together. Once synchronized, healthy long turns have no lifetime timeout.
	syncTimer := time.AfterFunc(10*time.Second, cancel)
	defer syncTimer.Stop()
	i := attachment.incarnation
	var stream sandbox.ProcessStream
	var err error
	if i.State == "starting" {
		resources, e := a.Store.resourcesFor(ctx, i.ProjectID)
		if e != nil {
			return e
		}
		environment := []string{"HOME=/root", "PATH=/opt/node-v24.15.0/bin:/usr/local/bin:/usr/bin:/bin"}
		if attachment.credential != "" {
			// The credential travels in the start environment, not in a protocol
			// frame: the framed stream is journaled durable evidence and must not
			// retain a secret, and a reconnect must not need to resend one.
			environment = append(environment, "PROJECT_SERVICE_CREDENTIAL="+attachment.credential)
		}
		if a.PlatformEndpoint != nil && attachment.credential != "" {
			if endpoint := a.PlatformEndpoint(ctx, attachment.scope); endpoint != "" {
				environment = append(environment, "PROJECT_PLATFORM_ENDPOINT="+endpoint)
			}
		}
		arguments := append([]string{"--session", i.SessionID, "--incarnation", i.ID}, resources.Profile.RunnerPaths()...)
		history, historyErr := a.Store.ImportedNativeHistory(ctx, i.SessionID, resources.Profile.RolePath("workspace"))
		if historyErr != nil {
			return historyErr
		}
		if len(history) > 0 {
			installer, ok := a.Backend.(interface {
				InstallSessionHistory(context.Context, sandbox.Reference, string, string, []byte) (string, error)
			})
			if !ok {
				return errors.New("runtime does not support history imports")
			}
			digest, e := installer.InstallSessionHistory(ctx, resources.Reference, resources.Profile.RolePath("runtime"), i.SessionID, history)
			if e != nil {
				return e
			}
			arguments = append(arguments, "--history-digest", digest)
		}
		stream, err = a.Backend.StartProcess(ctx, sandbox.StartProcessRequest{ProcessReference: i.Reference, IdempotencyKey: i.ID, Command: a.RunnerBinary, Arguments: arguments, Directory: resources.Profile.RolePath("workspace"), Environment: environment})
	} else {
		stream, err = a.Backend.ConnectProcess(ctx, i.Reference, 0)
	}
	if err != nil {
		a.inspectEnded(ctx, i, err)
		return err
	}
	defer stream.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var synchronized atomic.Bool
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
			if f.Gap || f.ResetReason != "" {
				if !synchronized.Load() {
					continue
				}
				writer.CloseWithError(errors.New("Agent transport gap; resynchronize original journal"))
				return
			}
			if f.Replay {
				continue
			} // The new sync explicitly replays the durable journal.

			if f.Kind == "data" && f.Stream != "stderr" {
				if _, err = writer.Write(f.Data); err != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(); reader.Close(); stream.Close(); <-pumpDone }()
	if err = a.send(ctx, attachment, agentwire.Frame{Kind: "sync", After: i.Ingested}); err != nil {
		a.inspectEnded(ctx, i, err)
		return err
	}
	var committed atomic.Uint64
	ackDone := make(chan struct{})
	go func() {
		defer close(ackDone)
		a.acknowledge(ctx, attachment, &committed, func(err error) { _ = writer.CloseWithError(err) })
	}()
	defer func() { cancel(); <-ackDone }()
	decoder, decoded := decodeAgentFrames(ctx, reader)
	defer func() { cancel(); reader.Close(); <-decoded }()
	for {
		next := decoder.next()
		f, err := next.frame, next.err
		if err != nil {
			if !synchronized.Load() && errors.Is(err, agentwire.ErrFrame) && next.recoverable {
				continue
			}
			var exit *sandbox.ProcessExitError
			if errors.As(err, &exit) {
				a.recordExit(ctx, i, exit.Code)
			} else {
				a.inspectEnded(ctx, i, err)
			}
			return err
		}
		if f.Incarnation != i.ID || f.Session != i.SessionID {
			return ErrConflict
		}
		if f.Owner != i.Owner {
			continue
		} // engine ring can contain previous-owner frames.
		if !synchronized.Load() && f.Kind != "synced" && f.Kind != "error" {
			continue
		}
		switch f.Kind {
		case "synced":
			// The last ACK may have been lost after host commit. Reclaim that
			// already-durable prefix even when the reattached runner is idle.
			committed.Store(i.Ingested)
			synchronized.Store(true)
			syncTimer.Stop()
			err = a.Store.write(ctx, func(tx *sql.Tx) error {
				if err := changed(tx.ExecContext(ctx, `UPDATE agent_incarnations SET state=CASE WHEN state='stopping' THEN 'stopping' ELSE 'running' END,error='' WHERE id=? AND owner=? AND state IN ('starting','running','stopping')`, i.ID, i.Owner)); err != nil {
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
			if err = a.queryUncertainCommands(ctx, attachment); err != nil {
				return err
			}
		case "record":
			sequence, err := a.Store.IngestAgentBatch(ctx, i, decoder.batch(f))
			if err != nil {
				return err
			}
			committed.Store(sequence)
		case "command_status":
			if err = a.Store.commandStatus(ctx, i, f.CommandID, f.State); err != nil {
				return err
			}

		case "error":
			return fmt.Errorf("runner: %s", f.Error)
		case "pressure":
			if f.Pressure == nil {
				return ErrInvalid
			}
			a.mu.Lock()
			attachment.pressure = *f.Pressure
			a.mu.Unlock()
		default:
			return ErrInvalid
		}
	}
}
func (a *Agents) recordExit(ctx context.Context, i Incarnation, code int32) {
	settle, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer done()
	_ = a.Store.write(settle, func(tx *sql.Tx) error {
		var owner uint64
		if err := tx.QueryRowContext(settle, `SELECT owner FROM agent_incarnations WHERE id=?`, i.ID).Scan(&owner); err != nil {
			return err
		}
		if owner != i.Owner {
			return ErrConflict
		}
		cleanup := "unknown"
		if code == 0 || code == agentwire.ExitCleanFailure {
			cleanup = "settled"
		}
		if _, err := tx.ExecContext(settle, `UPDATE agent_incarnations SET state='exited',cleanup_state=?,recovery_state='pending',error=? WHERE id=? AND owner=?`, cleanup, fmt.Sprintf("guardian exit %d", code), i.ID, i.Owner); err != nil {
			return err
		}
		if _, err := tx.ExecContext(settle, `INSERT INTO project_activity(id,project_id,session_id,kind,state,detail,created_at,updated_at) VALUES(?,?,?,'agent_control','active','original Agent journal recovery',?,?) ON CONFLICT(id) DO NOTHING`, "recovery/"+i.ID, i.ProjectID, i.SessionID, a.Store.timestamp(), a.Store.timestamp()); err != nil {
			return err
		}
		if cleanup == "unknown" {
			if _, err := tx.ExecContext(settle, `INSERT INTO project_activity(id,project_id,session_id,kind,state,detail,created_at,updated_at) VALUES(?,?,?,'descendants','unknown','guardian ended without descendant settlement',?,?) ON CONFLICT(id) DO NOTHING`, "exit/"+i.ID, i.ProjectID, i.SessionID, a.Store.timestamp(), a.Store.timestamp()); err != nil {
				return err
			}
		}
		if err := markRunUnknown(settle, tx, i.SessionID); err != nil {
			return err
		}
		var unknown int
		if err := tx.QueryRowContext(settle, `SELECT count(*) FROM execution_runs WHERE session_id=? AND state='unknown' AND acknowledged=0`, i.SessionID).Scan(&unknown); err != nil {
			return err
		}
		if code != 0 || unknown > 0 {
			if _, err := tx.ExecContext(settle, `UPDATE turns SET queue_policy='held',updated_at=? WHERE session_id=? AND disposition='pending' AND queue_policy='released'`, a.Store.timestamp(), i.SessionID); err != nil {
				return err
			}
		}
		state, failure := "idle", ""
		if cleanup != "settled" || unknown > 0 {
			state, failure = "blocked", "reconciling original Agent journal and cleanup evidence"
		}
		if _, err := tx.ExecContext(settle, `UPDATE sessions SET state=?,failure=? WHERE id=? AND deletion_intent<2`, state, failure, i.SessionID); err != nil {
			return err
		}

		return a.Store.bumpSession(settle, tx, i.ProjectID, i.SessionID, "session.recovering")
	})
}

func (a *Agents) inspectEnded(ctx context.Context, i Incarnation, cause error) {
	if ctx.Err() != nil || cause == nil {
		return
	}
	observed, err := a.Backend.InspectProcess(ctx, i.Reference)
	if err == nil && observed.State == "exited" && observed.ExitCode != nil {
		a.recordExit(ctx, i, int32(*observed.ExitCode))
		return
	}
	if !sandbox.Is(err, sandbox.NotFound) && (err != nil || observed.State != "lost") {
		return
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = a.Store.write(settle, func(tx *sql.Tx) error {
		if err := changed(tx.ExecContext(settle, `UPDATE agent_incarnations SET state='exited',cleanup_state='unknown',recovery_state='pending',error='original guardian handle is absent; no exit outcome is asserted' WHERE id=? AND owner=? AND state<>'exited'`, i.ID, i.Owner)); err != nil {
			return err
		}
		for _, activity := range []struct{ id, kind, detail string }{{"exit/" + i.ID, "descendants", "guardian handle absent; descendant cleanup unknown"}, {"recovery/" + i.ID, "agent_control", "original Agent journal recovery"}} {
			if _, err := tx.ExecContext(settle, `INSERT INTO project_activity(id,project_id,session_id,kind,state,detail,created_at,updated_at) VALUES(?,?,?,?,'unknown',?,?,?) ON CONFLICT(id) DO UPDATE SET state='unknown'`, activity.id, i.ProjectID, i.SessionID, activity.kind, activity.detail, a.Store.timestamp(), a.Store.timestamp()); err != nil {
				return err
			}
		}
		if err := markRunUnknown(settle, tx, i.SessionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(settle, `UPDATE turns SET queue_policy='held',updated_at=? WHERE session_id=? AND disposition='pending' AND queue_policy='released'`, a.Store.timestamp(), i.SessionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(settle, `UPDATE sessions SET state='blocked',failure='original guardian handle absent; reconcile history and descendant cleanup' WHERE id=? AND deletion_intent<2`, i.SessionID); err != nil {
			return err
		}
		return a.Store.bumpSession(settle, tx, i.ProjectID, i.SessionID, "session.recovering")
	})
}

func (a *Agents) queryUncertainCommands(ctx context.Context, attachment *agentAttachment) error {
	a.mu.Lock()
	if time.Since(attachment.statusQueried) < time.Second {
		a.mu.Unlock()
		return nil
	}
	attachment.statusQueried = time.Now()
	a.mu.Unlock()

	rows, err := a.Store.reader.QueryContext(ctx, `SELECT id FROM agent_commands WHERE incarnation_id=? AND state='unknown' ORDER BY created_at,id LIMIT 16`, attachment.incarnation.ID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = a.send(ctx, attachment, agentwire.Frame{Kind: "status", CommandID: id}); err != nil {
			return err
		}
	}
	return nil
}
