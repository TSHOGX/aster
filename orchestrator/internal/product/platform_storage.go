package product

import (
	"context"
	"database/sql"
)

// PlatformStorage reports declared capacity, not filesystem block consumption.
// Publications reserve a staging volume and an immutable derivative. Published
// references alias the derivative; registering or mounting it adds no charge.
// Failed/uncertain publication intents remain charged until explicit retirement.
type PlatformStorage struct {
	Owned          ResourceUsage `json:"owned"`
	Publications   ResourceUsage `json:"publications"`
	PublishedBytes int64         `json:"published_bytes,string"`
	Total          ResourceUsage `json:"total"`
}

func platformStorage(ctx context.Context, q queryer) (out PlatformStorage, err error) {
	rows, err := q.QueryContext(ctx, `SELECT capacity,state FROM storage_reservations WHERE state<>'released'`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var capacity int64
		var state string
		if err = rows.Scan(&capacity, &state); err != nil {
			break
		}
		if state == "reserved" {
			out.Owned.Reserved, err = checkedSum(out.Owned.Reserved, capacity)
		} else {
			out.Owned.Used, err = checkedSum(out.Owned.Used, capacity)
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	// Deduplicate by immutable engine object identity, including aliases in
	// registry metadata. A contradictory capacity is an error, never a guess.
	rows, err = q.QueryContext(ctx, `SELECT min(capacity),max(capacity),max(published) FROM (
 SELECT store_id,staging_volume_id volume_id,capacity,0 published FROM content_publications WHERE staging_volume_id<>''
 UNION ALL
 SELECT store_id,derived_volume_id volume_id,capacity,(published_volume_id=derived_volume_id) published FROM content_publications WHERE derived_volume_id<>''
 ) GROUP BY store_id,volume_id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var smallest, largest int64
		var published bool
		if err = rows.Scan(&smallest, &largest, &published); err != nil {
			break
		}
		if smallest != largest {
			err = ErrConflict
			break
		}
		out.Publications.Used, err = checkedSum(out.Publications.Used, largest)
		if err != nil {
			break
		}
		if published {
			out.PublishedBytes, err = checkedSum(out.PublishedBytes, largest)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, `SELECT capacity,(staging_volume_id='')+(derived_volume_id='') FROM content_publications WHERE staging_volume_id='' OR derived_volume_id=''`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var capacity, missing int64
		if err = rows.Scan(&capacity, &missing); err != nil {
			break
		}
		var reserved int64
		reserved, err = checkedProduct(capacity, missing)
		if err != nil {
			break
		}
		out.Publications.Reserved, err = checkedSum(out.Publications.Reserved, reserved)
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	out.Total.Used, err = checkedSum(out.Owned.Used, out.Publications.Used)
	if err != nil {
		return
	}
	out.Total.Reserved, err = checkedSum(out.Owned.Reserved, out.Publications.Reserved)
	if err == nil {
		_, err = checkedSum(out.Total.Used, out.Total.Reserved)
	}
	return
}
func (s *Store) OperatorStorageUsage(ctx context.Context) (PlatformStorage, error) {
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PlatformStorage{}, err
	}
	defer tx.Rollback()
	out, err := platformStorage(ctx, tx)
	if err != nil {
		return PlatformStorage{}, err
	}
	return out, tx.Commit()
}
