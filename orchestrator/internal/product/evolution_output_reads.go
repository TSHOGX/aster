package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sqlitex"
	"time"
)

func (s *Store) BeginEvolutionOutputRead(ctx context.Context, token string, query evolution.OutputCatalogQuery) (evolution.OutputCatalogScope, error) {
	scope := evolution.OutputCatalogScope{Query: query}
	if query.Validate() != nil {
		return scope, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, query.GroupID, "config.read"); err != nil {
			return err
		}
		scope.Ticket = uuid.NewString()
		if _, err := tx.ExecContext(ctx, "DELETE FROM evolution_output_reads WHERE expires_at<=?", s.timestamp()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO evolution_output_reads VALUES(?,?,?,?,?)", scope.Ticket, tokenDigest(token), query.GroupID, query.Digest(), sqlitex.FormatTime(s.now().Add(time.Minute)))
		return err
	})
	if err != nil {
		return evolution.OutputCatalogScope{}, err
	}
	return scope, nil
}
func (s *Store) EndEvolutionOutputRead(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM evolution_output_reads WHERE id=?", id)
		return err
	})
}
func (s *Store) EvolutionOutputReader(ctx context.Context, scope evolution.OutputCatalogScope) (string, error) {
	if !validID(scope.Ticket) || scope.Query.Validate() != nil {
		return "", ErrInvalid
	}
	var actor Account
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT a.id,a.role FROM evolution_output_reads r JOIN auth_sessions l ON l.token_digest=r.session_digest JOIN accounts a ON a.id=l.account_id
   WHERE r.id=? AND r.group_id=? AND r.query_digest=? AND r.expires_at>? AND l.expires_at>? AND l.restricted=0 AND a.must_change_password=0 AND a.session_revision=l.session_revision`, scope.Ticket, scope.Query.GroupID, scope.Query.Digest(), s.timestamp(), s.timestamp()).Scan(&actor.ID, &actor.Role); err != nil {
			return ErrForbidden
		}
		return authorizeEvolutionGroup(ctx, tx, actor, scope.Query.GroupID, "config.read")
	})
	if err != nil {
		return "", err
	}
	return actor.ID, nil
}
