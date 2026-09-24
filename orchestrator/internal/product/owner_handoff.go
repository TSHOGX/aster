package product

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/content"
)

// OwnerHandoffProtocol changes whenever retained native work cannot safely move
// between owner builds even with identical schema, runtime and configuration.
const OwnerHandoffProtocol = 2

// GatewayObservation is collected by the host from its protected gateway socket,
// never decoded from a release coordinator's request. Started retains monotonic
// time so a slow observation cannot become a fresh proof after its RPC returns.
type GatewayObservation struct {
	Started       time.Time
	Registrations map[string]GatewayRegistration
}

func checkGatewayObservation(ctx context.Context, q queryer, proof GatewayObservation) (string, error) {
	if proof.Started.IsZero() || time.Since(proof.Started) < 0 || time.Since(proof.Started) > 2*time.Second || len(proof.Registrations) > 64 {
		return "", ErrBusy
	}
	for audience, expected := range proof.Registrations {
		if !validID(audience) || !validID(expected.Instance) || expected.Generation < 1 || expected.Slots < 1 || expected.Slots > 256 {
			return "", ErrInvalid
		}
		current, err := gatewayRegistration(ctx, q, audience)
		if err != nil {
			return "", err
		}
		if current != expected {
			return "", ErrConflict
		}
	}
	// A configured snapshot cannot omit a gateway with an outstanding request.
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT audience FROM service_request_slots WHERE closed=0`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var audience string
		if err = rows.Scan(&audience); err != nil {
			return "", err
		}
		if _, ok := proof.Registrations[audience]; !ok {
			return "", ErrConflict
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	// Canonical JSON is deterministic across independently collected observations.
	registrations := proof.Registrations
	if registrations == nil {
		registrations = map[string]GatewayRegistration{}
	}
	raw, err := json.Marshal(registrations)
	return string(raw), err
}

func snapshotDigest(snapshot string) string {
	if snapshot == "" {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(snapshot)))
}

func (s *Store) BeginOwnerHandoff(ctx context.Context, owner string, timeout time.Duration, target, compatibility string, proof GatewayObservation) (ReleaseState, error) {
	if !content.ValidDigest(target) || !content.ValidDigest(compatibility) || compatibility != s.identity.CompatibilityDigest {
		return ReleaseState{}, ErrInvalid
	}
	return s.beginReleaseDrain(ctx, owner, timeout, "owner", target, compatibility, &proof)
}

func (s *Store) releaseEligible(ctx context.Context, tx *sql.Tx, state ReleaseState, proofs []GatewayObservation, completing bool) error {
	if state.Mode != "owner" {
		return s.releaseQuiescent(ctx, tx)
	}
	if state.CompatibilityDigest == "" || state.CompatibilityDigest != s.identity.CompatibilityDigest || len(proofs) != 1 {
		return ErrConflict
	}
	_, schema, err := SchemaIdentity()
	if err != nil {
		return err
	}
	if schema != state.SchemaDigest {
		return ErrConflict
	}
	snapshot, err := checkGatewayObservation(ctx, tx, proofs[0])
	if err != nil {
		return err
	}
	if snapshot != state.gatewaySnapshot {
		// A selected replacement can finish after gateway loss only once all
		// retained work is fully settled; activation still requires continuity.
		if completing {
			return s.releaseQuiescent(ctx, tx)
		}
		return ErrConflict
	}
	for _, count := range s.runtime.counts {
		if count > 0 {
			return ErrBusy
		}
	}
	var blocked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_handoff_blockers)`).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return ErrBusy
	}
	return nil
}
