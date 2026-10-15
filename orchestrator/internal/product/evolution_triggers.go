package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"aster/contracts/evolution"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sqlitex"
)

// Trigger ranges describe the chosen source delta. A dispatcher must seal these
// exact bounds and freeze the assignment before any remote admission attempt.
type EvolutionTriggerRange struct {
	SourceID       string `json:"source_id"`
	SourceRevision int64  `json:"source_revision"`
	After          int64  `json:"after_sequence"`
	Through        int64  `json:"through_sequence"`
}
type EvolutionTrigger struct {
	ID                    string                  `json:"id"`
	BindingID             string                  `json:"binding_id"`
	ConfigurationRevision int64                   `json:"configuration_revision"`
	Kind                  string                  `json:"kind"`
	State                 string                  `json:"state"`
	FirstAt               string                  `json:"first_at"`
	LatestAt              string                  `json:"latest_at"`
	Ranges                []EvolutionTriggerRange `json:"ranges"`
	RunID                 string                  `json:"run_id"`
}

func (s *Store) resetEvolutionTriggers(ctx context.Context, tx *sql.Tx, config evolution.Configuration, enabled bool) error {
	if err := clearEvolutionTriggers(ctx, tx, config.GroupID); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	var baseline int64
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(rowid),0) FROM corpus_episodes").Scan(&baseline); err != nil {
		return err
	}
	for _, binding := range config.Bindings {
		if !binding.Enabled || binding.Trigger == "manual" {
			continue
		}
		next := ""
		if binding.Trigger == "schedule" {
			schedule, _, err := parseRecurrence(binding.Schedule, binding.Timezone)
			if err != nil {
				return err
			}
			nextAt := schedule.Next(s.now())
			if nextAt.IsZero() {
				return ErrInvalid
			}
			next = sqlitex.FormatTime(nextAt)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO evolution_trigger_clocks VALUES(?,?,?,?)", config.GroupID, binding.ID, config.Revision, next); err != nil {
			return err
		}
		for _, id := range binding.SourceIDs {
			for _, source := range config.Sources {
				if source.ID != id {
					continue
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO evolution_trigger_sources VALUES(?,?,?,?,?,?)", config.GroupID, binding.ID, id, source.PolicyRevision, baseline, baseline); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func clearEvolutionTriggers(ctx context.Context, tx *sql.Tx, group string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE evolution_trigger_occurrences SET state='superseded' WHERE group_id=? AND state='pending'", group); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM evolution_trigger_clocks WHERE group_id=?", group)
	return err
}

// EvolutionTriggers returns bounded metadata only; raw evidence requires its own
// explicit grant. The cursor is a monotonic occurrence row sequence, not run state.
type EvolutionTriggerPage struct {
	Items []EvolutionTrigger `json:"items"`
	Next  int64              `json:"next"`
}

func (s *Store) EvolutionTriggers(ctx context.Context, token, group string, after int64) (EvolutionTriggerPage, error) {
	result := EvolutionTriggerPage{Items: []EvolutionTrigger{}, Next: after}
	if after < 0 {
		return result, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT rowid,id,binding_id,configuration_revision,trigger_kind,state,first_at,latest_at,ranges,run_id FROM evolution_trigger_occurrences WHERE group_id=? AND rowid>? ORDER BY rowid LIMIT 50", group, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item EvolutionTrigger
			var raw []byte
			if err = rows.Scan(&result.Next, &item.ID, &item.BindingID, &item.ConfigurationRevision, &item.Kind, &item.State, &item.FirstAt, &item.LatestAt, &raw, &item.RunID); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &item.Ranges); err != nil {
				return err
			}
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return EvolutionTriggerPage{}, err
	}
	return result, nil
}

type EvolutionTriggerWorker struct {
	Store                      *Store
	groupCursor, bindingCursor string
}

func (w *EvolutionTriggerWorker) Step(ctx context.Context) error {
	rows, err := w.Store.reader.QueryContext(ctx, `SELECT t.group_id,t.binding_id FROM evolution_trigger_clocks t
 JOIN evolution_group_configs g ON g.group_id=t.group_id AND g.active_revision=t.configuration_revision
 WHERE g.auto_enabled=1 AND g.state='active' AND (t.group_id>? OR (t.group_id=? AND t.binding_id>?))
 ORDER BY t.group_id,t.binding_id LIMIT 16`, w.groupCursor, w.groupCursor, w.bindingCursor)
	if err != nil {
		return err
	}
	type key struct{ group, binding string }
	var keys []key
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.group, &k.binding); err != nil {
			break
		}
		keys = append(keys, k)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		w.groupCursor = ""
		w.bindingCursor = ""
		return nil
	}
	var failures []error
	for _, k := range keys {
		w.groupCursor, w.bindingCursor = k.group, k.binding
		if err = w.Store.tickEvolutionTrigger(ctx, k.group, k.binding); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Store) tickEvolutionTrigger(ctx context.Context, group, bindingID string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var operation, next string
		if err := tx.QueryRowContext(ctx, `SELECT g.operation_id,t.next_at FROM evolution_trigger_clocks t JOIN evolution_group_configs g
   ON g.group_id=t.group_id AND g.active_revision=t.configuration_revision
   WHERE t.group_id=? AND t.binding_id=? AND g.auto_enabled=1 AND g.state='active'`, group, bindingID).Scan(&operation, &next); err != nil {
			return missing(err)
		}
		projection, err := readEvolutionProjection(ctx, tx, operation)
		if err != nil {
			return err
		}
		allowed, err := authorizeEvolutionProjection(ctx, tx, ProjectionScope{GroupID: group, OperationID: operation, Revision: projection.Configuration.Revision, Digest: projection.Digest})
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var binding evolution.Binding
		for _, b := range projection.Configuration.Bindings {
			if b.ID == bindingID {
				binding = b
				break
			}
		}
		if !binding.Enabled || binding.Trigger == "manual" || binding.ID == "" {
			return ErrConflict
		}
		now := s.now()
		due := false
		scheduledAt := next
		if binding.Trigger == "schedule" {
			anchor, err := time.Parse(time.RFC3339Nano, next)
			if err != nil {
				return err
			}
			due = !anchor.After(now)
			if due {
				schedule, _, err := parseRecurrence(binding.Schedule, binding.Timezone)
				if err != nil {
					return err
				}
				nextAt := schedule.Next(now)
				if interval, ok := schedule.(minuteInterval); ok {
					nextAt = interval.after(now, anchor)
				}
				if nextAt.IsZero() {
					return ErrInvalid
				}
				next = sqlitex.FormatTime(nextAt)
			}
		}
		ranges := []EvolutionTriggerRange{}
		rows, err := tx.QueryContext(ctx, `SELECT t.source_id,t.source_revision,t.baseline,t.observed_through,
   coalesce((SELECT max(sealed_through) FROM corpus_snapshots s WHERE s.group_id=t.group_id AND s.source_id=t.source_id AND s.source_revision=t.source_revision),0),
   coalesce((SELECT through_sequence FROM corpus_consumption c WHERE c.group_id=t.group_id AND c.binding_id=t.binding_id AND c.source_id=t.source_id),0)
   FROM evolution_trigger_sources t WHERE t.group_id=? AND t.binding_id=? ORDER BY t.source_id`, group, bindingID)
		if err != nil {
			return err
		}
		fresh := false
		type sourceObservation struct {
			source  string
			through int64
		}
		var observations []sourceObservation
		for rows.Next() {
			var r EvolutionTriggerRange
			var observed, consumed int64
			if err = rows.Scan(&r.SourceID, &r.SourceRevision, &r.After, &observed, &r.Through, &consumed); err != nil {
				break
			}
			if r.Through > observed {
				fresh = true
				observations = append(observations, sourceObservation{r.SourceID, r.Through})
			}
			if consumed > r.After {
				r.After = consumed
			}
			if r.Through > r.After {
				ranges = append(ranges, r)
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for _, o := range observations {
			if _, err = tx.ExecContext(ctx, "UPDATE evolution_trigger_sources SET observed_through=? WHERE group_id=? AND binding_id=? AND source_id=?", o.through, group, bindingID, o.source); err != nil {
				return err
			}
		}
		if binding.Trigger == "schedule" {
			if _, err = tx.ExecContext(ctx, "UPDATE evolution_trigger_clocks SET next_at=? WHERE group_id=? AND binding_id=?", next, group, bindingID); err != nil {
				return err
			}
		}
		var id string
		var pendingRanges []byte
		err = tx.QueryRowContext(ctx, "SELECT id,ranges FROM evolution_trigger_occurrences WHERE group_id=? AND binding_id=? AND state='pending'", group, bindingID).Scan(&id, &pendingRanges)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		trigger := due || binding.Trigger == "event" && fresh
		// Pending work absorbs new input and successful consumption before dispatch.
		raw, _ := json.Marshal(ranges)
		if !trigger && (id == "" || bytes.Equal(raw, pendingRanges)) {
			return nil
		}
		first := sqlitex.FormatTime(now)
		if due {
			first = scheduledAt
		}
		if id == "" {
			id = uuid.NewString()
			if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_trigger_occurrences(id,group_id,binding_id,configuration_revision,trigger_kind,state,first_at,latest_at,ranges) VALUES(?,?,?,?,?,'pending',?,?,?)", id, group, bindingID, projection.Configuration.Revision, binding.Trigger, first, sqlitex.FormatTime(now), raw); err != nil {
				return err
			}
		} else {
			if _, err = tx.ExecContext(ctx, "UPDATE evolution_trigger_occurrences SET latest_at=?,ranges=? WHERE id=? AND state='pending'", sqlitex.FormatTime(now), raw, id); err != nil {
				return err
			}
		}
		return s.auditEvent(ctx, tx, "service:evolution", "evolution.trigger.coalesced", "group", group, id, projection.Configuration.Revision, map[string]any{"binding_id": bindingID, "kind": binding.Trigger, "ranges": ranges})
	})
}
