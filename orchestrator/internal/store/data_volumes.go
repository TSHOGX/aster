package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/canonicaljson"
)

type RuntimeIdentity struct {
	UID              int
	GID              int
	SupplementalGIDs []int
}

type SessionWorkspaceVolume struct {
	SessionID                string
	HostPath                 string
	LayoutVersion            int
	SandboxUID               int
	SandboxGID               int
	SandboxSupplementalGIDs  []int
	RuntimeIdentityDigest    string
	ProvisionedAt            time.Time
	ProvisioningMarkerPath   string
	ProvisioningMarkerDigest string
}

type SessionDriverHomeVolume struct {
	SessionID                string
	Driver                   string
	HostPath                 string
	LayoutVersion            int
	SandboxUID               int
	SandboxGID               int
	SandboxSupplementalGIDs  []int
	RuntimeIdentityDigest    string
	ProvisionedAt            time.Time
	ProvisioningMarkerPath   string
	ProvisioningMarkerDigest string
}

func DriverHomeKeyFor(driverID string) (string, error) {
	driverID = strings.TrimSpace(driverID)
	if _, ok := agents.Lookup(driverID); !ok {
		return "", fmt.Errorf("unsupported driver home key %q", driverID)
	}
	return driverID, nil
}

type dataVolumeIdentityJSON struct {
	SandboxUID              int   `json:"sandbox_uid"`
	SandboxGID              int   `json:"sandbox_gid"`
	SandboxSupplementalGIDs []int `json:"sandbox_supplemental_gids"`
}

func RuntimeIdentityDigest(identity RuntimeIdentity) (string, error) {
	normalized := NormalizeRuntimeIdentity(identity)
	if err := ValidateRuntimeIdentity(normalized); err != nil {
		return "", err
	}
	payload := dataVolumeIdentityJSON{
		SandboxUID:              normalized.UID,
		SandboxGID:              normalized.GID,
		SandboxSupplementalGIDs: normalized.SupplementalGIDs,
	}
	data, err := canonicaljson.Marshal(payload)
	if err != nil {
		return "", err
	}
	return SandboxContractDigest(data), nil
}

func NormalizeRuntimeIdentity(identity RuntimeIdentity) RuntimeIdentity {
	normalized := identity
	normalized.SupplementalGIDs = append([]int(nil), identity.SupplementalGIDs...)
	sort.Ints(normalized.SupplementalGIDs)
	return normalized
}

func ValidateRuntimeIdentity(identity RuntimeIdentity) error {
	if identity.UID <= 0 {
		return fmt.Errorf("runtime identity uid must be > 0")
	}
	if identity.GID <= 0 {
		return fmt.Errorf("runtime identity gid must be > 0")
	}
	seen := map[int]struct{}{}
	for _, gid := range identity.SupplementalGIDs {
		if gid <= 0 {
			return fmt.Errorf("runtime identity supplemental gids must be positive")
		}
		if _, ok := seen[gid]; ok {
			return fmt.Errorf("runtime identity supplemental gids contain duplicate gid %d", gid)
		}
		seen[gid] = struct{}{}
	}
	return nil
}

func (s *Store) InsertSessionWorkspaceRecord(ctx context.Context, row SessionWorkspaceVolume) error {
	gids, err := json.Marshal(row.SandboxSupplementalGIDs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO session_workspaces (
  session_id, host_path, layout_version, sandbox_uid, sandbox_gid,
  sandbox_supplemental_gids, runtime_identity_digest, provisioned_at,
  provisioning_marker_path, provisioning_marker_digest
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO NOTHING`,
		row.SessionID, row.HostPath, row.LayoutVersion, row.SandboxUID, row.SandboxGID,
		string(gids), row.RuntimeIdentityDigest, formatTime(row.ProvisionedAt),
		row.ProvisioningMarkerPath, row.ProvisioningMarkerDigest)
	return err
}

func (s *Store) InsertSessionDriverHomeRecord(ctx context.Context, row SessionDriverHomeVolume) error {
	gids, err := json.Marshal(row.SandboxSupplementalGIDs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO session_driver_homes (
  session_id, driver, host_path, layout_version, sandbox_uid, sandbox_gid,
  sandbox_supplemental_gids, runtime_identity_digest, provisioned_at,
  provisioning_marker_path, provisioning_marker_digest
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, driver) DO NOTHING`,
		row.SessionID, row.Driver, row.HostPath, row.LayoutVersion, row.SandboxUID, row.SandboxGID,
		string(gids), row.RuntimeIdentityDigest, formatTime(row.ProvisionedAt),
		row.ProvisioningMarkerPath, row.ProvisioningMarkerDigest)
	return err
}

func (s *Store) GetSessionWorkspaceRecord(ctx context.Context, sessionID string) (SessionWorkspaceVolume, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT session_id, host_path, layout_version, sandbox_uid, sandbox_gid,
       sandbox_supplemental_gids, runtime_identity_digest, provisioned_at,
       provisioning_marker_path, provisioning_marker_digest
FROM session_workspaces
WHERE session_id = ?`, sessionID)
	return scanSessionWorkspaceVolume(row)
}

func scanSessionWorkspaceVolume(row rowScanner) (SessionWorkspaceVolume, error) {
	var volume SessionWorkspaceVolume
	var gids, provisionedAt string
	if err := row.Scan(
		&volume.SessionID,
		&volume.HostPath,
		&volume.LayoutVersion,
		&volume.SandboxUID,
		&volume.SandboxGID,
		&gids,
		&volume.RuntimeIdentityDigest,
		&provisionedAt,
		&volume.ProvisioningMarkerPath,
		&volume.ProvisioningMarkerDigest,
	); err != nil {
		return SessionWorkspaceVolume{}, err
	}
	if err := json.Unmarshal([]byte(gids), &volume.SandboxSupplementalGIDs); err != nil {
		return SessionWorkspaceVolume{}, err
	}
	volume.ProvisionedAt = parseTime(provisionedAt)
	return volume, nil
}

func (s *Store) GetSessionDriverHomeRecord(ctx context.Context, sessionID, driver string) (SessionDriverHomeVolume, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT session_id, driver, host_path, layout_version, sandbox_uid, sandbox_gid,
       sandbox_supplemental_gids, runtime_identity_digest, provisioned_at,
       provisioning_marker_path, provisioning_marker_digest
FROM session_driver_homes
WHERE session_id = ?
  AND driver = ?`, sessionID, driver)
	var volume SessionDriverHomeVolume
	var gids, provisionedAt string
	if err := row.Scan(
		&volume.SessionID,
		&volume.Driver,
		&volume.HostPath,
		&volume.LayoutVersion,
		&volume.SandboxUID,
		&volume.SandboxGID,
		&gids,
		&volume.RuntimeIdentityDigest,
		&provisionedAt,
		&volume.ProvisioningMarkerPath,
		&volume.ProvisioningMarkerDigest,
	); err != nil {
		return SessionDriverHomeVolume{}, err
	}
	if err := json.Unmarshal([]byte(gids), &volume.SandboxSupplementalGIDs); err != nil {
		return SessionDriverHomeVolume{}, err
	}
	volume.ProvisionedAt = parseTime(provisionedAt)
	return volume, nil
}

func (s *Store) ListSessionWorkspaceRecords(ctx context.Context) ([]SessionWorkspaceVolume, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,host_path,layout_version,sandbox_uid,sandbox_gid,sandbox_supplemental_gids,runtime_identity_digest,provisioned_at,provisioning_marker_path,provisioning_marker_digest FROM session_workspaces WHERE NOT EXISTS(SELECT 1 FROM session_execution_backends b WHERE b.session_id=session_workspaces.session_id AND b.backend='khora') ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	volumes := []SessionWorkspaceVolume{}
	for rows.Next() {
		volume, err := scanSessionWorkspaceVolume(rows)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, volume)
	}
	return volumes, rows.Err()
}
