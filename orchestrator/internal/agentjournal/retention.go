package agentjournal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

func criticalRecord(r Record) bool {
	if strings.HasPrefix(r.Kind, "command.") {
		return true
	}
	if r.Kind != "native" {
		return false
	}
	var event struct{ Type string }
	if json.Unmarshal(r.Payload, &event) != nil {
		return false
	}
	switch event.Type {
	case "response", "message_end", "agent_end", "tool_execution_end":
		return true
	}
	return false
}

// Digest and original result remain available for exact duplicate detection.
// Only a host ACK of the result permits releasing a resolved command's payload.
func compactCommands(ctx context.Context, tx *sql.Tx, ack uint64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,length(payload)-2 FROM commands WHERE result_sequence>0 AND result_sequence<=? AND length(payload)>2 AND (state IN ('completed','rejected') OR (state='applied' AND kind<>'prompt')) ORDER BY result_sequence,id LIMIT 32`, ack)
	if err != nil {
		return err
	}
	type old struct {
		id    string
		bytes int64
	}
	commands := []old{}
	for rows.Next() {
		var c old
		if err = rows.Scan(&c.id, &c.bytes); err != nil {
			break
		}
		commands = append(commands, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var freed int64
	for _, c := range commands {
		if _, err = tx.ExecContext(ctx, `UPDATE commands SET payload=X'7b7d',size=size-? WHERE id=?`, c.bytes, c.id); err != nil {
			return err
		}
		freed += c.bytes
	}
	if freed == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE metadata SET bytes=bytes-? WHERE id=1`, freed)
	return err
}
