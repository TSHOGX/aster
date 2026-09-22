package product

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/sqlitex"
)

var ErrDraining = errors.New("application update is draining; new work is temporarily unavailable")
var ErrReleaseExpired = errors.New("release deadline expired; admission reopened")

func releaseFenceError(err error) error {
	if err != nil && strings.Contains(err.Error(), "application_admission_closed") {
		return ErrDraining
	}
	return err
}

type ReleaseState struct {
	Generation int64  `json:"generation"`
	Owner      string `json:"owner"`
	Phase      string `json:"phase"`
	Deadline   string `json:"deadline"`
	Reason     string `json:"reason"`
	UpdatedAt  string `json:"updated_at"`
}

func releaseState(ctx context.Context, q queryer) (out ReleaseState, err error) {
	err = q.QueryRowContext(ctx, `SELECT generation,owner,phase,deadline,reason,updated_at FROM application_release WHERE id=1`).Scan(&out.Generation, &out.Owner, &out.Phase, &out.Deadline, &out.Reason, &out.UpdatedAt)
	return
}
func (s *Store) ReleaseState(ctx context.Context) (ReleaseState, error) {
	return releaseState(ctx, s.reader)
}

// BeginReleaseDrain closes admission atomically with the new persisted owner.
// Repeating the same owner is idempotent and cannot extend its deadline.
func (s *Store) BeginReleaseDrain(ctx context.Context, owner string, timeout time.Duration) (out ReleaseState, err error) {
	if !validID(owner) || timeout < time.Second || timeout > time.Hour {
		return out, ErrInvalid
	}
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	err = s.writeClass(ctx, urgentWrite, "", 1024, func(tx *sql.Tx) error {
		current, e := releaseState(ctx, tx)
		if e != nil {
			return e
		}
		if current.Phase != "open" {
			if current.Owner == owner && current.Phase == "draining" {
				out = current
				return nil
			}
			return ErrConflict
		}
		if current.Owner == owner {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE application_release SET generation=generation+1,owner=?,phase='draining',deadline=?,reason='',updated_at=? WHERE id=1`, owner, sqlitex.FormatTime(s.now().Add(timeout)), s.timestamp())
		if e != nil {
			return e
		}
		out, e = releaseState(ctx, tx)
		return e
	})
	return
}
func (s *Store) AbortReleaseDrain(ctx context.Context, owner string, generation int64) (out ReleaseState, err error) {
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	err = s.writeClass(ctx, urgentWrite, "", 1024, func(tx *sql.Tx) error {
		current, e := releaseState(ctx, tx)
		if e != nil {
			return e
		}
		if current.Owner != owner || current.Generation != generation || generation < 1 {
			return ErrConflict
		}
		if current.Phase == "open" {
			out = current
			return nil
		}
		if current.Phase != "draining" && current.Phase != "ready" {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE application_release SET phase='open',deadline='',reason='aborted',updated_at=? WHERE id=1`, s.timestamp())
		if e != nil {
			return e
		}
		out, e = releaseState(ctx, tx)
		return e
	})
	return
}

// Expiry aborts an update, not its admitted work. It never reopens
// activating generations from a stale timer. Ready still uses the drain deadline.
func (s *Store) ExpireReleaseDrain(ctx context.Context) error {
	current, err := s.ReleaseState(ctx)
	if err != nil {
		return err
	}
	if (current.Phase != "draining" && current.Phase != "ready") || current.Deadline > s.timestamp() {
		return nil
	}
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	return s.writeClass(ctx, urgentWrite, "", 1024, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE application_release SET phase='open',deadline='',reason='deadline',updated_at=? WHERE id=1 AND generation=? AND owner=? AND phase IN ('draining','ready') AND deadline<=?`, s.timestamp(), current.Generation, current.Owner, s.timestamp())
		return err
	})
}

// TryReleaseReady closes runtime admission only after one atomic check of both
// lease counts and durable evidence. A ready result is revocable, not activation.
func (s *Store) TryReleaseReady(ctx context.Context, owner string, generation int64) (out ReleaseState, err error) {
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	err = s.writeClass(ctx, urgentWrite, "", 1024, func(tx *sql.Tx) error {
		current, e := releaseState(ctx, tx)
		if e != nil {
			return e
		}
		if generation < 1 || current.Owner != owner || current.Generation != generation || (current.Phase != "draining" && current.Phase != "ready") {
			return ErrConflict
		}
		if current.Deadline <= s.timestamp() {
			_, e = tx.ExecContext(ctx, `UPDATE application_release SET phase='open',deadline='',reason='deadline',updated_at=? WHERE id=1`, s.timestamp())
			if e != nil {
				return e
			}
			out, e = releaseState(ctx, tx)
			return e
		}
		for _, count := range s.runtime.counts {
			if count > 0 {
				return ErrBusy
			}
		}
		var blocked bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM release_blockers)`).Scan(&blocked); e != nil {
			return e
		}
		if blocked {
			return ErrBusy
		}
		_, e = tx.ExecContext(ctx, `UPDATE application_release SET phase='ready',reason='',updated_at=? WHERE id=1`, s.timestamp())
		if e != nil {
			return e
		}
		out, e = releaseState(ctx, tx)
		return e
	})
	if err == nil && out.Phase == "open" {
		err = ErrReleaseExpired
	}
	return
}

// Status is observational; only TryReleaseReady checks both sources atomically.
// A ready generation is revocable and still requires activation coordination.
type ReleaseDrainStatus struct {
	ReleaseState
	DurableBlockers map[string]int64 `json:"durable_blockers"`
	RuntimeWork     map[string]int   `json:"runtime_work"`
}

func (s *Store) ReleaseDrainStatus(ctx context.Context) (out ReleaseDrainStatus, err error) {
	out.RuntimeWork = s.RuntimeWorkStats()
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.ReleaseState, err = releaseState(ctx, tx)
	if err != nil {
		return out, err
	}
	out.DurableBlockers = map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT kind,count(*) FROM release_blockers GROUP BY kind`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind string
		var count int64
		if err = rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return out, err
		}
		if count > 0 {
			out.DurableBlockers[kind] = count
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
