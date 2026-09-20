package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/egress"
)

// RegisterEgressCompilation persists dormant exact tuples. Rows are never
// deleted or reassigned: revocation leaves a tombstone under the backend ID.
func (s *Store) RegisterEgressCompilation(ctx context.Context, compilation egress.Compilation) error {
	if len(compilation.PolicyDigest) != 64 || len(compilation.NetworkDigest) != 64 || compilation.Default != "deny" {
		return fmt.Errorf("canonical restricted egress compilation required")
	}
	if err := egress.ValidateCompilation(compilation); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := registerEgressCompilationTx(ctx, tx, compilation); err != nil {
		return err
	}
	return tx.Commit()
}
func registerEgressCompilationTx(ctx context.Context, tx *sql.Tx, compilation egress.Compilation) error {
	for _, selected := range compilation.Services {
		binding := selected.Binding
		if binding.ID == "" || binding.BackendID == "" || binding.GenerationID == "" || binding.Epoch == 0 || binding.Revision == 0 || binding.ServiceID != selected.Service.ID {
			return fmt.Errorf("complete dormant route binding required")
		}
		service, err := json.Marshal(selected.Service)
		if err != nil {
			return err
		}
		operations, err := json.Marshal(selected.Access.Operations)
		if err != nil {
			return err
		}
		var prior, policy, priorService, priorOperations string
		var backend, generation, serviceID, destination, gateway string
		var epoch, revision uint64
		err = tx.QueryRowContext(ctx, `SELECT network_digest,policy_digest,service,operations,backend_id,generation_id,epoch,revision,service_id,destination,gateway FROM egress_route_bindings WHERE binding_id=?`, binding.ID).Scan(&prior, &policy, &priorService, &priorOperations, &backend, &generation, &epoch, &revision, &serviceID, &destination, &gateway)
		if err == nil {
			if prior != compilation.NetworkDigest || policy != compilation.PolicyDigest || priorService != string(service) || priorOperations != string(operations) || backend != binding.BackendID || generation != binding.GenerationID || epoch != binding.Epoch || revision != binding.Revision || serviceID != binding.ServiceID || destination != binding.Destination.String() || gateway != binding.Gateway.String() {
				return fmt.Errorf("egress binding identity changed")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var managed int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM egress_tuple_pools WHERE store_id=?`, binding.BackendID).Scan(&managed); err != nil {
			return err
		}
		if managed != 0 {
			var raw string
			if err := tx.QueryRowContext(ctx, `SELECT bindings FROM egress_allocations WHERE generation_id=? AND store_id=?`, binding.GenerationID, binding.BackendID).Scan(&raw); err != nil {
				return fmt.Errorf("compiled route lacks managed tuple reservation: %w", err)
			}
			var reserved []egress.RuntimeBinding
			if err := json.Unmarshal([]byte(raw), &reserved); err != nil {
				return err
			}
			matched := false
			for _, candidate := range reserved {
				if candidate == binding {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("compiled route differs from managed tuple reservation")
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO egress_route_bindings(binding_id,backend_id,generation_id,epoch,revision,service_id,destination,gateway,policy_digest,network_digest,service,operations,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'dormant')`, binding.ID, binding.BackendID, binding.GenerationID, binding.Epoch, binding.Revision, binding.ServiceID, binding.Destination.String(), binding.Gateway.String(), compilation.PolicyDigest, compilation.NetworkDigest, string(service), string(operations))
		if err != nil {
			return err
		}
	}
	return nil
}

// ActivateExecutionRoutes reconciles service authority for an execution that is
// already accepted. AcceptExecutionBinding activates this generation's reserved
// routes in its own transaction, which is the normal path; this exists for the
// case where a compilation becomes known to the product after acceptance, so a
// reconciler can settle it without a fresh process.
//
// Both use the same predicate — a dormant route of this generation at the
// accepted epoch — so neither can revive a revoked tombstone.
func (s *Store) ActivateExecutionRoutes(ctx context.Context, session, generation, owner string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := acceptedExecutionTx(ctx, tx, session, generation, owner, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE egress_route_bindings SET state='active' WHERE generation_id=? AND state='dormant' AND EXISTS(SELECT 1 FROM execution_bindings b WHERE b.generation_id=egress_route_bindings.generation_id AND b.store_id=egress_route_bindings.backend_id AND b.epoch=egress_route_bindings.epoch AND b.state='accepted')`, generation)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func issueBoundGrantsTx(ctx context.Context, tx *sql.Tx, generation string, turn int64, expires time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id=? AND turn_id!=? AND state='active'`, generation, turn)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO egress_grants(grant_id,binding_id,generation_id,turn_id,policy_digest,binding_revision,expires_at,state)
 SELECT binding_id || ':' || ? || coalesce((SELECT ':' || a.revision FROM active_execution_adoptions a WHERE a.generation_id=egress_route_bindings.generation_id),''),binding_id,generation_id,?,policy_digest,revision,?,'active' FROM egress_route_bindings WHERE generation_id=? AND state='active' AND NOT EXISTS(SELECT 1 FROM execution_turn_commands c WHERE c.generation_id=egress_route_bindings.generation_id AND c.turn_id=? AND c.kind='abort')
 ON CONFLICT(grant_id) DO UPDATE SET expires_at=excluded.expires_at WHERE egress_grants.state='active'`, turn, turn, formatTime(expires), generation, turn)
	return err
}

func (s *Store) AuthorizeEgressRequest(ctx context.Context, p egress.AuthorizationRequest) (egress.Authorization, error) {
	// The destination is the authority: the product reserved one address per
	// service, so a kernel-derived destination names the service unambiguously.
	// ServiceID is an optional cross-check for an adapter that knows it, never a
	// way for a caller to select a different service than the one it reached.
	if p.BackendID == "" || !p.Destination.IsValid() || p.RequestID == "" || p.Operation == "" {
		return egress.Authorization{}, egress.ErrDenied
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return egress.Authorization{}, err
	}
	defer tx.Rollback()
	var result egress.Authorization
	var serviceJSON, operations, owner, expires, driver string
	err = tx.QueryRowContext(ctx, `SELECT r.binding_id,r.generation_id,r.epoch,r.revision,r.policy_digest,r.service,r.operations,b.owner,g.grant_id,g.turn_id,g.expires_at,b.driver_id
 FROM egress_route_bindings r JOIN execution_bindings b ON b.generation_id=r.generation_id JOIN egress_grants g ON g.binding_id=r.binding_id
 JOIN turns t ON t.id=g.turn_id AND t.generation_id=r.generation_id
 WHERE r.backend_id=? AND r.destination=? AND (?='' OR r.service_id=?) AND r.state='active' AND g.state='active' AND g.expires_at>? AND g.binding_revision=r.revision AND g.policy_digest=r.policy_digest
 AND b.store_id=r.backend_id AND b.epoch=r.epoch AND t.status IN ('leased','running') AND t.lease_owner=b.owner AND t.lease_expires_at>?`, p.BackendID, p.Destination.String(), p.ServiceID, p.ServiceID, formatTime(p.Now), formatTime(p.Now)).Scan(&result.BindingID, &result.GenerationID, &result.Epoch, &result.BindingRevision, &result.PolicyDigest, &serviceJSON, &operations, &owner, &result.GrantID, &result.TurnID, &expires, &driver)
	if errors.Is(err, sql.ErrNoRows) {
		return result, egress.ErrDenied
	}
	if err != nil {
		return result, err
	}
	var session, user, lease string
	if err := tx.QueryRowContext(ctx, `SELECT b.session_id,s.user_id,g.lease_expires_at FROM execution_bindings b JOIN sessions s ON s.id=b.session_id JOIN runtime_generations g ON g.generation_id=b.generation_id WHERE b.generation_id=?`, result.GenerationID).Scan(&session, &user, &lease); err != nil {
		return result, err
	}
	if err := acceptedExecutionTx(ctx, tx, session, result.GenerationID, owner, p.Now); err != nil {
		return result, egress.ErrDenied
	}
	if err := json.Unmarshal([]byte(serviceJSON), &result.Service); err != nil {
		return result, err
	}
	var allowed []string
	if err := json.Unmarshal([]byte(operations), &allowed); err != nil {
		return result, err
	}
	permitted := false
	for _, operation := range allowed {
		if operation == p.Operation {
			permitted = true
		}
	}
	if !permitted || (driver == "sh" && result.Service.Protocol == egress.ModelHTTP) || !egress.AllowsResource(result.Service, p) {
		return result, egress.ErrDenied
	}
	result.SessionID = session
	result.UserID = user
	result.RequestID = p.RequestID
	result.BackendID = p.BackendID
	result.ServiceID = result.Service.ID
	result.ExpiresAt = parseTime(expires)
	if generationExpiry := parseTime(lease); generationExpiry.Before(result.ExpiresAt) {
		result.ExpiresAt = generationExpiry
	}
	semantic := p
	semantic.Now = time.Time{}
	raw, err := json.Marshal(semantic)
	if err != nil {
		return result, err
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	var previous, bindingID, grantID string
	var priorTurn int64
	err = tx.QueryRowContext(ctx, `SELECT request_digest,binding_id,grant_id,turn_id FROM egress_requests WHERE backend_id=? AND request_id=?`, p.BackendID, p.RequestID).Scan(&previous, &bindingID, &grantID, &priorTurn)
	if err == nil {
		if previous != digest || bindingID != result.BindingID || grantID != result.GrantID || priorTurn != result.TurnID {
			return result, egress.ErrRequestConflict
		}
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO egress_requests(backend_id,request_id,request_digest,binding_id,grant_id,generation_id,turn_id,epoch,service_id,operation,started_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, p.BackendID, p.RequestID, digest, result.BindingID, result.GrantID, result.GenerationID, result.TurnID, result.Epoch, result.ServiceID, p.Operation, formatTime(p.Now))
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) FinishEgressRequest(ctx context.Context, p egress.RequestFinish) error {
	if p.BackendID == "" || p.RequestID == "" || strings.TrimSpace(p.Outcome) == "" || p.Bytes < 0 {
		return fmt.Errorf("request identity and valid outcome required")
	}
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var outcome sql.NullString
	var bytes sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT outcome,bytes FROM egress_requests WHERE backend_id=? AND request_id=?`, p.BackendID, p.RequestID).Scan(&outcome, &bytes)
	if err != nil {
		return err
	}
	if outcome.Valid {
		if outcome.String != p.Outcome || bytes.Int64 != p.Bytes {
			return egress.ErrRequestConflict
		}
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `UPDATE egress_requests SET outcome=?,bytes=?,finished_at=? WHERE backend_id=? AND request_id=?`, p.Outcome, p.Bytes, formatTime(p.Now), p.BackendID, p.RequestID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeExecutionRoutes(ctx context.Context, generation string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE egress_route_bindings SET state='revoked' WHERE generation_id=?`, generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE egress_grants SET state='revoked' WHERE generation_id=?`, generation); err != nil {
		return err
	}
	return tx.Commit()
}
