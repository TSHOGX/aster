package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/agentwire"
	"harness-platform/orchestrator/internal/sandbox"
)

type Incarnation struct {
	ID, ProjectID, SessionID string
	Reference                sandbox.ProcessReference
	Owner                    uint64
	State                    string
	Ingested                 uint64
	Error                    string
}

const incarnationColumns = `id,project_id,session_id,store_id,sandbox_id,epoch,handle,owner,state,ingested_sequence,error`

func scanIncarnation(row scanner) (i Incarnation, err error) {
	err = missing(row.Scan(&i.ID, &i.ProjectID, &i.SessionID, &i.Reference.StoreID, &i.Reference.SandboxID, &i.Reference.Epoch, &i.Reference.Handle, &i.Owner, &i.State, &i.Ingested, &i.Error))
	return
}
func liveIncarnation(ctx context.Context, q queryer, session string) (Incarnation, error) {
	return scanIncarnation(q.QueryRowContext(ctx, `SELECT `+incarnationColumns+` FROM agent_incarnations WHERE session_id=? AND state<>'exited'`, session))
}
func (s *Store) reserveAgent(ctx context.Context, scope Scope, session string) (i Incarnation, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		p, err := writableProject(ctx, tx, scope)
		if err != nil {
			return err
		}
		if p.State != "active" {
			return ErrPending
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		if child.State == "blocked" {
			return ErrConflict
		}
		var recovering int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_incarnations WHERE session_id=? AND recovery_state IN ('pending','replaying')`, session).Scan(&recovering); err != nil {
			return err
		}
		if recovering > 0 {
			return ErrPending
		}
		i, err = liveIncarnation(ctx, tx, session)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		r, err := resources(ctx, tx, scope.ProjectID)
		if err != nil {
			return err
		}
		id := uuid.NewString()
		i = Incarnation{ID: id, ProjectID: scope.ProjectID, SessionID: session, Reference: sandbox.ProcessReference{StoreID: r.Reference.StoreID, SandboxID: r.Reference.SandboxID, Epoch: r.Reference.Epoch, Handle: "agent-" + id}, Owner: 1, State: "starting"}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_incarnations(id,project_id,session_id,store_id,sandbox_id,epoch,handle,owner,state,last_work_at,created_at) VALUES(?,?,?,?,?,?,?,1,'starting',?,?)`, id, scope.ProjectID, session, i.Reference.StoreID, i.Reference.SandboxID, i.Reference.Epoch, i.Reference.Handle, s.timestamp(), s.timestamp())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state='starting',failure='' WHERE id=?`, session); err != nil {
			return err
		}
		if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "session.starting")
	})
	return
}
func (s *Store) agentCommand(ctx context.Context, i Incarnation) (agentwire.Frame, error) {
	var f agentwire.Frame
	err := s.write(ctx, func(tx *sql.Tx) error {
		var projectState, sessionState string
		var deletion Deletion
		if err := tx.QueryRowContext(ctx, `SELECT p.state,s.state,s.deletion_intent FROM projects p JOIN sessions s ON s.project_id=p.id WHERE p.id=? AND s.id=?`, i.ProjectID, i.SessionID).Scan(&projectState, &sessionState, &deletion); err != nil {
			return err
		}
		if projectState != "active" || sessionState == "blocked" || deletion != Live {
			return ErrPending
		}
		current, err := liveIncarnation(ctx, tx, i.SessionID)
		if err != nil {
			return err
		}
		if current.ID != i.ID || current.Owner != i.Owner || current.State != "running" {
			return ErrConflict
		}
		var unknown int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_commands WHERE incarnation_id=? AND state='unknown'`, i.ID).Scan(&unknown); err != nil {
			return err
		}
		if unknown > 0 {
			return ErrPending
		}
		var id, turn, kind string
		var payload []byte
		err = tx.QueryRowContext(ctx, `SELECT id,coalesce(turn_id,''),kind,payload FROM agent_commands WHERE incarnation_id=? AND state='pending' ORDER BY created_at,id LIMIT 1`, i.ID).Scan(&id, &turn, &kind, &payload)
		if errors.Is(err, sql.ErrNoRows) {
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM turns WHERE session_id=? AND (state IN ('starting','running','stopping') OR (state='unknown' AND acknowledged=0))`, i.SessionID).Scan(&active); err != nil {
				return err
			}
			if active > 0 {
				return ErrPending
			}
			var content string
			if err := tx.QueryRowContext(ctx, `SELECT id,content FROM turns WHERE session_id=? AND state='queued' ORDER BY sequence LIMIT 1`, i.SessionID).Scan(&turn, &content); err != nil {
				return missing(err)
			}
			id, kind = uuid.NewString(), "prompt"
			payload, err = nativePayload(map[string]string{"type": "prompt", "message": content})
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_commands(id,project_id,session_id,incarnation_id,turn_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,'prompt',?,'pending',?)`, id, i.ProjectID, i.SessionID, i.ID, turn, payload, s.timestamp()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE turns SET state='starting',updated_at=? WHERE id=? AND state='queued'`, s.timestamp(), turn); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := changed(tx.ExecContext(ctx, `UPDATE agent_commands SET state='sending' WHERE id=? AND state='pending'`, id)); err != nil {
			return err
		}
		f = agentwire.Frame{Kind: "command", Session: i.SessionID, Incarnation: i.ID, Owner: i.Owner, CommandID: id, TurnID: turn, Payload: payload}
		return s.bumpSession(ctx, tx, i.ProjectID, i.SessionID, "command.sending")
	})
	return f, err
}

// IngestAgent commits raw record identity and its projection together. A caller
// may ACK only the returned watermark, and only after this transaction commits.
func (s *Store) IngestAgent(ctx context.Context, i Incarnation, f agentwire.Frame) (uint64, error) {
	return s.ingestAgent(ctx, i, f, false)
}
func (s *Store) ingestAgent(ctx context.Context, i Incarnation, f agentwire.Frame, recovery bool) (uint64, error) {
	if f.Kind != "record" || f.Session != i.SessionID || f.Incarnation != i.ID || f.Owner != i.Owner || f.Sequence == 0 || len(f.Payload) > agentwire.MaxFrame-1024 || !json.Valid(f.Payload) {
		return 0, ErrInvalid
	}
	digest := creationDigest(f.CommandID, f.TurnID, f.State, string(f.Payload))
	var watermark uint64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var current Incarnation
		var err error
		if recovery {
			current, err = scanIncarnation(tx.QueryRowContext(ctx, `SELECT `+incarnationColumns+` FROM agent_incarnations WHERE id=? AND state='exited' AND recovery_state='replaying'`, i.ID))
		} else {
			current, err = liveIncarnation(ctx, tx, i.SessionID)
		}
		if err != nil {
			return err
		}
		if current.ID != i.ID || current.Owner != i.Owner {
			return ErrConflict
		}
		watermark = current.Ingested
		if f.Sequence <= watermark {
			var old string
			if err := tx.QueryRowContext(ctx, `SELECT digest FROM agent_records WHERE incarnation_id=? AND sequence=?`, i.ID, f.Sequence).Scan(&old); err != nil {
				return err
			}
			if old != digest {
				return ErrConflict
			}
			return nil
		}
		if f.Sequence != watermark+1 {
			return ErrConflict
		}
		if f.TurnID != "" {
			var found string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM turns WHERE id=? AND session_id=? AND project_id=?`, f.TurnID, i.SessionID, i.ProjectID).Scan(&found); err != nil {
				return missing(err)
			}
		}
		if f.CommandID != "" {
			var found string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_commands WHERE id=? AND incarnation_id=?`, f.CommandID, i.ID).Scan(&found); err != nil {
				return missing(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_records(incarnation_id,sequence,digest,command_id,turn_id,kind,payload) VALUES(?,?,?,?,?,?,?)`, i.ID, f.Sequence, digest, f.CommandID, f.TurnID, f.State, []byte(f.Payload)); err != nil {
			return err
		}
		if err = s.projectAgentRecord(ctx, tx, i, f); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_incarnations SET ingested_sequence=?,last_work_at=? WHERE id=? AND owner=?`, f.Sequence, s.timestamp(), i.ID, i.Owner); err != nil {
			return err
		}
		watermark = f.Sequence
		return s.bumpSession(ctx, tx, i.ProjectID, i.SessionID, "agent.record")
	})
	return watermark, err
}
func (s *Store) projectAgentRecord(ctx context.Context, tx *sql.Tx, i Incarnation, f agentwire.Frame) error {
	if f.State == "native" {
		var event struct {
			Type    string
			Message struct {
				Role    string
				Content []struct{ Type, Text string }
			}
		}
		if err := json.Unmarshal(f.Payload, &event); err != nil {
			return err
		}
		if event.Type == "message_end" && event.Message.Role == "assistant" && f.TurnID != "" {
			text := ""
			for _, part := range event.Message.Content {
				if part.Type == "text" {
					text += part.Text
				}
			}
			if text != "" {
				return s.message(ctx, tx, i, f.TurnID, fmt.Sprintf("%s/%d", i.ID, f.Sequence), "assistant", text, f.Payload)
			}
		}
		return nil
	}
	var kind, turn string
	if err := tx.QueryRowContext(ctx, `SELECT kind,coalesce(turn_id,'') FROM agent_commands WHERE id=? AND incarnation_id=?`, f.CommandID, i.ID).Scan(&kind, &turn); err != nil {
		return err
	}
	state := ""
	switch f.State {
	case "command.applied":
		state = "applied"
	case "command.rejected":
		state = "rejected"
	case "command.completed":
		state = "completed"
	case "command.unknown":
		state = "unknown"
	default:
		return ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_commands SET state=? WHERE id=?`, state, f.CommandID); err != nil {
		return err
	}
	if kind == "steer" && state == "applied" {
		var content string
		if err := tx.QueryRowContext(ctx, `SELECT content FROM turns WHERE id=? AND state='held'`, turn).Scan(&content); err != nil {
			return err
		}
		if err := s.message(ctx, tx, i, turn, turn+"/user", "user", content, nil); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE turns SET state='completed',updated_at=? WHERE id=?`, s.timestamp(), turn)
		return err
	}
	if kind != "prompt" {
		return nil
	}
	switch state {
	case "applied":
		var content string
		if err := tx.QueryRowContext(ctx, `SELECT content FROM turns WHERE id=?`, turn).Scan(&content); err != nil {
			return err
		}
		if err := s.message(ctx, tx, i, turn, turn+"/user", "user", content, nil); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET state='running',updated_at=? WHERE id=? AND state='starting'`, s.timestamp(), turn); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET state='running' WHERE id=? AND deletion_intent<2`, i.SessionID)
		return err
	case "rejected":
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET state='failed',updated_at=? WHERE id=?`, s.timestamp(), turn); err != nil {
			return err
		}
	case "unknown":
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET state='unknown',updated_at=? WHERE id=?`, s.timestamp(), turn); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET state='blocked',failure='native command outcome unknown' WHERE id=? AND deletion_intent<2`, i.SessionID)
		return err
	case "completed":
		var capture struct {
			Path, Digest, Outcome string
			Size                  int64
		}
		if err := json.Unmarshal(f.Payload, &capture); err != nil {
			return err
		}
		expectedPrefix := "/runtime/sessions/" + i.SessionID + "/native/captures/"
		if capture.Path != expectedPrefix+f.CommandID+".jsonl" || len(capture.Digest) != len("sha256:")+sha256.Size*2 || capture.Size < 0 {
			return ErrInvalid
		}
		outcome := "completed"
		if capture.Outcome == "canceled" || capture.Outcome == "failed" {
			outcome = capture.Outcome
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_captures(turn_id,incarnation_id,path,digest,size) VALUES(?,?,?,?,?)`, turn, i.ID, capture.Path, capture.Digest, capture.Size); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_activity(id,project_id,session_id,kind,state,detail,created_at,updated_at) VALUES(?,?,?,'capture','active','completed native bytes awaiting export',?,?)`, "capture/"+turn, i.ProjectID, i.SessionID, s.timestamp(), s.timestamp()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET state=?,updated_at=? WHERE id=?`, outcome, s.timestamp(), turn); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET state='idle' WHERE id=? AND deletion_intent<2 AND state<>'blocked'`, i.SessionID); err != nil {
		return err
	}
	return s.activity(ctx, tx, i.ProjectID)
}
func (s *Store) message(ctx context.Context, tx *sql.Tx, i Incarnation, turn, id, role, content string, native []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO messages(id,project_id,session_id,turn_id,role,content,native_message,sequence,created_at) VALUES(?,?,?,?,?,?,?,(SELECT coalesce(max(sequence),0)+1 FROM messages WHERE session_id=?),?) ON CONFLICT(id) DO NOTHING`, id, i.ProjectID, i.SessionID, turn, role, content, native, i.SessionID, s.timestamp())
	return err
}

type Message struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	SessionID string          `json:"session_id"`
	TurnID    string          `json:"turn_id"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Native    json.RawMessage `json:"native,omitempty"`
	Sequence  int64           `json:"sequence"`
	CreatedAt string          `json:"created_at"`
}

func (s *Store) Messages(ctx context.Context, scope Scope, session string, after int64, limit int) ([]Message, error) {
	if _, err := sessionIn(ctx, s.reader, scope, session); err != nil {
		return nil, err
	}
	return s.messages(ctx, scope.ProjectID, session, after, limit)
}
func (s *Store) messages(ctx context.Context, project, session string, after int64, limit int) ([]Message, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT id,project_id,session_id,turn_id,role,content,native_message,sequence,created_at FROM messages WHERE project_id=? AND session_id=? AND sequence>? ORDER BY sequence LIMIT ?`, project, session, after, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var native []byte
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.SessionID, &m.TurnID, &m.Role, &m.Content, &native, &m.Sequence, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.Native = native
		out = append(out, m)
	}
	return out, rows.Err()
}

func nativePayload(value any) ([]byte, error) {
	raw, err := agentwire.MarshalJSON(value)
	if err != nil || len(raw) > agentwire.MaxFrame-1024 {
		return nil, ErrInvalid
	}
	return raw, nil
}

func (s *Store) commandStatus(ctx context.Context, i Incarnation, id, state string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		current, err := liveIncarnation(ctx, tx, i.SessionID)
		if err != nil {
			return err
		}
		if current.ID != i.ID || current.Owner != i.Owner {
			return ErrConflict
		}
		switch state {
		case "absent", "received":
			// The new owner's serialized status query proves the original native command
			// was never dispatched. Retain its original ID/payload/turn for first dispatch.
			_, err = tx.ExecContext(ctx, `UPDATE agent_commands SET state='pending',error='' WHERE id=? AND incarnation_id=? AND state='unknown'`, id, i.ID)
		case "dispatching":
			_, err = tx.ExecContext(ctx, `UPDATE agent_commands SET state='dispatching' WHERE id=? AND incarnation_id=? AND state IN ('sending','unknown')`, id, i.ID)
		}
		// Applied/completed/rejected statuses are not projection authority. Their
		// original sequenced journal record must still commit before changing a turn.
		return err
	})
}
