package product

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
)

type CatalogRevision struct {
	Kind     string          `json:"kind"`
	ID       string          `json:"id"`
	Revision int64           `json:"revision"`
	Digest   string          `json:"digest"`
	Body     json.RawMessage `json:"body"`
	Enabled  bool            `json:"enabled"`
	Retired  bool            `json:"retired"`
}

func validCatalogKind(k string) bool {
	return k == "service" || k == "template" || k == "workflow" || k == "profile"
}
func (s *Store) CatalogRevision(ctx context.Context, token, kind, id string, revision int64) (CatalogRevision, error) {
	var c CatalogRevision
	var body string
	var enabled, retired int
	if !validCatalogKind(kind) || !validID(id) || revision < 1 {
		return c, ErrInvalid
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.adminSession(ctx, tx, token); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT kind,id,revision,digest,body,enabled,retired FROM evolution_catalog_revisions WHERE kind=? AND id=? AND revision=?", kind, id, revision).Scan(&c.Kind, &c.ID, &c.Revision, &c.Digest, &body, &enabled, &retired)
	})
	if err != nil {
		return c, err
	}
	c.Body = json.RawMessage(body)
	if c.Digest != evolution.Digest(c.Body) {
		return c, ErrInvalid
	}
	c.Enabled = enabled == 1
	c.Retired = retired == 1
	return c, nil
}
func (s *Store) PublishCatalogRevision(ctx context.Context, token string, c CatalogRevision) (CatalogRevision, error) {
	if !validCatalogKind(c.Kind) || !validID(c.ID) || c.Revision < 1 || len(c.Body) == 0 || c.Digest != evolution.Digest(c.Body) {
		return c, ErrInvalid
	}
	if _, err := evolution.Decode[map[string]json.RawMessage](bytes.NewReader(c.Body)); err != nil {
		return c, ErrInvalid
	}
	if c.Kind == "workflow" {
		workflow, err := evolution.DecodeWorkflow(bytes.NewReader(c.Body))
		if err != nil || workflow.ID != c.ID {
			return c, ErrInvalid
		}
		canonical, err := workflow.Canonical()
		if err != nil || !bytes.Equal(canonical, c.Body) {
			return c, ErrInvalid
		}
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		var max int64
		err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0) FROM evolution_catalog_revisions WHERE kind=? AND id=?", c.Kind, c.ID).Scan(&max)
		if err != nil {
			return err
		}
		if c.Revision != max+1 {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO evolution_catalog_revisions VALUES(?,?,?,?,?,?,?,?)", c.Kind, c.ID, c.Revision, c.Digest, string(c.Body), c.Enabled, c.Retired, s.timestamp()); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.catalog.publish", c.Kind, c.ID, "", c.Revision, c)
	})
	return c, err
}
func (s *Store) BindCatalogRevision(ctx context.Context, token, group, binding, kind, id string, revision int64) (CatalogRevision, error) {
	if !validID(group) || !validID(binding) || !validCatalogKind(kind) || !validID(id) || revision < 1 {
		return CatalogRevision{}, ErrInvalid
	}
	var c CatalogRevision
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		var body string
		var enabled, retired int
		if err = tx.QueryRowContext(ctx, "SELECT kind,id,revision,digest,body,enabled,retired FROM evolution_catalog_revisions WHERE kind=? AND id=? AND revision=?", kind, id, revision).Scan(&c.Kind, &c.ID, &c.Revision, &c.Digest, &body, &enabled, &retired); err != nil {
			return err
		}
		c.Body = json.RawMessage(body)
		if c.Digest != evolution.Digest(c.Body) {
			return ErrInvalid
		}
		c.Enabled = enabled == 1
		c.Retired = retired == 1
		if !c.Enabled || c.Retired {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_catalog_bindings VALUES(?,?,?,?,?) ON CONFLICT(group_id,binding_id,kind) DO UPDATE SET catalog_id=excluded.catalog_id,revision=excluded.revision`, group, binding, kind, id, revision); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.catalog.bind", "group", group, "", revision, map[string]string{"binding": binding, "kind": kind, "id": id})
	})
	return c, err
}
func (s *Store) UnbindCatalogRevision(ctx context.Context, token, group, binding, kind string) error {
	if !validID(group) || !validID(binding) || !validCatalogKind(kind) {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM evolution_catalog_bindings WHERE group_id=? AND binding_id=? AND kind=?", group, binding, kind); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "evolution.catalog.unbind", "group", group, "", 0, map[string]string{"binding": binding, "kind": kind})
	})
}
