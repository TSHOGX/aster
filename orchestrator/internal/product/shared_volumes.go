package product

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/sandbox"
)

type SharedVolume struct {
	ResourceTemplateRef
	Revision  int64  `json:"revision"`
	Capacity  int64  `json:"capacity,string"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updated_at"`
	VolumeID  string `json:"-"`
	StoreID   string `json:"-"`
}
type SharedVolumeEdit struct {
	Preview              bool   `json:"preview,omitempty"`
	ExpectedDependencies string `json:"expected_dependencies,omitempty"`
	RequestID            string `json:"request_id"`
	ExpectedRevision     int64  `json:"expected_revision"`
	Name                 string `json:"name"`
	Description          string `json:"description"`
	Capacity             int64  `json:"capacity,string"`
	Enabled              bool   `json:"enabled"`
}

func sharedVolume(ctx context.Context, q queryer, id string) (SharedVolume, error) {
	var out SharedVolume
	err := missing(q.QueryRowContext(ctx, `SELECT id,name,description,revision,capacity,enabled,state,error,updated_at,volume_id,store_id FROM shared_volumes WHERE id=? AND state<>'deleted'`, id).Scan(&out.ID, &out.Name, &out.Description, &out.Revision, &out.Capacity, &out.Enabled, &out.State, &out.Error, &out.UpdatedAt, &out.VolumeID, &out.StoreID))
	return out, err
}
func (s *Store) SharedVolumes(ctx context.Context, token string) ([]SharedVolume, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,name,description,revision,capacity,enabled,state,error,updated_at FROM shared_volumes WHERE state<>'deleted' ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SharedVolume{}
	for rows.Next() {
		var v SharedVolume
		if err = rows.Scan(&v.ID, &v.Name, &v.Description, &v.Revision, &v.Capacity, &v.Enabled, &v.State, &v.Error, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveSharedVolume(ctx context.Context, token, id string, input SharedVolumeEdit) (json.RawMessage, error) {
	if err := metadataChange(&input.Name, &input.Description, true); err != nil {
		return nil, err
	}
	if input.Capacity <= 0 {
		return nil, ErrInvalid
	}
	return s.catalogMutation(ctx, token, "shared_volume.save", id, input.RequestID, input, func(tx *sql.Tx, actor Account) (any, error) {
		selected := id
		if selected == "" {
			if input.ExpectedRevision != 0 {
				return nil, ErrConflict
			}
			selected = uuid.NewString()
		} else {
			v, e := sharedVolume(ctx, tx, id)
			if e != nil {
				return nil, e
			}
			if v.Revision != input.ExpectedRevision {
				return nil, ErrConflict
			}
			if v.Capacity != input.Capacity {
				return nil, &SettingError{Field: "capacity", Reason: "Capacity is fixed after creation."}
			}
			if v.State == "deleting" {
				return nil, ErrDeleting
			}
		}
		if e := uniqueName(ctx, tx, "shared_volumes", input.Name, selected); e != nil {
			return nil, e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO shared_volumes(id,name,description,capacity,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,description=excluded.description,enabled=excluded.enabled,revision=revision+1,updated_at=excluded.updated_at`, selected, input.Name, input.Description, input.Capacity, input.Enabled, s.timestamp(), s.timestamp())
		if e != nil {
			return nil, e
		}
		return sharedVolume(ctx, tx, selected)
	})
}
func (s *Store) DeleteSharedVolume(ctx context.Context, token, id string, input AccountRevision) (json.RawMessage, error) {
	return s.catalogMutation(ctx, token, "shared_volume.delete", id, input.RequestID, input, func(tx *sql.Tx, actor Account) (any, error) {
		v, e := sharedVolume(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		if v.Revision != input.ExpectedRevision {
			return nil, ErrConflict
		}
		var used bool
		for _, query := range []string{`SELECT EXISTS(SELECT 1 FROM policy_resource_bindings WHERE resource_kind='shared_volume' AND resource_id=?)`, `SELECT EXISTS(SELECT 1 FROM environment_templates t,json_each(t.document,'$.mounts') m WHERE json_extract(m.value,'$.source.shared_volume_id')=?)`, `SELECT EXISTS(SELECT 1 FROM project_configuration c JOIN projects p ON p.id=c.project_id,json_each(c.environment,'$.mounts') m WHERE p.state<>'deleted' AND json_extract(m.value,'$.source.shared_volume_id')=?)`} {
			if e = tx.QueryRowContext(ctx, query, id).Scan(&used); e != nil {
				return nil, e
			}
			if used {
				return nil, Admission("resource_referenced", ErrConflict)
			}
		}
		if _, e = tx.ExecContext(ctx, `UPDATE shared_volumes SET state='deleting',enabled=0,revision=revision+1,updated_at=? WHERE id=?`, s.timestamp(), id); e != nil {
			return nil, e
		}
		return sharedVolume(ctx, tx, id)
	})
}

// ReconcileSharedVolumes uses stable allocation keys so a lost engine reply is
// recovered without allocating a second copy. Deletion is equally retryable.
func (l *Lifecycle) ReconcileSharedVolumes(ctx context.Context) error {
	release, err := l.Store.beginRuntimeWork(ctx, runtimeMaintenance)
	if err != nil {
		return err
	}
	defer release()
	rows, err := l.Store.reader.QueryContext(ctx, `SELECT id FROM shared_volumes WHERE state IN ('pending','deleting') ORDER BY updated_at,id LIMIT 16`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if err := l.reconcileSharedVolume(ctx, id); err != nil {
			l.Store.sharedVolumeError(ctx, id)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (l *Lifecycle) reconcileSharedVolume(ctx context.Context, id string) error {
	v, e := sharedVolume(ctx, l.Store.reader, id)
	if e != nil {
		return e
	}
	// Recover pending creates before deletion as their replies may have been lost.
	if v.VolumeID == "" {
		created, e := l.Backend.CreateVolume(ctx, "shared-volume/"+id, v.Capacity)
		if e != nil {
			return e
		}
		if created.VolumeID == "" || created.StoreID == "" {
			return ErrConflict
		}
		e = l.Store.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE shared_volumes SET volume_id=?,store_id=?,state=CASE WHEN state='pending' THEN 'ready' ELSE state END,error='',updated_at=? WHERE id=? AND state IN ('pending','deleting')`, created.VolumeID, created.StoreID, l.Store.timestamp(), id)
			return e
		})
		if e != nil {
			return e
		}
		v.VolumeID = created.VolumeID
		v.StoreID = created.StoreID
	}
	if v.State == "deleting" {
		e = l.Backend.DeleteVolume(ctx, sandbox.VolumeReference{StoreID: v.StoreID, VolumeID: v.VolumeID})
		if e != nil && !sandbox.Is(e, sandbox.NotFound) {
			return e
		}
		if e = l.Store.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE shared_volumes SET state='deleted',error='',updated_at=? WHERE id=? AND state='deleting'`, l.Store.timestamp(), id)
			return e
		}); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) sharedVolumeError(ctx context.Context, id string) {
	_ = s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE shared_volumes SET error=?,updated_at=? WHERE id=?`, "Storage operation failed; retrying.", s.timestamp(), id)
		return err
	})
}
