package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

func intentDigest(action, request, source string, payload any) string {
	raw, _ := json.Marshal(struct {
		Action, Request, Source string
		Payload                 any
	}{action, request, source, payload})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func insertReceipt(ctx context.Context, tx *sql.Tx, project, session, request, action, digest, prompt, delivery, run, state string, result any, now string) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO control_receipts(request_id,project_id,session_id,action,intent_digest,prompt_id,delivery_id,run_id,state,result,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, request, project, session, action, digest, prompt, delivery, run, state, raw, now, now)
	return err
}

func receipt(ctx context.Context, tx *sql.Tx, session, request string) (digest, action, prompt, delivery, run, state string, result []byte, err error) {
	err = tx.QueryRowContext(ctx, `SELECT intent_digest,action,prompt_id,delivery_id,run_id,state,result FROM control_receipts WHERE session_id=? AND request_id=?`, session, request).Scan(&digest, &action, &prompt, &delivery, &run, &state, &result)
	return
}

func createExecutionRun(ctx context.Context, tx *sql.Tx, project, session, run, prompt, incarnation string, owner uint64, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO execution_runs(id,project_id,session_id,prompt_id,incarnation_id,owner,state,started_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, run, project, session, prompt, incarnation, owner, "starting", now)
	return err
}

func createDelivery(ctx context.Context, tx *sql.Tx, project, session, prompt, request, command, run, channel string, now string) (string, string, error) {
	if channel != "normal" && channel != "steer" {
		return "", "", ErrInvalid
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(acceptance_sequence),0)+1 FROM input_deliveries WHERE session_id=?`, session).Scan(&sequence); err != nil {
		return "", "", err
	}
	id, token := uuid.NewString(), uuid.NewString()
	_, err := tx.ExecContext(ctx, `INSERT INTO input_deliveries(id,project_id,session_id,prompt_id,request_id,command_id,run_id,input_token,channel,state,acceptance_sequence,accepted_at) VALUES(?,?,?,?,?,?,?,?,?,'accepted',?,?)`, id, project, session, prompt, request, command, run, token, channel, sequence, now)
	return id, token, err
}

func markDeliveryDispatching(ctx context.Context, tx *sql.Tx, command string, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE input_deliveries SET state='dispatching' WHERE command_id=? AND state='accepted'`, command)
	return err
}

func markDeliveryAck(ctx context.Context, tx *sql.Tx, command string, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE input_deliveries SET state=CASE WHEN state='consumed' THEN state ELSE 'native_queued' END,ack_at=? WHERE command_id=? AND state IN ('accepted','dispatching','native_queued','consumed')`, now, command)
	return err
}

func consumeDelivery(ctx context.Context, tx *sql.Tx, i Incarnation, token string, run string, sequence uint64, payload []byte, now string) error {
	var delivery, project, session, prompt, request, state, expectedRun, channel, content string
	if err := tx.QueryRowContext(ctx, `SELECT d.id,d.project_id,d.session_id,d.prompt_id,d.request_id,d.state,d.run_id,d.channel,t.content FROM input_deliveries d JOIN turns t ON t.id=d.prompt_id WHERE d.input_token=?`, token).Scan(&delivery, &project, &session, &prompt, &request, &state, &expectedRun, &channel, &content); err != nil {
		return err
	}
	if project != i.ProjectID || session != i.SessionID || expectedRun != run {
		return ErrConflict
	}
	if state == "consumed" {
		return nil
	}
	if state == "removed" || state == "rejected" {
		return ErrConflict
	}
	if err := changed(tx.ExecContext(ctx, `UPDATE input_deliveries SET state='consumed',consumed_sequence=?,consumed_at=?,error='' WHERE id=? AND state IN ('accepted','dispatching','native_queued','unknown')`, sequence, now, delivery)); err != nil {
		return err
	}
	if err := sMessage(ctx, tx, i, prompt, prompt+"/user", "user", content, payload, now); err != nil {
		return err
	}
	newState := "completed"
	if channel == "normal" {
		newState = "running"
	}
	_, err := tx.ExecContext(ctx, `UPDATE turns SET state=?,updated_at=? WHERE id=? AND state IN ('held','queued','starting','running')`, newState, now, prompt)
	return err
}

func sMessage(ctx context.Context, tx *sql.Tx, i Incarnation, turn, id, role, content string, native []byte, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO messages(id,project_id,session_id,turn_id,role,content,native_message,sequence,created_at) VALUES(?,?,?,?,?,?,?,(SELECT coalesce(max(sequence),0)+1 FROM messages WHERE session_id=?),?) ON CONFLICT(id) DO NOTHING`, id, i.ProjectID, i.SessionID, turn, role, content, native, i.SessionID, now)
	return err
}

func settleExecutionRun(ctx context.Context, tx *sql.Tx, run, outcome, now string) error {
	var started string
	if err := tx.QueryRowContext(ctx, `SELECT started_at FROM execution_runs WHERE id=?`, run).Scan(&started); err != nil {
		return err
	}
	// Timestamps are ISO strings in this store. The durable elapsed value is
	// populated by the runtime event in later protocol work; zero is explicit.
	_, err := tx.ExecContext(ctx, `UPDATE execution_runs SET state=CASE WHEN ?='failed' THEN 'failed' ELSE 'settled' END,outcome=?,settled_at=? WHERE id=? AND state IN ('starting','running','stopping','unknown')`, outcome, outcome, now, run)
	return err
}

var errNoDelivery = errors.New("delivery record missing")

func deliveryForCommand(ctx context.Context, tx *sql.Tx, command string) (string, error) {
	var token string
	if err := tx.QueryRowContext(ctx, `SELECT input_token FROM input_deliveries WHERE command_id=?`, command).Scan(&token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errNoDelivery
		}
		return "", err
	}
	return token, nil
}

func validateReceiptIntent(existingDigest, expected string) error {
	if existingDigest != expected {
		return fmt.Errorf("request_id reused with a different intent: %w", ErrConflict)
	}
	return nil
}
