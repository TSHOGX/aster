package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	_, err := tx.ExecContext(ctx, `INSERT INTO execution_runs(id,project_id,session_id,prompt_id,incarnation_id,owner,state,started_at) VALUES(?,?,?,?,?,?,?,?)`, run, project, session, prompt, incarnation, owner, "starting", now)
	return err
}

func createDelivery(ctx context.Context, tx *sql.Tx, project, session, prompt, request, command, run, channel string, now string) (string, string, error) {
	if channel != "normal" && channel != "steer" {
		return "", "", ErrInvalid
	}
	// The same transaction and unique claim index serve normal dispatch and Steer.
	if err := changed(tx.ExecContext(ctx, `UPDATE turns SET disposition='claimed',updated_at=? WHERE id=? AND session_id=? AND disposition='pending'`, now, prompt, session)); err != nil {
		return "", "", ErrConflict
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
	_, err := tx.ExecContext(ctx, `UPDATE input_deliveries SET state=CASE WHEN state IN ('accepted','dispatching','native_queued','unknown') AND NOT EXISTS(SELECT 1 FROM execution_runs r WHERE r.id=input_deliveries.run_id AND r.reconciled_at<>'') THEN 'native_queued' ELSE state END,ack_at=? WHERE command_id=?`, now, command)
	return err
}

func consumeDelivery(ctx context.Context, tx *sql.Tx, i Incarnation, token string, run string, sequence uint64, payload []byte, now string, elapsed *int64) error {
	if _, err := time.Parse(time.RFC3339Nano, now); err != nil {
		return ErrInvalid
	}
	if elapsed != nil && *elapsed < 0 {
		return ErrInvalid
	}
	var delivery, project, session, prompt, request, state, expectedRun, channel, content string
	if err := tx.QueryRowContext(ctx, `SELECT d.id,d.project_id,d.session_id,d.prompt_id,d.request_id,d.state,d.run_id,d.channel,t.content FROM input_deliveries d JOIN turns t ON t.id=d.prompt_id WHERE d.input_token=?`, token).Scan(&delivery, &project, &session, &prompt, &request, &state, &expectedRun, &channel, &content); err != nil {
		return err
	}
	var incarnation string
	if err := tx.QueryRowContext(ctx, `SELECT incarnation_id FROM execution_runs WHERE id=?`, run).Scan(&incarnation); err != nil {
		return err
	}
	if project != i.ProjectID || session != i.SessionID || expectedRun != run || incarnation != i.ID {
		return ErrConflict
	}
	if state == "consumed" {
		return nil
	}
	if state == "removed" || state == "rejected" {
		return ErrConflict
	}
	if err := changed(tx.ExecContext(ctx, `UPDATE input_deliveries SET state='consumed',consumed_sequence=?,consumed_at=?,consumed_elapsed_ms=?,error='' WHERE id=? AND state IN ('accepted','dispatching','native_queued','unknown')`, sequence, now, elapsed, delivery)); err != nil {
		return err
	}
	if err := sMessage(ctx, tx, i, prompt, prompt+"/user", "user", content, payload, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE turns SET disposition='consumed',updated_at=? WHERE id=? AND disposition='claimed'`, now, prompt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_runs SET state=CASE WHEN state='starting' THEN 'running' ELSE state END WHERE id=?`, run)
	return err
}

// handleDelivery records successful extension handling. It is terminal input
// evidence: the intent must never return to the dispatchable queue, but it is
// deliberately not a transcript/model-consumption anchor.
func handleDelivery(ctx context.Context, tx *sql.Tx, i Incarnation, token, run, now string) error {
	var delivery, prompt, expectedRun, state string
	if err := tx.QueryRowContext(ctx, `SELECT d.id,d.prompt_id,d.run_id,d.state FROM input_deliveries d WHERE d.input_token=? AND d.session_id=?`, token, i.SessionID).Scan(&delivery, &prompt, &expectedRun, &state); err != nil {
		return err
	}
	if expectedRun != run || state == "consumed" || state == "removed" || state == "rejected" {
		return ErrConflict
	}
	if state == "handled" {
		return nil
	}
	if err := changed(tx.ExecContext(ctx, `UPDATE input_deliveries SET state='handled',consumed_at=?,error='' WHERE id=? AND state IN ('accepted','dispatching','native_queued','unknown')`, now, delivery)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE turns SET disposition='handled',updated_at=? WHERE id=? AND disposition='claimed'`, now, prompt)
	return err
}

func sMessage(ctx context.Context, tx *sql.Tx, i Incarnation, turn, id, role, content string, native []byte, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO messages(id,project_id,session_id,turn_id,role,content,native_message,sequence,created_at) VALUES(?,?,?,?,?,?,?,(SELECT coalesce(max(sequence),0)+1 FROM messages WHERE session_id=?),?)`, id, i.ProjectID, i.SessionID, turn, role, content, native, i.SessionID, now)
	return err
}

func settleExecutionRun(ctx context.Context, tx *sql.Tx, run, outcome, now string) error {
	// A command settlement without full native disposition is retained as unknown.
	var reconciled string
	if err := tx.QueryRowContext(ctx, `SELECT reconciled_at FROM execution_runs WHERE id=?`, run).Scan(&reconciled); err != nil {
		return err
	}
	state := "settled"
	if outcome == "failed" {
		state = "failed"
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM input_deliveries WHERE run_id=? AND state IN ('accepted','dispatching','native_queued','unknown')`, run).Scan(&unresolved); err != nil {
		return err
	}
	if reconciled == "" || unresolved > 0 {
		state = "unknown"
		outcome = "unknown"
	}
	_, err := tx.ExecContext(ctx, `UPDATE execution_runs SET state=?,outcome=?,settled_at=CASE WHEN ?='' THEN settled_at ELSE ? END WHERE id=? AND state IN ('starting','running','stopping','unknown')`, state, outcome, now, now, run)
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
		return Admission("request_id_reused", fmt.Errorf("request_id reused with a different intent: %w", ErrConflict))
	}
	return nil
}

// markRunUnknown never erases immutable consumption evidence.
func markRunUnknown(ctx context.Context, tx *sql.Tx, session string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM execution_runs WHERE session_id=? AND state IN ('starting','running','stopping','unknown')`, session)
	if err != nil {
		return err
	}
	var runs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		runs = append(runs, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, run := range runs {
		if err = retireUnsentInputs(ctx, tx, run, "process lost before dispatch", ""); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE input_deliveries SET state='unknown',error='native outcome unknown' WHERE session_id=? AND state IN ('dispatching','native_queued')`, session); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE execution_runs SET state='unknown',outcome='unknown' WHERE session_id=? AND state IN ('starting','running','stopping')`, session)
	return err
}

func restoreUndelivered(ctx context.Context, tx *sql.Tx, command, state, reason, now string) error {
	_, err := tx.ExecContext(ctx, `UPDATE input_deliveries SET state=?,removed_at=?,error=? WHERE command_id=? AND state IN ('accepted','dispatching','native_queued','unknown')`, state, now, reason, command)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE turns SET disposition='pending',queue_policy='held',updated_at=? WHERE id IN (SELECT prompt_id FROM input_deliveries WHERE command_id=? AND state IN ('removed','rejected')) AND disposition='claimed'`, now, command)
	return err
}

// A late rejection can complete the barrier after command.completed was ingested.
func finishReconciledRun(ctx context.Context, tx *sql.Tx, session string) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.id,coalesce(c.payload,'{"outcome":"failed"}') FROM execution_runs r JOIN agent_commands a ON a.id=r.id LEFT JOIN agent_records c ON c.command_id=r.id AND c.kind='command.completed' WHERE r.session_id=? AND r.state='unknown' AND r.reconciled_at<>'' AND (c.command_id IS NOT NULL OR a.state='rejected')`, session)
	if err != nil {
		return err
	}
	type completed struct {
		id  string
		raw []byte
	}
	var list []completed
	for rows.Next() {
		var c completed
		if err = rows.Scan(&c.id, &c.raw); err != nil {
			break
		}
		list = append(list, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, c := range list {
		var evidence struct{ Outcome string }
		if err = json.Unmarshal(c.raw, &evidence); err != nil {
			return err
		}
		if evidence.Outcome == "" {
			evidence.Outcome = "completed"
		}
		if err = settleExecutionRun(ctx, tx, c.id, evidence.Outcome, ""); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET state='idle',failure='' WHERE id=? AND deletion_intent=0 AND NOT EXISTS(SELECT 1 FROM execution_runs WHERE session_id=? AND state IN ('starting','running','stopping','unknown') AND acknowledged=0)`, session, session)
	return err
}

// Pending is a host fact: agentCommand changes it to sending in the same
// transaction before any transport write. It is safe to withdraw only here.
func retireUnsentInputs(ctx context.Context, tx *sql.Tx, run, reason, now string) error {
	if now == "" {
		now = time.Now().UTC().Format(time.RFC3339Nano)
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.command_id FROM input_deliveries d JOIN agent_commands c ON c.id=d.command_id WHERE d.run_id=? AND d.state='accepted' AND c.state='pending'`, run)
	if err != nil {
		return err
	}
	var commands []string
	for rows.Next() {
		var command string
		if err = rows.Scan(&command); err != nil {
			break
		}
		commands = append(commands, command)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, command := range commands {
		if err = restoreUndelivered(ctx, tx, command, "removed", reason, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE agent_commands SET state='rejected',error=? WHERE id=?`, reason, command); err != nil {
			return err
		}
	}
	return nil
}
