package product

import (
	"database/sql"
	"testing"
)

func TestEvolutionOperationInspectionScopeAndFailure(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	must(t, s.FailEvolutionParticipant(ctx, token, pending.OperationID, "evaluation-space", pending.DesiredDigest, "catalog_rejected"))
	value, err := s.EvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID)
	must(t, err)
	if value.State != "failed" || len(value.Participants) != 1 || value.Participants[0].Error != "catalog_rejected" || value.Digest != pending.DesiredDigest {
		t.Fatal(value)
	}
	if _, err = s.EvolutionOperation(ctx, "", StandardUsersGroup, pending.OperationID); err == nil {
		t.Fatal("anonymous inspection")
	}
	if _, err = s.EvolutionOperation(ctx, token, "other", pending.OperationID); err != ErrNotFound {
		t.Fatal("cross-group operation returned", err)
	}
	_, err = s.SetEvolutionGroupGrant(ctx, token, StandardUsersGroup, EvolutionGroupGrantInput{"deny", admin.ID, "config.read", "deny", 0})
	must(t, err)
	if _, err = s.EvolutionOperation(ctx, token, StandardUsersGroup, pending.OperationID); err != ErrForbidden {
		t.Fatal("revoked read allowed", err)
	}
}

func TestEvolutionHistoricalOperationRetainsTerminalState(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	first, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"first", 0, activationConfigFixture(1, true)})
	must(t, err)
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, acceptEvolutionFixture(s, ctx, token, first.OperationID, participant, first.DesiredDigest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, first.OperationID)
	must(t, err)
	second, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"second", 1, activationConfigFixture(2, true)})
	must(t, err)
	must(t, s.FailEvolutionParticipant(ctx, token, second.OperationID, "evaluation-space", second.DesiredDigest, "catalog_rejected"))
	old, err := s.EvolutionOperation(ctx, token, StandardUsersGroup, first.OperationID)
	must(t, err)
	if old.Current || old.State != "active" || old.Revision != 1 || len(old.Participants) != 2 {
		t.Fatal("historical operation changed", old)
	}
	current, err := s.EvolutionOperation(ctx, token, StandardUsersGroup, second.OperationID)
	must(t, err)
	if !current.Current || current.State != "failed" || current.Revision != 2 {
		t.Fatal(current)
	}
	if err = s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM evolution_operation_states WHERE operation_id=?", first.OperationID)
		return err
	}); err == nil {
		t.Fatal("operation provenance deleted")
	}
}

func TestEvolutionTerminalStateCannotReopen(t *testing.T) {
	s, admin := accountStore(t)
	token := adminTestToken(t, s, admin)
	pending, err := s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"activate", 0, activationConfigFixture(1, true)})
	must(t, err)
	// Invalid shortcuts must fail atomically, including the current-state row.
	for _, state := range []string{"discarding", "discarded", "draft"} {
		if err = s.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE evolution_group_configs SET state=? WHERE operation_id=?", state, pending.OperationID)
			return err
		}); err == nil {
			t.Fatal("invalid transition accepted", state)
		}
	}
	for _, participant := range []string{"studio", "evaluation-space"} {
		must(t, acceptEvolutionFixture(s, ctx, token, pending.OperationID, participant, pending.DesiredDigest))
	}
	_, err = s.SettleEvolutionActivation(ctx, token, pending.OperationID)
	must(t, err)
	for _, state := range []string{"activating", "failed", "discarding", "discarded"} {
		if err = s.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE evolution_group_configs SET state=? WHERE operation_id=?", state, pending.OperationID)
			return err
		}); err == nil {
			t.Fatal("terminal activation reopened", state)
		}
	}
	current, err := s.EvolutionConfig(ctx, token, StandardUsersGroup)
	must(t, err)
	events, err := s.EvolutionOperationEvents(ctx, token, StandardUsersGroup, pending.OperationID, 0, 100)
	must(t, err)
	if current.State != "active" || len(events.Events) != 2 {
		t.Fatal("rejected transitions changed state/history", current, events)
	}
	_, err = s.ActivateEvolutionConfig(ctx, token, StandardUsersGroup, EvolutionActivationInput{"next", 1, activationConfigFixture(2, true)})
	must(t, err)
}
