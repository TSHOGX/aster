package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"aster/contracts/evolution"
)

// resetCorpusCollectors establishes the baseline in the activation transaction,
// before the first worker tick. Unchanged enabled sources retain pending work.
func resetCorpusCollectors(ctx context.Context, tx *sql.Tx, config evolution.Configuration, enabled bool) error {
	var baseline, through int64
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0) FROM corpus_discovery_events").Scan(&baseline); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(rowid),0) FROM corpus_episodes").Scan(&through); err != nil {
		return err
	}
	selected := map[string]bool{}
	if enabled {
		for _, binding := range config.Bindings {
			if binding.Enabled && binding.Trigger != "manual" {
				for _, id := range binding.SourceIDs {
					selected[id] = true
				}
			}
		}
	}
	for _, source := range config.Sources {
		if !selected[source.ID] || source.Kind == "volume" {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO corpus_collectors(group_id,source_id,source_revision,configuration_revision,baseline,after_event,sealed_through) VALUES(?,?,?,?,?,?,?)
   ON CONFLICT(group_id,source_id) DO UPDATE SET configuration_revision=excluded.configuration_revision,
   baseline=CASE WHEN source_revision=excluded.source_revision THEN baseline ELSE excluded.baseline END,
   after_event=CASE WHEN source_revision=excluded.source_revision THEN after_event ELSE excluded.after_event END,
   sealed_through=CASE WHEN source_revision=excluded.source_revision THEN sealed_through ELSE excluded.sealed_through END,
   snapshot_id=CASE WHEN source_revision=excluded.source_revision THEN snapshot_id ELSE '' END,
   source_revision=excluded.source_revision`, config.GroupID, source.ID, source.PolicyRevision, config.Revision, baseline, baseline, through)
		if err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM corpus_collectors WHERE group_id=? AND configuration_revision<>?", config.GroupID, config.Revision)
	return err
}

type CorpusCollectorStatus struct {
	SourceID              string `json:"source_id"`
	SourceRevision        int64  `json:"source_revision"`
	ConfigurationRevision int64  `json:"configuration_revision"`
	Baseline              int64  `json:"baseline_event"`
	AfterEvent            int64  `json:"after_event"`
	SealedThrough         int64  `json:"sealed_through"`
	SnapshotID            string `json:"snapshot_id"`
}

func (s *Store) CorpusCollectors(ctx context.Context, token, group string) ([]CorpusCollectorStatus, error) {
	result := []CorpusCollectorStatus{}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT source_id,source_revision,configuration_revision,baseline,after_event,sealed_through,snapshot_id FROM corpus_collectors WHERE group_id=? ORDER BY source_id", group)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var status CorpusCollectorStatus
			if err := rows.Scan(&status.SourceID, &status.SourceRevision, &status.ConfigurationRevision, &status.Baseline, &status.AfterEvent, &status.SealedThrough, &status.SnapshotID); err != nil {
				return err
			}
			result = append(result, status)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// CorpusCollector is an internal service worker. It uses the activation author's
// current grants directly; it creates neither a login nor an employee session.
type CorpusCollector struct {
	Store                     *Store
	groupCursor, sourceCursor string
}

func (w *CorpusCollector) Step(ctx context.Context) error {
	rows, err := w.Store.reader.QueryContext(ctx, `SELECT c.group_id,c.source_id FROM corpus_collectors c
 JOIN evolution_group_configs g ON g.group_id=c.group_id AND g.active_revision=c.configuration_revision
 WHERE g.state='active' AND g.auto_enabled=1 AND (c.group_id>? OR (c.group_id=? AND c.source_id>?))
 ORDER BY c.group_id,c.source_id LIMIT 8`, w.groupCursor, w.groupCursor, w.sourceCursor)
	if err != nil {
		return err
	}
	type key struct{ group, source string }
	var keys []key
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.group, &k.source); err != nil {
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
		w.sourceCursor = ""
		return nil
	}
	var failures []error
	for _, k := range keys {
		w.groupCursor, w.sourceCursor = k.group, k.source
		if err := w.Store.collectCorpusSource(ctx, k.group, k.source); err != nil {
			failures = append(failures, fmt.Errorf("corpus source %s/%s: %w", k.group, k.source, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Store) collectCorpusSource(ctx context.Context, group, sourceID string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var actor Account
		var revision, baseline, after, through int64
		if err := tx.QueryRowContext(ctx, `SELECT a.id,a.role,c.source_revision,c.baseline,c.after_event,c.sealed_through
   FROM corpus_collectors c JOIN evolution_group_configs g ON g.group_id=c.group_id AND g.active_revision=c.configuration_revision
   JOIN evolution_configuration_revisions r ON r.group_id=g.group_id AND r.revision=g.active_revision
   JOIN accounts a ON a.id=r.actor_id
   WHERE c.group_id=? AND c.source_id=? AND g.state='active' AND g.auto_enabled=1`, group, sourceID).Scan(&actor.ID, &actor.Role, &revision, &baseline, &after, &through); err != nil {
			return missing(err)
		}
		if err := authorizeEvolutionGroup(ctx, tx, actor, group, "config.manage"); err != nil {
			return err
		}
		if err := authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
			return err
		}
		source, err := sourcePolicy(ctx, tx, group, sourceID, revision)
		if err != nil {
			return err
		}
		// Bound the global event page, including unrelated events. Empty filtered pages
		// still advance, so an unchanged first page cannot starve later executions.
		rows, err := tx.QueryContext(ctx, `SELECT d.sequence,e.id,
   EXISTS(SELECT 1 FROM corpus_discovery_events first WHERE first.execution_id=e.id AND first.kind='terminal' AND first.sequence>?)
   FROM (SELECT sequence,execution_id FROM corpus_discovery_events WHERE sequence>? ORDER BY sequence LIMIT 32) d
   LEFT JOIN execution_runs e ON e.id=d.execution_id AND e.state IN ('settled','failed','unknown')
   AND ((?='project' AND e.project_id=?) OR (?='session' AND e.session_id=?))
   LEFT JOIN sessions session ON session.id=e.session_id
   LEFT JOIN projects project ON project.id=e.project_id
   WHERE e.id IS NULL OR (session.deletion_intent=0 AND project.state NOT IN ('deleting','deleted')) ORDER BY d.sequence`, baseline, after, source.Kind, source.ResourceID, source.Kind, source.ResourceID)
		if err != nil {
			return err
		}
		type event struct {
			sequence  int64
			execution sql.NullString
			eligible  bool
		}
		var events []event
		for rows.Next() {
			var e event
			if err = rows.Scan(&e.sequence, &e.execution, &e.eligible); err != nil {
				break
			}
			events = append(events, e)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		// Advance across deleted-source events as well; authorization above prevents
		// capture of a source deleted since activation.
		var next int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),?) FROM (SELECT sequence FROM corpus_discovery_events WHERE sequence>? ORDER BY sequence LIMIT 32)", after, after).Scan(&next); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, e := range events {
			if !e.execution.Valid || !e.eligible || seen[e.execution.String] {
				continue
			}
			seen[e.execution.String] = true
			var session, turn string
			if err = tx.QueryRowContext(ctx, "SELECT session_id,prompt_id FROM execution_runs WHERE id=?", e.execution.String).Scan(&session, &turn); err != nil {
				return err
			}
			if sourceExcludesEpisode(source, session, turn, e.execution.String) {
				continue
			}

			_, err = s.captureCorpusEpisode(ctx, tx, actor, group, CorpusCaptureInput{RequestID: corpusDiscoveryRequest("capture", group, sourceID, revision, baseline, e.sequence, 0), SourceID: sourceID, SourceRevision: revision, ExecutionID: e.execution.String})
			if err != nil {
				return err
			}
		}
		snapshot, err := s.sealCorpusSnapshot(ctx, tx, actor, group, CorpusSnapshotInput{RequestID: corpusDiscoveryRequest("seal", group, sourceID, revision, baseline, next, through), SourceID: sourceID, SourceRevision: revision, AfterSequence: through, Limit: 32})
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil {
			// Captures, sealed bytes/grants and cursor commit together. Each goal's
			// successful consumption cursor remains separate from this capture cursor.
			if _, err = tx.ExecContext(ctx, "UPDATE corpus_collectors SET sealed_through=?,snapshot_id=? WHERE group_id=? AND source_id=?", snapshot.Through, snapshot.ID, group, sourceID); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE corpus_collectors SET after_event=? WHERE group_id=? AND source_id=?", next, group, sourceID)
		return err
	})
}

func corpusDiscoveryRequest(action, group, source string, revision, baseline, event, through int64) string {
	return "collector-" + evolution.Digest([]byte(fmt.Sprintf("%s/%s/%s/%d/%d/%d/%d", action, group, source, revision, baseline, event, through)))[7:]
}
