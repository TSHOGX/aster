package product

import (
	"context"
	"database/sql"
)

type CatalogBindingView struct {
	BindingID string `json:"binding_id"`
	Kind      string `json:"kind"`
	CatalogID string `json:"catalog_id"`
	Revision  int64  `json:"revision"`
	Digest    string `json:"digest"`
	Enabled   bool   `json:"enabled"`
}

func (s *Store) GroupCatalog(ctx context.Context, token, group string) ([]CatalogBindingView, error) {
	if !validID(group) {
		return nil, ErrInvalid
	}
	out := []CatalogBindingView{}
	err := s.write(ctx, func(tx *sql.Tx) error {
		session, err := s.normalLogin(ctx, tx, token)
		if err != nil {
			return err
		}
		var member int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM account_memberships WHERE account_id=? AND group_id=?", session.Account.ID, group).Scan(&member); err != nil {
			return err
		}
		if member == 0 {
			return ErrForbidden
		}
		rows, err := tx.QueryContext(ctx, `SELECT b.binding_id,b.kind,b.catalog_id,b.revision,r.digest,r.enabled FROM evolution_catalog_bindings b JOIN evolution_catalog_revisions r ON r.kind=b.kind AND r.id=b.catalog_id AND r.revision=b.revision WHERE b.group_id=? ORDER BY b.binding_id,b.kind`, group)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value CatalogBindingView
			var enabled int
			if err = rows.Scan(&value.BindingID, &value.Kind, &value.CatalogID, &value.Revision, &value.Digest, &enabled); err != nil {
				return err
			}
			value.Enabled = enabled == 1
			out = append(out, value)
		}
		return rows.Err()
	})
	return out, err
}
