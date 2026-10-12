package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"strconv"
)

type PlatformLimit struct {
	Dimension ResourceDimension `json:"dimension"`
	Maximum   int64             `json:"maximum,string"`
	Revision  int64             `json:"revision"`
}
type PlatformLimitEdit struct {
	Dimension        ResourceDimension `json:"dimension"`
	Maximum          int64             `json:"maximum,string"`
	ExpectedRevision int64             `json:"expected_revision"`
	RequestID        string            `json:"request_id"`
}
type PlatformLimitResult struct {
	PlatformLimit
	Replayed bool `json:"replayed"`
}

func (input *PlatformLimitEdit) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Dimension        ResourceDimension `json:"dimension"`
		Maximum          *string           `json:"maximum"`
		ExpectedRevision int64             `json:"expected_revision"`
		RequestID        string            `json:"request_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF || wire.Maximum == nil {
		return ErrInvalid
	}
	value, err := strconv.ParseInt(*wire.Maximum, 10, 64)
	if err != nil || strconv.FormatInt(value, 10) != *wire.Maximum {
		return ErrInvalid
	}
	*input = PlatformLimitEdit{Dimension: wire.Dimension, Maximum: value, ExpectedRevision: wire.ExpectedRevision, RequestID: wire.RequestID}
	return nil
}

func (s *Store) OperatorPlatformLimits(ctx context.Context) ([]PlatformLimit, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT dimension,maximum,revision FROM platform_resource_limits ORDER BY dimension`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlatformLimit{}
	for rows.Next() {
		var value PlatformLimit
		if err = rows.Scan(&value.Dimension, &value.Maximum, &value.Revision); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// EditPlatformLimit changes only the per-user platform bound. It never rewrites
// group grants, retained user requests, saved allocations or engine resources.
func (s *Store) EditPlatformLimit(ctx context.Context, input PlatformLimitEdit) (PlatformLimitResult, error) {
	var out PlatformLimitResult
	if !validResourceDimension(input.Dimension) || input.Maximum < 0 || input.ExpectedRevision < 1 || input.Dimension == ResourceMounts && (input.Maximum < 2 || input.Maximum > 128) || input.Dimension == ResourceTransfer && input.Maximum > 256<<20 {
		return out, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "platform.limit.update", string(input.Dimension), input)
	if err != nil {
		return out, err
	}
	changed := false
	err = s.write(ctx, func(tx *sql.Tx) error {
		raw, exists, err := readOperatorReceipt(ctx, tx, request)
		if err != nil {
			return err
		}
		if exists {
			if err = json.Unmarshal(raw, &out); err != nil {
				return err
			}
			out.Replayed = true
			return nil
		}
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return ErrDraining
		}
		before := PlatformLimit{Dimension: input.Dimension}
		if err = missing(tx.QueryRowContext(ctx, `SELECT maximum,revision FROM platform_resource_limits WHERE dimension=?`, input.Dimension).Scan(&before.Maximum, &before.Revision)); err != nil {
			return err
		}
		if before.Revision != input.ExpectedRevision {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE platform_resource_limits SET maximum=?,revision=revision+1 WHERE dimension=?`, input.Maximum, input.Dimension); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET authorization_revision=authorization_revision+1,revision=revision+1`); err != nil {
			return err
		}
		out.PlatformLimit = PlatformLimit{Dimension: input.Dimension, Maximum: input.Maximum, Revision: before.Revision + 1}
		if err = s.auditEvent(ctx, tx, "operator", request.Action, "platform_limit", request.Target, request.ID, out.Revision, map[string]any{"before": before, "after": out.PlatformLimit}); err != nil {
			return err
		}
		if err = s.writeOperatorReceipt(ctx, tx, request, out); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return PlatformLimitResult{}, err
	}
	if changed {
		s.invalidateIdentities()
	}
	return out, nil
}
