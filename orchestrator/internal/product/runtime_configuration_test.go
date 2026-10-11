package product

import (
	"bytes"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHandoffConfigurationIncludesPersistedAuthorityAndExcludesTraffic(t *testing.T) {
	s, scope, _ := fixture(t)
	original, err := runtimeConfigurationDigest(ctx, s.reader)
	must(t, err)
	// Browser credential/appearance traffic is not runtime configuration.
	must(t, s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE accounts SET session_revision=session_revision+1,authorization_revision=authorization_revision+1,display_name='Changed label' WHERE id=?`, scope.Actor)
		return err
	}))
	after, err := runtimeConfigurationDigest(ctx, s.reader)
	must(t, err)
	if original != after {
		t.Fatal("credential/display traffic changed runtime contract")
	}
	statements := []string{
		`UPDATE network_defaults SET configuration_revision=configuration_revision+1`,
		`UPDATE default_environment SET revision=revision+1`,
		`UPDATE registered_sandboxes SET enabled=0`,
		`UPDATE platform_resource_limits SET revision=revision+1 WHERE dimension='projects'`,
		`UPDATE group_resource_policy SET default_value=default_value-1 WHERE dimension='projects'`,
		`UPDATE permission_policies SET revision=revision+1`,
		`DELETE FROM account_memberships WHERE account_id='bob'`,
		`UPDATE accounts SET status='disabled' WHERE id='bob'`,
	}
	for _, statement := range statements {
		must(t, s.write(ctx, func(tx *sql.Tx) error {
			before, err := runtimeConfigurationDigest(ctx, tx)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return err
			}
			after, err := runtimeConfigurationDigest(ctx, tx)
			if err != nil {
				return err
			}
			if before == after {
				t.Errorf("configuration change missing from fingerprint: %s", statement)
			}
			return nil
		}))
	}
}
func TestHandoffRejectsChangedConfigurationBeforeActivationAndWriterOpen(t *testing.T) {
	for _, boundary := range []string{"readiness", "activation", "reopen"} {
		t.Run(boundary, func(t *testing.T) {
			s, _, _, proof := ownerHandoffFixture(t)
			target := "sha256:" + strings.Repeat("b", 64)
			drain, err := s.BeginOwnerHandoff(ctx, "config-"+boundary, time.Minute, target, s.identity.CompatibilityDigest, proof())
			must(t, err)
			if boundary != "readiness" {
				_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, proof())
				must(t, err)
			}
			if boundary == "reopen" {
				_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target, proof())
				must(t, err)
			}
			// Simulate an out-of-band mutation; normal services fence this edit already.
			_, err = s.db.Exec(`UPDATE network_defaults SET configuration_revision=configuration_revision+1`)
			must(t, err)
			switch boundary {
			case "readiness":
				_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, proof())
				wantError(t, err, ErrConflict)
			case "activation":
				_, err = s.ActivateRelease(ctx, drain.Owner, drain.Generation, target, proof())
				wantError(t, err, ErrConflict)
			case "reopen":
				var number int
				var name, path string
				must(t, s.reader.QueryRow(`PRAGMA database_list`).Scan(&number, &name, &path))
				identity := s.identity
				identity.ExecutableDigest = target
				must(t, s.Close())
				before, err := os.ReadFile(path)
				must(t, err)
				if opened, err := OpenApplication(ctx, path, identity); err == nil {
					opened.Close()
					t.Fatal("changed config acquired replacement writer")
				}
				after, err := os.ReadFile(path)
				must(t, err)
				if !bytes.Equal(before, after) {
					t.Fatal("rejected candidate mutated database")
				}
			}
		})
	}
}

func TestHandoffDoesNotTreatSuspendedOwnersWorkAsTransferable(t *testing.T) {
	s, _, _, proof := ownerHandoffFixture(t)
	_, err := s.db.Exec(`UPDATE accounts SET status='disabled' WHERE id IN (SELECT owner_id FROM projects WHERE id IN (SELECT project_id FROM execution_runs WHERE state='running'))`)
	must(t, err)
	target := "sha256:" + strings.Repeat("b", 64)
	drain, err := s.BeginOwnerHandoff(ctx, "suspended-owner", time.Minute, target, s.identity.CompatibilityDigest, proof())
	must(t, err)
	_, err = s.TryReleaseReady(ctx, drain.Owner, drain.Generation, proof())
	wantError(t, err, ErrBusy)
}
