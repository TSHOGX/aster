package product

import (
	"context"
	"database/sql"
	"errors"
)

// ServiceRequest identifies one use of a bounded slot in an independent gateway.
// Sequence never repeats within an instance. The host, never a guest, assigns it.
type ServiceRequest struct {
	Audience string `json:"audience"`
	Instance string `json:"instance"`
	Slot     int    `json:"slot"`
	Sequence int64  `json:"sequence"`
}

type GatewayRegistration struct {
	Instance   string `json:"instance"`
	Generation int64  `json:"generation"`
	Slots      int    `json:"slots"`
}

func gatewayRegistration(ctx context.Context, q queryer, audience string) (out GatewayRegistration, err error) {
	err = q.QueryRowContext(ctx, `SELECT instance,generation,slots FROM service_gateways WHERE audience=?`, audience).Scan(&out.Instance, &out.Generation, &out.Slots)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func (s *Store) GatewayRegistration(ctx context.Context, audience string) (GatewayRegistration, error) {
	return gatewayRegistration(ctx, s.reader, audience)
}

// RegisterGateway is a privileged process-start operation. Its caller MUST hold
// the exclusive gateway lifetime lock until every upstream connection is closed.
// Obtaining that lock proves the previous process has exited; only then may its
// unresolved slots be discarded. Generation CAS rejects delayed registrations.
// This is not a heartbeat and cannot be called by request handlers.
func (s *Store) RegisterGateway(ctx context.Context, audience, instance string, expected int64, slots int) (out GatewayRegistration, err error) {
	if !validID(audience) || len(audience) > 64 || !validID(instance) || expected < 0 || slots < 1 || slots > 256 {
		return out, ErrInvalid
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		current, e := gatewayRegistration(ctx, tx, audience)
		if e != nil {
			return e
		}
		if current.Instance == instance {
			if current.Slots != slots {
				return ErrConflict
			}
			out = current
			return nil
		}
		if current.Generation != expected {
			return ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO service_gateways(audience,instance,generation,slots) VALUES(?,?,?,?) ON CONFLICT(audience) DO UPDATE SET instance=excluded.instance,generation=excluded.generation,slots=excluded.slots`, audience, instance, expected+1, slots); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM service_request_slots WHERE audience=?`, audience); e != nil {
			return e
		}
		out = GatewayRegistration{Instance: instance, Generation: expected + 1, Slots: slots}
		return nil
	})
	return
}

func validServiceRequest(r ServiceRequest) bool {
	return validID(r.Audience) && len(r.Audience) <= 64 && validID(r.Instance) && r.Slot >= 0 && r.Slot < 256 && r.Sequence > 0
}

func checkGateway(ctx context.Context, q queryer, r ServiceRequest) error {
	if !validServiceRequest(r) {
		return ErrInvalid
	}
	g, err := gatewayRegistration(ctx, q, r.Audience)
	if err != nil {
		return err
	}
	if g.Instance != r.Instance || r.Slot >= g.Slots {
		return ErrConflict
	}
	return nil
}

// AcquireGatewayRequest authorizes and records admission in the same writer
// transaction as the release fence. No normal Store is opened by the gateway.
func (s *Store) AcquireGatewayRequest(ctx context.Context, r ServiceRequest, token string) (out Attribution, err error) {
	if !validServiceRequest(r) || len(token) > 256 {
		return out, ErrInvalid
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if e := checkGateway(ctx, tx, r); e != nil {
			return e
		}
		state, e := releaseState(ctx, tx)
		if e != nil {
			return e
		}
		if state.Phase != "open" && state.Phase != "draining" {
			return ErrDraining
		}
		var sequence int64
		var closed bool
		var grant, turn string
		e = tx.QueryRowContext(ctx, `SELECT sequence,closed,grant_id,turn_id FROM service_request_slots WHERE audience=? AND slot=?`, r.Audience, r.Slot).Scan(&sequence, &closed, &grant, &turn)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if sequence > r.Sequence || (sequence == r.Sequence && closed) || (sequence < r.Sequence && sequence > 0 && !closed) {
			return ErrConflict
		}
		out, e = authorizeService(ctx, tx, s.timestamp(), r.Audience, token)
		if e != nil {
			return e
		}
		if sequence == r.Sequence {
			if out.Grant.ID != grant || out.TurnID != turn {
				return ErrConflict
			}
		} else {
			var count int
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM service_request_slots WHERE closed=0`).Scan(&count); e != nil {
				return e
			}
			if count >= 256 {
				return ErrCapacity
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO service_request_slots(audience,slot,sequence,closed,grant_id,turn_id) VALUES(?,?,?,0,?,?) ON CONFLICT(audience,slot) DO UPDATE SET sequence=excluded.sequence,closed=0,grant_id=excluded.grant_id,turn_id=excluded.turn_id`, r.Audience, r.Slot, r.Sequence, out.Grant.ID, out.TurnID)
			if e != nil {
				return e
			}
		}
		out.Request = &r
		return nil
	})
	return
}

// CheckGatewayRequest does not renew a durable TTL. Only positive responses
// renew the gateway's short, monotonic continuation deadline. A completed turn,
// revoked grant or replacement gateway can never inherit an old request.
func (s *Store) CheckGatewayRequest(ctx context.Context, r ServiceRequest) error {
	if !validServiceRequest(r) {
		return ErrInvalid
	}
	var a Attribution
	err := s.reader.QueryRowContext(ctx, `SELECT r.grant_id,r.turn_id FROM service_request_slots r JOIN service_gateways g ON g.audience=r.audience WHERE r.audience=? AND g.instance=? AND r.slot=? AND r.sequence=? AND r.closed=0`, r.Audience, r.Instance, r.Slot, r.Sequence).Scan(&a.Grant.ID, &a.TurnID)
	if err != nil {
		return missing(err)
	}
	return holdsAttribution(ctx, s.reader, s.timestamp(), a)
}

// ReleaseGatewayRequest is called only after forwarding has returned. Closed
// slots retain their sequence so even a release that overtakes a delayed acquire
// is safe. A missing acknowledgment holds gateway capacity until retry succeeds.
func (s *Store) ReleaseGatewayRequest(ctx context.Context, r ServiceRequest) error {
	return s.writeClass(ctx, urgentWrite, "", 1024, func(tx *sql.Tx) error {
		if e := checkGateway(ctx, tx, r); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO service_request_slots(audience,slot,sequence,closed) VALUES(?,?,?,1) ON CONFLICT(audience,slot) DO UPDATE SET sequence=excluded.sequence,closed=1,grant_id='',turn_id='' WHERE service_request_slots.sequence<=excluded.sequence`, r.Audience, r.Slot, r.Sequence)
		return e
	})
}
