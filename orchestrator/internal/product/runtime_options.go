package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"time"

	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sandbox"
)

type RuntimeInspector interface {
	RequireTemplate(context.Context, string) error
	InspectVolume(context.Context, sandbox.VolumeReference) (sandbox.Volume, error)
}
type RuntimeOption struct {
	Reference                     ArtifactReference                    `json:"reference"`
	Kind                          string                               `json:"kind"`
	Enabled                       bool                                 `json:"enabled"`
	Availability                  string                               `json:"availability"`
	CheckedAt                     string                               `json:"checked_at,omitempty"`
	RequiredPublications          []runtimerelease.RequiredPublication `json:"required_publications,omitempty"`
	ProtectedRoots                []string                             `json:"protected_roots,omitempty"`
	BlockedPathCount              int                                  `json:"blocked_path_count,omitempty"`
	Roles                         []string                             `json:"roles,omitempty"`
	MinimumBytes                  *string                              `json:"minimum_bytes,omitempty"`
	SourceCapacityBytes           *string                              `json:"source_capacity_bytes,omitempty"`
	CleanRuntime                  bool                                 `json:"clean_runtime,omitempty"`
	storeID, templateID, volumeID string
	capacity                      int64
}
type RuntimeOptions struct {
	Options    []RuntimeOption `json:"options"`
	MaxMounts  int64           `json:"max_mounts,string"`
	NextCursor string          `json:"next_cursor,omitempty"`
}
type runtimeOptionCursor struct{ Kind, ID, Version string }

func (s *Store) AdminRuntimeOptions(ctx context.Context, token, kind, cursor string, limit int, inspector RuntimeInspector) (RuntimeOptions, error) {
	out := RuntimeOptions{Options: []RuntimeOption{}}
	if kind != "sandbox" && kind != "volume" {
		return out, ErrInvalid
	}
	limit, err := adminLimit(limit)
	if err != nil {
		return out, err
	}
	after := runtimeOptionCursor{Kind: kind}
	if cursor != "" {
		if len(cursor) > 2048 {
			return out, ErrInvalid
		}
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil {
			return out, ErrInvalid
		}
		if e = json.Unmarshal(raw, &after); e != nil || after.Kind != kind || !validID(after.ID) || !validText(after.Version, 128) {
			return out, ErrInvalid
		}
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = s.adminSession(ctx, tx, token); err != nil {
		return out, err
	}
	current, _, err := currentEnvironment(ctx, tx)
	if err != nil {
		return out, err
	}
	selected, err := registeredSandbox(ctx, tx, current.SandboxRelease, false)
	if err != nil {
		return out, err
	}
	limits, err := platformResourceLimits(ctx, tx)
	if err != nil {
		return out, err
	}
	out.MaxMounts = limits[ResourceMounts]
	statement := `SELECT id,version,digest,store_id,template_id,manifest,enabled FROM registered_sandboxes WHERE id>? OR (id=? AND version>?) ORDER BY id,version LIMIT ?`
	if kind == "volume" {
		statement = `SELECT v.id,v.version,v.digest,v.store_id,v.roles,v.minimum_bytes,v.clean_runtime,v.enabled,coalesce(p.published_volume_id,''),coalesce(p.capacity,0) FROM builtin_volumes v LEFT JOIN content_publications p ON p.store_id=v.store_id AND p.purpose=v.publication_purpose AND p.revision_digest=v.digest AND p.stage='published' WHERE v.id>? OR (v.id=? AND v.version>?) ORDER BY v.id,v.version LIMIT ?`
	}
	rows, err := tx.QueryContext(ctx, statement, after.ID, after.ID, after.Version, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		option := RuntimeOption{Kind: kind, Availability: "unchecked"}
		if kind == "sandbox" {
			var raw string
			err = rows.Scan(&option.Reference.ID, &option.Reference.Version, &option.Reference.Digest, &option.storeID, &option.templateID, &raw, &option.Enabled)
			if err != nil {
				break
			}
			manifest, compatibilityErr := runtimerelease.Load(bytes.NewReader([]byte(raw)), option.Reference.Digest)
			if compatibilityErr != nil {
				option.Availability = "incompatible"
			}
			option.RequiredPublications = manifest.Mounts.RequiredPublications
			option.ProtectedRoots = manifest.Mounts.ProtectedRoots
			option.BlockedPathCount = len(manifest.Mounts.BlockedPaths)
		} else {
			var raw string
			var minimum int64
			err = rows.Scan(&option.Reference.ID, &option.Reference.Version, &option.Reference.Digest, &option.storeID, &raw, &minimum, &option.CleanRuntime, &option.Enabled, &option.volumeID, &option.capacity)
			if err != nil {
				break
			}
			if err = json.Unmarshal([]byte(raw), &option.Roles); err != nil {
				break
			}
			value := strconv.FormatInt(minimum, 10)
			option.MinimumBytes = &value
			if option.volumeID == "" {
				option.Availability = "unavailable"
			} else {
				size := strconv.FormatInt(option.capacity, 10)
				option.SourceCapacityBytes = &size
			}
		}
		if option.storeID != selected.StoreID {
			option.Availability = "other_store"
		}
		if !option.Enabled {
			option.Availability = "disabled"
		}
		out.Options = append(out.Options, option)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Options) > limit {
		out.Options = out.Options[:limit]
		last := out.Options[limit-1]
		raw, _ := json.Marshal(runtimeOptionCursor{Kind: kind, ID: last.Reference.ID, Version: last.Reference.Version})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	// Observations are bounded and outside the DB snapshot. Availability can
	// change again; it is evidence for the operator, never allocation authority.
	observed, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if inspector != nil {
		for index := range out.Options {
			option := &out.Options[index]
			if option.Availability != "unchecked" {
				continue
			}
			if observed.Err() != nil {
				break
			}
			if kind == "sandbox" {
				err = inspector.RequireTemplate(observed, option.templateID)
			} else {
				var volume sandbox.Volume
				volume, err = inspector.InspectVolume(observed, sandbox.VolumeReference{StoreID: option.storeID, VolumeID: option.volumeID})
				if err == nil && volume.Capacity != option.capacity {
					err = ErrConflict
				}
			}
			option.CheckedAt = s.timestamp()
			option.Availability = "available"
			if err != nil {
				option.Availability = "unavailable"
				if !sandbox.Is(err, sandbox.NotFound) && err != ErrConflict {
					option.Availability = "unknown"
				}
			}
		}
	}
	if _, err = s.adminSession(ctx, s.reader, token); err != nil {
		return RuntimeOptions{}, err
	}
	return out, nil
}
