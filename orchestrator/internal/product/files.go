package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/projectfiles"
	"harness-platform/orchestrator/internal/sandbox"
)

var ErrFileOperationFailed = errors.New("file operation failed")

var ErrFileOutcomeUnknown = errors.New("file operation outcome unknown; inspect workspace before a new operation")

type FileOperation struct {
	ID        string                   `json:"id"`
	ProjectID string                   `json:"project_id"`
	Request   projectfiles.Request     `json:"request"`
	State     string                   `json:"state"`
	Settled   bool                     `json:"settled"`
	Error     string                   `json:"error,omitempty"`
	Reference sandbox.ProcessReference `json:"-"`
}

func fileOperation(ctx context.Context, q queryer, id string) (o FileOperation, err error) {
	var raw string
	err = missing(q.QueryRowContext(ctx, `SELECT id,project_id,request,store_id,sandbox_id,epoch,handle,state,settled,error FROM file_operations WHERE id=?`, id).Scan(&o.ID, &o.ProjectID, &raw, &o.Reference.StoreID, &o.Reference.SandboxID, &o.Reference.Epoch, &o.Reference.Handle, &o.State, &o.Settled, &o.Error))
	if err == nil {
		err = json.Unmarshal([]byte(raw), &o.Request)
	}
	return
}
func (s *Store) FileOperation(ctx context.Context, scope Scope, id string) (FileOperation, error) {
	if _, err := authorizedProject(ctx, s.reader, scope); err != nil {
		return FileOperation{}, err
	}
	o, err := fileOperation(ctx, s.reader, id)
	if err == nil && o.ProjectID != scope.ProjectID {
		err = ErrNotFound
	}
	return o, err
}
func (s *Store) reserveFile(ctx context.Context, scope Scope, id string, r projectfiles.Request) (o FileOperation, fresh bool, err error) {
	if !validID(id) || r.Validate() != nil {
		return o, false, ErrInvalid
	}
	raw, _ := json.Marshal(r)
	err = s.write(ctx, func(tx *sql.Tx) error {
		p, e := writableProject(ctx, tx, scope)
		if e != nil {
			return e
		}
		old, e := fileOperation(ctx, tx, id)
		if e == nil {
			if old.ProjectID != scope.ProjectID {
				return ErrNotFound
			}
			saved, _ := json.Marshal(old.Request)
			if string(saved) != string(raw) {
				return ErrConflict
			}
			o = old
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if p.State != "active" {
			return ErrPending
		}
		resources, e := resources(ctx, tx, scope.ProjectID)
		if e != nil {
			return e
		}
		o = FileOperation{ID: id, ProjectID: scope.ProjectID, Request: r, State: "running", Reference: sandbox.ProcessReference{StoreID: resources.Reference.StoreID, SandboxID: resources.Reference.SandboxID, Epoch: resources.Reference.Epoch, Handle: "file-" + uuid.NewString()}}
		now := s.timestamp()
		if _, e = tx.ExecContext(ctx, `INSERT INTO file_operations(id,project_id,request,store_id,sandbox_id,epoch,handle,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'running',?,?)`, id, scope.ProjectID, string(raw), o.Reference.StoreID, o.Reference.SandboxID, o.Reference.Epoch, o.Reference.Handle, now, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO project_activity(id,project_id,kind,state,detail,created_at,updated_at) VALUES(?,?,'files','active',?,?,?)`, "file:"+id, scope.ProjectID, r.Operation+" "+r.Path, now, now); e != nil {
			return e
		}
		fresh = true
		return s.activity(ctx, tx, scope.ProjectID)
	})
	return
}
func (s *Store) finishFile(ctx context.Context, o FileOperation, state, detail string, settled bool) error {
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		current, err := fileOperation(ctx, tx, o.ID)
		if err != nil {
			return err
		}
		if current.Settled {
			return nil
		}
		// Recovery may know the process ended without knowing whether its earlier
		// rename committed. Keep that uncertainty even when releasing its activity.
		if current.State == "unknown" {
			state = "unknown"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE file_operations SET state=?,error=?,settled=?,updated_at=? WHERE id=?`, state, detail, settled, s.timestamp(), o.ID); err != nil {
			return err
		}
		activity := "unknown"
		if settled {
			activity = "complete"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE project_activity SET state=?,updated_at=? WHERE id=?`, activity, s.timestamp(), "file:"+o.ID); err != nil {
			return err
		}
		return s.activity(ctx, tx, o.ProjectID)
	})
}

type FileBackend interface {
	sandbox.Processes
	WatchWorkspace(context.Context, sandbox.Reference, uint64) (sandbox.FileWatch, error)
}

// Files composes read-only reusable readers and durable one-shot mutation helpers. No pathname or workspace locks exist.
// active tracks only this owner's in-flight calls, so its reconciler never kills
// a healthy transfer. Durable rows, not this map, discover work after restart.
type Files struct {
	Store            *Store
	Lifecycle        *Lifecycle
	Backend          FileBackend
	Profile          Profile
	Helper           string
	CaptureDirectory string
	mu               sync.Mutex
	active           map[string]bool
	readerOnce       sync.Once
	reader           *WorkspaceReader
}

func (f *Files) Execute(ctx context.Context, scope Scope, id string, r projectfiles.Request, input io.Reader) (FileOperation, error) {
	if r.Validate() != nil || !validID(id) {
		return FileOperation{}, ErrInvalid
	}
	if _, err := f.Store.Project(ctx, scope); err != nil {
		return FileOperation{}, err
	}
	// Return an old outcome without waking a paused Project or dispatching again.
	if old, err := f.Store.FileOperation(ctx, scope, id); err == nil {
		a, _ := json.Marshal(old.Request)
		b, _ := json.Marshal(r)
		if string(a) != string(b) {
			return old, ErrConflict
		}
		return old, fileResult(old)
	} else if !errors.Is(err, ErrNotFound) {
		return FileOperation{}, err
	}
	if err := f.Lifecycle.EnsureConfiguredActive(ctx, scope, f.Profile); err != nil {
		return FileOperation{}, err
	}
	f.mu.Lock()
	if len(f.active) >= 16 {
		f.mu.Unlock()
		return FileOperation{}, ErrCapacity
	}
	o, fresh, err := f.Store.reserveFile(ctx, scope, id, r)
	if err == nil && fresh {
		if f.active == nil {
			f.active = map[string]bool{}
		}
		f.active[id] = true
	}
	f.mu.Unlock()
	if err != nil {
		return o, err
	}
	if !fresh {
		return o, fileResult(o)
	}
	defer func() { f.mu.Lock(); delete(f.active, id); f.mu.Unlock() }()
	if input == nil {
		input = strings.NewReader("")
	}
	raw, _ := json.Marshal(r)
	helper := f.Helper
	if helper == "" {
		helper = "/usr/local/bin/workspace-files"
	}
	runctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if closer, ok := input.(io.ReadCloser); ok {
		stop := context.AfterFunc(runctx, func() { _ = closer.Close() })
		defer stop()
	}
	stream, err := f.Backend.StartProcess(runctx, sandbox.StartProcessRequest{ProcessReference: o.Reference, IdempotencyKey: o.Reference.Handle, Command: helper, Arguments: []string{string(raw)}, Directory: "/workspace"})
	observed := false
	if err == nil {
		stopClose := context.AfterFunc(runctx, func() { _ = stream.Close() })
		defer stopClose()
		defer stream.Close()
		err, observed = f.transfer(runctx, stream, o, input)
	}
	state, detail := "succeeded", ""
	if err != nil {
		state, detail = "unknown", err.Error()
		if observed && !errors.Is(err, ErrFileOutcomeUnknown) {
			state = "failed"
		}
	}
	settleCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	persist := f.Store.finishFile(settleCtx, o, state, detail, observed)
	o.State, o.Error, o.Settled = state, detail, observed
	if persist != nil {
		return o, errors.Join(err, persist)
	}
	if !observed {
		return o, errors.Join(ErrFileOutcomeUnknown, err)
	}
	return o, err
}
func fileResult(o FileOperation) error {
	switch o.State {
	case "running":
		return ErrPending
	case "unknown":
		return ErrFileOutcomeUnknown
	case "failed":
		return fmt.Errorf("%w: %s", ErrFileOperationFailed, o.Error)
	}
	return nil
}
func (f *Files) transfer(ctx context.Context, stream sandbox.ProcessStream, o FileOperation, input io.Reader) (error, bool) {
	var diagnostic strings.Builder
	started := false
	sent := make(chan error, 1)
	for {
		frame, err := stream.Next()
		if err != nil {
			return err, false
		}
		if frame.Gap || frame.ResetReason != "" {
			return errors.New("file transfer output gap"), false
		}
		switch frame.Kind {
		case "start":
			if started {
				continue
			}
			started = true
			// Non-upload commands consume no stdin. Writes use fixed chunks
			// on the original helper; no helper-per-chunk or Agent journal payloads.
			go func() {
				if o.Request.Operation != "write" {
					sent <- nil
					return
				}
				buf := make([]byte, 32<<10)
				remaining := o.Request.Size + 1
				for remaining > 0 {
					size := int64(len(buf))
					if size > remaining {
						size = remaining
					}
					n, e := input.Read(buf[:size])
					remaining -= int64(n)
					if n > 0 {
						if sendErr := f.Backend.SendProcessInput(ctx, o.Reference, buf[:n]); sendErr != nil {
							sent <- sendErr
							_ = stream.Close()
							return
						}
					}
					if e != nil {
						if e != io.EOF {
							sent <- e
							_ = stream.Close()
							return
						}
						break
					}
					if ctx.Err() != nil {
						sent <- ctx.Err()
						_ = stream.Close()
						return
					}
				}
				e := f.Backend.CloseProcessInput(ctx, o.Reference)
				sent <- e
				if e != nil {
					_ = stream.Close()
				}
			}()
		case "data":
			if frame.Stream == "stderr" {
				if diagnostic.Len() < 4096 {
					n := 4096 - diagnostic.Len()
					if n > len(frame.Data) {
						n = len(frame.Data)
					}
					diagnostic.Write(frame.Data[:n])
				}
			} else {
				if len(frame.Data) > 0 {
					return errors.Join(ErrFileOutcomeUnknown, errors.New("unexpected mutation helper output")), false
				}
			}
		case "end":
			if frame.ExitCode != 0 {
				reason := ErrFileOperationFailed
				if frame.ExitCode == 3 {
					reason = ErrFileOutcomeUnknown
				}
				return errors.Join(reason, &sandbox.ProcessExitError{Code: frame.ExitCode, Diagnostic: diagnostic.String()}), true
			}
			select {
			case err := <-sent:
				if err != nil {
					return err, true
				}
			default:
			}
			return nil, true
		}
	}
}

// Reconcile never reissues a file mutation. It terminates abandoned original
// helpers and releases activity only with end/placement-loss evidence.
func (f *Files) Reconcile(ctx context.Context, limit int) error {
	readerErr := f.workspaceReader().Reconcile(ctx)
	if limit < 1 || limit > 100 {
		limit = 32
	}
	rows, err := f.Store.reader.QueryContext(ctx, `SELECT id FROM file_operations WHERE settled=0 ORDER BY updated_at,id LIMIT ?`, limit)
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
	var failures []error
	for _, id := range ids {
		f.mu.Lock()
		active := f.active[id]
		f.mu.Unlock()
		if active {
			continue
		}
		o, err := fileOperation(ctx, f.Store.reader, id)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		_, _ = f.Store.db.ExecContext(ctx, `UPDATE file_operations SET updated_at=? WHERE id=?`, f.Store.timestamp(), id)
		err = f.recover(ctx, o)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(readerErr, errors.Join(failures...))
}
func (f *Files) recover(ctx context.Context, o FileOperation) error {
	r, err := f.Store.resourcesFor(ctx, o.ProjectID)
	if err != nil {
		return err
	}
	if r.Reference.StoreID == o.Reference.StoreID && r.Reference.SandboxID == o.Reference.SandboxID && (r.Reference.Epoch != o.Reference.Epoch || r.Reference.Epoch == 0) {
		return f.Store.finishFile(ctx, o, "unknown", "placement ended before outcome was recorded", true)
	}
	if r.Reference.StoreID != o.Reference.StoreID || r.Reference.SandboxID != o.Reference.SandboxID {
		return ErrConflict
	}
	attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Signal-before-connect avoids allowing an abandoned upload to commit after
	// cleanup is declared. NotFound is process absence, not successful mutation.
	err = f.Backend.SignalProcess(attempt, o.Reference, 9)
	if sandbox.Is(err, sandbox.NotFound) {
		return f.Store.finishFile(ctx, o, "unknown", "helper absent; mutation outcome unknown", true)
	}
	if err != nil && !sandbox.Is(err, sandbox.Precondition) {
		return err
	}
	stream, err := f.Backend.ConnectProcess(attempt, o.Reference, 0)
	if err != nil {
		return err
	}
	defer stream.Close()
	stop := context.AfterFunc(attempt, func() { _ = stream.Close() })
	defer stop()
	for {
		frame, err := stream.Next()
		if err != nil {
			return err
		}
		if frame.Kind == "end" {
			return f.Store.finishFile(ctx, o, "unknown", "abandoned helper ended; inspect workspace before retrying", true)
		}
	}
}

// Watch is advisory and holds no lifecycle activity. The browser rescans its
// bounded inventory on any event, gap, reset or disconnect. A paused Project is
// returned as pending and is never resumed by an observer.
func (f *Files) Watch(ctx context.Context, scope Scope, after uint64) (sandbox.FileWatch, error) {
	r, err := f.Lifecycle.ActiveResources(ctx, scope)
	if err != nil {
		return nil, err
	}
	return f.Backend.WatchWorkspace(ctx, r.Reference, after)
}
