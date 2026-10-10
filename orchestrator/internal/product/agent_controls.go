package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// ExecutionControl binds intent to a specific run and incarnation fence. A retry
// carries the original fields, even when the current head has moved on.
type ExecutionControl struct {
	RequestID      string `json:"request_id"`
	Action         string `json:"action"`
	Source         string `json:"source,omitempty"`
	PromptRevision int64  `json:"expected_prompt_revision,omitempty"`
	RunID          string `json:"target_run_id"`
	IncarnationID  string `json:"target_incarnation_id"`
	Owner          uint64 `json:"target_owner"`
}
type ControlReceipt struct {
	RequestID  string `json:"request_id"`
	Action     string `json:"action"`
	PromptID   string `json:"prompt_id,omitempty"`
	DeliveryID string `json:"delivery_id,omitempty"`
	CommandID  string `json:"command_id"`
	RunID      string `json:"run_id"`
	State      string `json:"state"`
}

func (s *Store) ControlRun(ctx context.Context, scope Scope, session string, req ExecutionControl) (ControlReceipt, error) {
	var result ControlReceipt
	if !validText(req.RequestID, 128) || req.RunID == "" || req.IncarnationID == "" || req.Owner == 0 || (req.Action != "steer" && req.Action != "stop") || (req.Action == "steer" && (req.Source == "" || req.PromptRevision <= 0)) || (req.Action == "stop" && (req.Source != "" || req.PromptRevision != 0)) {
		return result, ErrInvalid
	}
	digest := intentDigest(req.Action, req.RequestID, req.Source, req)
	class := controlWrite
	if req.Action == "stop" {
		class = urgentWrite
	}
	err := s.writeClass(ctx, class, scope.ProjectID, 1<<20, func(tx *sql.Tx) error {
		if _, err := mutableOwnedProject(ctx, tx, scope); err != nil {
			return err
		}
		child, err := sessionIn(ctx, tx, scope, session)
		if err != nil {
			return err
		}
		old, _, _, _, _, _, raw, err := receipt(ctx, tx, session, req.RequestID)
		if err == nil {
			if err = validateReceiptIntent(old, digest); err != nil {
				return err
			}
			return json.Unmarshal(raw, &result)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if child.Deletion != Live {
			return ErrDeleting
		}
		var run ExecutionRun
		err = tx.QueryRowContext(ctx, `SELECT id,prompt_id,incarnation_id,owner,state,reconciled_at FROM execution_runs WHERE id=? AND session_id=?`, req.RunID, session).Scan(&run.ID, &run.PromptID, &run.IncarnationID, &run.Owner, &run.State, &run.SettledAt)
		if errors.Is(err, sql.ErrNoRows) {
			return Admission("target_ended", ErrConflict)
		}
		if err != nil {
			return err
		}
		if run.IncarnationID != req.IncarnationID || run.Owner != req.Owner {
			return Admission("target_changed", ErrConflict)
		}
		if run.SettledAt != "" || (run.State != "starting" && run.State != "running" && !(req.Action == "stop" && (run.State == "stopping" || run.State == "unknown"))) {
			return Admission("target_ended", ErrConflict)
		}
		i, err := liveIncarnation(ctx, tx, session)
		if err != nil {
			return ErrConflict
		}
		if i.ID != req.IncarnationID || i.Owner != req.Owner || i.State != "running" {
			return ErrConflict
		}
		kind, turn := "abort", run.PromptID
		payload := map[string]string{"type": kind, "run_id": run.ID}
		if req.Action == "steer" {
			t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE id=? AND session_id=?`, req.Source, session))
			if err != nil {
				return err
			}
			if t.PromptRevision != req.PromptRevision {
				return Admission("prompt_changed", ErrConflict)
			}
			if t.Disposition != "pending" {
				return Admission("already_claimed", ErrConflict)
			}
			kind, turn = "steer", t.ID
			payload["type"], payload["message"] = kind, t.Content
		} else {
			if err = retireUnsentInputs(ctx, tx, run.ID, "stopped before dispatch", s.timestamp()); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE turns SET queue_policy='held',updated_at=? WHERE session_id=? AND disposition='pending' AND queue_policy='released'`, s.timestamp(), session); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE execution_runs SET state='stopping' WHERE id=?`, run.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE sessions SET state='stopping' WHERE id=?`, session); err != nil {
				return err
			}
		}
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(session+"\x00control\x00"+req.RequestID)).String()
		raw, err = nativePayload(payload)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_commands(id,project_id,session_id,incarnation_id,turn_id,kind,payload,state,created_at) VALUES(?,?,?,?,?,?,?,'pending',?)`, id, scope.ProjectID, session, i.ID, turn, kind, raw, s.timestamp()); err != nil {
			return err
		}
		result = ControlReceipt{RequestID: req.RequestID, Action: req.Action, PromptID: turn, CommandID: id, RunID: run.ID, State: "accepted"}
		if kind == "steer" {
			delivery, token, err := createDelivery(ctx, tx, scope.ProjectID, session, turn, req.RequestID, id, run.ID, "steer", s.timestamp())
			if err != nil {
				return err
			}
			result.DeliveryID = delivery
			payload["input_token"] = token
			raw, err = nativePayload(payload)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE agent_commands SET payload=? WHERE id=?`, raw, id); err != nil {
				return err
			}
		}
		if err = insertReceipt(ctx, tx, scope.ProjectID, session, req.RequestID, req.Action, digest, turn, result.DeliveryID, run.ID, "accepted", result, s.timestamp()); err != nil {
			return err
		}
		if err = s.activity(ctx, tx, scope.ProjectID); err != nil {
			return err
		}
		return s.bumpSession(ctx, tx, scope.ProjectID, session, "command."+kind)
	})
	return result, err
}

// ControlResult separates an immutable acceptance receipt from current evidence.
// A lost HTTP reply is resolved using the same request identity, never resubmission
// under a new delivery identity.
type ControlResult struct {
	Receipt       json.RawMessage `json:"receipt"`
	DeliveryState string          `json:"delivery_state,omitempty"`
	CommandState  string          `json:"command_state,omitempty"`
	RunState      string          `json:"run_state,omitempty"`
	Error         string          `json:"error,omitempty"`
}

func (s *Store) ControlResult(ctx context.Context, scope Scope, session, request string) (ControlResult, error) {
	var out ControlResult
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = sessionIn(ctx, tx, scope, session); err != nil {
		return out, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT c.result,coalesce(d.state,''),coalesce(a.state,''),coalesce(r.state,''),coalesce(d.error,a.error,'') FROM control_receipts c LEFT JOIN input_deliveries d ON d.id=c.delivery_id LEFT JOIN agent_commands a ON a.id=coalesce(d.command_id,json_extract(c.result,'$.command_id')) LEFT JOIN execution_runs r ON r.id=c.run_id WHERE c.session_id=? AND c.request_id=?`, session, request).Scan(&raw, &out.DeliveryState, &out.CommandState, &out.RunState, &out.Error)
	if err != nil {
		return out, missing(err)
	}
	out.Receipt = raw
	return out, tx.Commit()
}
