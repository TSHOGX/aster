package product

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/content"
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
	RuntimeConfigurationDigest string `json:"runtime_configuration_digest,omitempty"`
	Mode                       string `json:"mode"`
	CompatibilityDigest        string `json:"compatibility_digest,omitempty"`
	SchemaDigest               string `json:"schema_digest,omitempty"`
	GatewayIdentity            string `json:"gateway_identity,omitempty"`
	gatewaySnapshot            string

	Generation         int64  `json:"generation"`
	Owner              string `json:"owner"`
	Phase              string `json:"phase"`
	Deadline           string `json:"deadline"`
	Reason             string `json:"reason"`
	UpdatedAt          string `json:"updated_at"`
	ActivationInstance string `json:"activation_instance,omitempty"`
	TargetDigest       string `json:"target_digest,omitempty"`
	FinishedDigest     string `json:"finished_digest,omitempty"`
}

func releaseState(ctx context.Context, q queryer) (out ReleaseState, err error) {
	err = q.QueryRowContext(ctx, `SELECT generation,owner,phase,deadline,reason,updated_at,activation_instance,target_digest,finished_digest,mode,compatibility_digest,schema_digest,gateway_snapshot,runtime_configuration_digest FROM application_release WHERE id=1`).Scan(&out.Generation, &out.Owner, &out.Phase, &out.Deadline, &out.Reason, &out.UpdatedAt, &out.ActivationInstance, &out.TargetDigest, &out.FinishedDigest, &out.Mode, &out.CompatibilityDigest, &out.SchemaDigest, &out.gatewaySnapshot, &out.RuntimeConfigurationDigest)
	out.GatewayIdentity = snapshotDigest(out.gatewaySnapshot)
	return
}
func (s *Store) ReleaseState(ctx context.Context) (ReleaseState, error) {
	return releaseState(ctx, s.reader)
}

// BeginReleaseDrain closes admission atomically with the new persisted owner.
// Repeating the same owner is idempotent and cannot extend its deadline.
func (s *Store) BeginReleaseDrain(ctx context.Context, owner string, timeout time.Duration) (ReleaseState, error) {
	return s.beginReleaseDrain(ctx, owner, timeout, "quiescent", "", "", nil)
}
func (s *Store) beginReleaseDrain(ctx context.Context, owner string, timeout time.Duration, mode, target, compatibility string, proof *GatewayObservation) (out ReleaseState, err error) {
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
		snapshot, schema, configuration := "", "", ""
		if mode == "owner" {
			if proof == nil {
				return ErrInvalid
			}
			snapshot, e = checkGatewayObservation(ctx, tx, *proof)
			if e != nil {
				return e
			}
			configuration, e = runtimeConfigurationDigest(ctx, tx)
			if e != nil {
				return e
			}
			_, schema, e = SchemaIdentity()
			if e != nil {
				return e
			}
		}
		if current.Phase != "open" {
			if current.Owner == owner && current.Phase == "draining" && current.Mode == mode && (mode != "owner" || (current.TargetDigest == target && current.CompatibilityDigest == compatibility && current.gatewaySnapshot == snapshot && current.RuntimeConfigurationDigest == configuration)) {
				out = current
				return nil
			}
			return ErrConflict
		}
		var used bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM release_coordinators WHERE owner=?)`, owner).Scan(&used); e != nil {
			return e
		}
		if used {
			return ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO release_coordinators(owner,generation) VALUES(?,?)`, owner, current.Generation+1); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE application_release SET generation=generation+1,owner=?,phase='draining',deadline=?,reason='',activation_instance='',target_digest=?,finished_digest='',mode=?,compatibility_digest=?,schema_digest=?,gateway_snapshot=?,runtime_configuration_digest=?,updated_at=? WHERE id=1`, owner, sqlitex.FormatTime(s.now().Add(timeout)), target, mode, compatibility, schema, snapshot, configuration, s.timestamp())
		if e != nil {
			return e
		}
		out, e = releaseState(ctx, tx)
		return e
	})
	return
}

// runtime.mu and the writer transaction must both be held by the caller.
func (s *Store) releaseQuiescent(ctx context.Context, tx *sql.Tx) error {
	for _, count := range s.runtime.counts {
		if count > 0 {
			return ErrBusy
		}
	}
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM release_blockers)`).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return ErrBusy
	}
	return nil
}

func (s *Store) ActivateRelease(ctx context.Context, owner string, generation int64, target string, proofs ...GatewayObservation) (out ReleaseState, err error) {
	if !content.ValidDigest(target) || s.instance == "" {
		return out, ErrInvalid
	}
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
		if current.Phase == "activating" && current.ActivationInstance == s.instance && current.TargetDigest == target {
			out = current
			return nil
		}
		if current.Mode == "owner" && current.TargetDigest != target {
			return ErrConflict
		}
		if current.Phase != "ready" {
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
		if e = s.releaseEligible(ctx, tx, current, proofs, false); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE application_release SET phase='activating',activation_instance=?,target_digest=?,reason='',updated_at=? WHERE id=1`, s.instance, target, s.timestamp())
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

// FinishRelease is called only by the replacement application with its measured
// executable digest. Only the selected target can complete activation.
func (s *Store) FinishRelease(ctx context.Context, owner string, generation int64, digest string, proofs ...GatewayObservation) (out ReleaseState, err error) {
	if !content.ValidDigest(digest) || s.instance == "" {
		return out, ErrInvalid
	}
	if s.identity.ExecutableDigest != "" && digest != s.identity.ExecutableDigest {
		return out, ErrConflict
	}
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	err = s.writeClass(ctx, urgentWrite, "", 1024, func(tx *sql.Tx) error {
		current, e := releaseState(ctx, tx)
		if e != nil {
			return e
		}
		if current.Owner != owner || current.Generation != generation || generation < 1 || current.ActivationInstance == "" || current.ActivationInstance == s.instance {
			return ErrConflict
		}
		reason := "completed"
		if current.TargetDigest != digest {
			return ErrConflict
		}
		if current.Phase == "open" && current.Reason == reason && current.FinishedDigest == digest {
			out = current
			return nil
		}
		if current.Phase != "activating" {
			return ErrConflict
		}
		if e = s.releaseEligible(ctx, tx, current, proofs, true); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE application_release SET phase='open',deadline='',reason=?,finished_digest=?,updated_at=? WHERE id=1`, reason, digest, s.timestamp())
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
func (s *Store) TryReleaseReady(ctx context.Context, owner string, generation int64, proofs ...GatewayObservation) (out ReleaseState, err error) {
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
		if e = s.releaseEligible(ctx, tx, current, proofs, false); e != nil {
			return e
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
	HandoffBlockers map[string]int64 `json:"handoff_blockers"`
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
	if out.Mode == "owner" {
		rows, e := tx.QueryContext(ctx, `SELECT kind,count(*) FROM owner_handoff_blockers GROUP BY kind`)
		if e != nil {
			return out, e
		}
		out.HandoffBlockers = map[string]int64{}
		for rows.Next() {
			var kind string
			var count int64
			if e = rows.Scan(&kind, &count); e != nil {
				rows.Close()
				return out, e
			}
			out.HandoffBlockers[kind] = count
		}
		if e = errors.Join(rows.Err(), rows.Close()); e != nil {
			return out, e
		}
	}
	return out, tx.Commit()
}
