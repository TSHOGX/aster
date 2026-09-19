package local

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"harness-platform/orchestrator/internal/canonicaljson"
	"harness-platform/orchestrator/internal/store"
)

type dataVolumeIdentityJSON struct {
	SandboxUID              int   `json:"sandbox_uid"`
	SandboxGID              int   `json:"sandbox_gid"`
	SandboxSupplementalGIDs []int `json:"sandbox_supplemental_gids"`
}

const DataVolumeLayoutVersion = 1

type DataVolumeProvisionerConfig struct {
	SessionsRoot    string
	AgentHomesRoot  string
	EvidenceRoot    string
	LayoutVersion   int
	RuntimeIdentity store.RuntimeIdentity
}

type ProvisionSessionWorkspaceParams struct {
	SessionID string
	Config    DataVolumeProvisionerConfig
	Now       time.Time
}

type ProvisionSessionDriverHomeParams struct {
	SessionID string
	Driver    string
	Config    DataVolumeProvisionerConfig
	Now       time.Time
}

type VerifySessionWorkspaceVolumeParams struct {
	SessionID string
	Config    DataVolumeProvisionerConfig
}

type VerifySessionDriverHomeVolumeParams struct {
	SessionID string
	Driver    string
	Config    DataVolumeProvisionerConfig
}

type dataVolumeKind string

const (
	dataVolumeWorkspace  dataVolumeKind = "workspace"
	dataVolumeDriverHome dataVolumeKind = "driver_home"
)

type dataVolumeMarker struct {
	MarkerVersion         int                    `json:"marker_version"`
	VolumeType            string                 `json:"volume_type"`
	SessionID             string                 `json:"session_id"`
	Driver                string                 `json:"driver,omitempty"`
	HostPath              string                 `json:"host_path"`
	LayoutVersion         int                    `json:"layout_version"`
	RuntimeIdentity       dataVolumeIdentityJSON `json:"runtime_identity"`
	RuntimeIdentityDigest string                 `json:"runtime_identity_digest"`
	ProvisionedAt         string                 `json:"provisioned_at"`
}

func (s Volumes) ProvisionSessionWorkspace(ctx context.Context, p ProvisionSessionWorkspaceParams) (store.SessionWorkspaceVolume, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	cfg, err := normalizeDataVolumeConfig(p.Config)
	if err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	sessionID, err := dataVolumeSafePathComponent("session id", p.SessionID)
	if err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	hostPath := filepath.Join(cfg.SessionsRoot, sessionID)
	markerPath := filepath.Join(cfg.EvidenceRoot, "workspaces", sessionID+".json")
	static := dataVolumeStatic{
		kind:       dataVolumeWorkspace,
		sessionID:  sessionID,
		hostPath:   hostPath,
		markerPath: markerPath,
		cfg:        cfg,
	}
	existing, err := s.Records.GetSessionWorkspaceRecord(ctx, sessionID)
	if err == nil {
		if err := validateExistingWorkspaceVolume(existing, static); err != nil {
			return store.SessionWorkspaceVolume{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.SessionWorkspaceVolume{}, err
	}
	if err := prepareFreshDataVolumeHostDir(hostPath, cfg.SessionsRoot); err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	marker, markerBytes, markerDigest, err := buildDataVolumeMarker(static, p.Now)
	if err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	if err := writeDataVolumeMarker(markerPath, cfg.EvidenceRoot, markerBytes, markerDigest); err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	row := store.SessionWorkspaceVolume{
		SessionID:                sessionID,
		HostPath:                 hostPath,
		LayoutVersion:            cfg.LayoutVersion,
		SandboxUID:               cfg.RuntimeIdentity.UID,
		SandboxGID:               cfg.RuntimeIdentity.GID,
		SandboxSupplementalGIDs:  append([]int(nil), cfg.RuntimeIdentity.SupplementalGIDs...),
		RuntimeIdentityDigest:    marker.RuntimeIdentityDigest,
		ProvisionedAt:            p.Now.UTC(),
		ProvisioningMarkerPath:   markerPath,
		ProvisioningMarkerDigest: markerDigest,
	}
	if err := s.Records.InsertSessionWorkspaceRecord(ctx, row); err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	return s.Records.GetSessionWorkspaceRecord(ctx, sessionID)
}

func (s Volumes) ProvisionSessionDriverHome(ctx context.Context, p ProvisionSessionDriverHomeParams) (store.SessionDriverHomeVolume, error) {
	if p.Now.IsZero() {
		p.Now = time.Now().UTC()
	}
	cfg, err := normalizeDataVolumeConfig(p.Config)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	sessionID, err := dataVolumeSafePathComponent("session id", p.SessionID)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	driver, err := store.DriverHomeKeyFor(p.Driver)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	driver, err = dataVolumeSafePathComponent("driver", driver)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	hostPath := filepath.Join(cfg.AgentHomesRoot, sessionID, driver)
	markerPath := filepath.Join(cfg.EvidenceRoot, "driver-homes", sessionID, driver+".json")
	static := dataVolumeStatic{
		kind:       dataVolumeDriverHome,
		sessionID:  sessionID,
		driver:     driver,
		hostPath:   hostPath,
		markerPath: markerPath,
		cfg:        cfg,
	}
	existing, err := s.Records.GetSessionDriverHomeRecord(ctx, sessionID, driver)
	if err == nil {
		if err := validateExistingDriverHomeVolume(existing, static); err != nil {
			return store.SessionDriverHomeVolume{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.SessionDriverHomeVolume{}, err
	}
	if err := prepareFreshDataVolumeHostDir(hostPath, cfg.AgentHomesRoot); err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	marker, markerBytes, markerDigest, err := buildDataVolumeMarker(static, p.Now)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	if err := writeDataVolumeMarker(markerPath, cfg.EvidenceRoot, markerBytes, markerDigest); err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	row := store.SessionDriverHomeVolume{
		SessionID:                sessionID,
		Driver:                   driver,
		HostPath:                 hostPath,
		LayoutVersion:            cfg.LayoutVersion,
		SandboxUID:               cfg.RuntimeIdentity.UID,
		SandboxGID:               cfg.RuntimeIdentity.GID,
		SandboxSupplementalGIDs:  append([]int(nil), cfg.RuntimeIdentity.SupplementalGIDs...),
		RuntimeIdentityDigest:    marker.RuntimeIdentityDigest,
		ProvisionedAt:            p.Now.UTC(),
		ProvisioningMarkerPath:   markerPath,
		ProvisioningMarkerDigest: markerDigest,
	}
	if err := s.Records.InsertSessionDriverHomeRecord(ctx, row); err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	return s.Records.GetSessionDriverHomeRecord(ctx, sessionID, driver)
}

func (s Volumes) VerifySessionWorkspaceVolume(ctx context.Context, p VerifySessionWorkspaceVolumeParams) (store.SessionWorkspaceVolume, error) {
	cfg, err := normalizeDataVolumeConfig(p.Config)
	if err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	sessionID, err := dataVolumeSafePathComponent("session id", p.SessionID)
	if err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	static := dataVolumeStatic{
		kind:       dataVolumeWorkspace,
		sessionID:  sessionID,
		hostPath:   filepath.Join(cfg.SessionsRoot, sessionID),
		markerPath: filepath.Join(cfg.EvidenceRoot, "workspaces", sessionID+".json"),
		cfg:        cfg,
	}
	volume, err := s.Records.GetSessionWorkspaceRecord(ctx, sessionID)
	if err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	if err := validateExistingWorkspaceVolume(volume, static); err != nil {
		return store.SessionWorkspaceVolume{}, err
	}
	return volume, nil
}

func (s Volumes) ListVerifiedSessionWorkspaceVolumes(ctx context.Context, cfg DataVolumeProvisionerConfig) ([]store.SessionWorkspaceVolume, error) {
	normalized, err := normalizeDataVolumeConfig(cfg)
	if err != nil {
		return nil, err
	}
	records, err := s.Records.ListSessionWorkspaceRecords(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		sessionID, err := dataVolumeSafePathComponent("session id", record.SessionID)
		if err != nil {
			return nil, err
		}
		expected := dataVolumeStatic{kind: dataVolumeWorkspace, sessionID: sessionID, hostPath: filepath.Join(normalized.SessionsRoot, sessionID), markerPath: filepath.Join(normalized.EvidenceRoot, "workspaces", sessionID+".json"), cfg: normalized}
		if err := validateExistingWorkspaceVolume(record, expected); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func (s Volumes) VerifySessionDriverHomeVolume(ctx context.Context, p VerifySessionDriverHomeVolumeParams) (store.SessionDriverHomeVolume, error) {
	cfg, err := normalizeDataVolumeConfig(p.Config)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	sessionID, err := dataVolumeSafePathComponent("session id", p.SessionID)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	driver, err := store.DriverHomeKeyFor(p.Driver)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	driver, err = dataVolumeSafePathComponent("driver", driver)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	static := dataVolumeStatic{
		kind:       dataVolumeDriverHome,
		sessionID:  sessionID,
		driver:     driver,
		hostPath:   filepath.Join(cfg.AgentHomesRoot, sessionID, driver),
		markerPath: filepath.Join(cfg.EvidenceRoot, "driver-homes", sessionID, driver+".json"),
		cfg:        cfg,
	}
	volume, err := s.Records.GetSessionDriverHomeRecord(ctx, sessionID, driver)
	if err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	if err := validateExistingDriverHomeVolume(volume, static); err != nil {
		return store.SessionDriverHomeVolume{}, err
	}
	return volume, nil
}

type normalizedDataVolumeConfig struct {
	SessionsRoot    string
	AgentHomesRoot  string
	EvidenceRoot    string
	LayoutVersion   int
	RuntimeIdentity store.RuntimeIdentity
}

type dataVolumeStatic struct {
	kind       dataVolumeKind
	sessionID  string
	driver     string
	hostPath   string
	markerPath string
	cfg        normalizedDataVolumeConfig
}

func normalizeDataVolumeConfig(cfg DataVolumeProvisionerConfig) (normalizedDataVolumeConfig, error) {
	layoutVersion := cfg.LayoutVersion
	if layoutVersion == 0 {
		layoutVersion = DataVolumeLayoutVersion
	}
	if layoutVersion <= 0 {
		return normalizedDataVolumeConfig{}, fmt.Errorf("data volume layout version must be > 0")
	}
	identity := store.NormalizeRuntimeIdentity(cfg.RuntimeIdentity)
	if err := store.ValidateRuntimeIdentity(identity); err != nil {
		return normalizedDataVolumeConfig{}, err
	}
	sessionsRoot, err := dataVolumeCanonicalRoot(cfg.SessionsRoot, "sessions root")
	if err != nil {
		return normalizedDataVolumeConfig{}, err
	}
	agentHomesRoot, err := dataVolumeCanonicalRoot(cfg.AgentHomesRoot, "agent homes root")
	if err != nil {
		return normalizedDataVolumeConfig{}, err
	}
	evidenceRoot, err := dataVolumeCanonicalRoot(cfg.EvidenceRoot, "data volume evidence root")
	if err != nil {
		return normalizedDataVolumeConfig{}, err
	}
	if dataVolumeRootsOverlap(sessionsRoot, agentHomesRoot) {
		return normalizedDataVolumeConfig{}, fmt.Errorf("data volume sessions root %q overlaps agent homes root %q", sessionsRoot, agentHomesRoot)
	}
	for _, root := range []struct {
		label string
		path  string
	}{
		{label: "sessions root", path: sessionsRoot},
		{label: "agent homes root", path: agentHomesRoot},
	} {
		if dataVolumeRootsOverlap(evidenceRoot, root.path) {
			return normalizedDataVolumeConfig{}, fmt.Errorf("data volume evidence root %q overlaps %s %q", evidenceRoot, root.label, root.path)
		}
	}
	return normalizedDataVolumeConfig{
		SessionsRoot:    sessionsRoot,
		AgentHomesRoot:  agentHomesRoot,
		EvidenceRoot:    evidenceRoot,
		LayoutVersion:   layoutVersion,
		RuntimeIdentity: identity,
	}, nil
}

func dataVolumeCanonicalRoot(path, label string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s is required and must be absolute", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%s %q must be absolute: %w", label, path, err)
	}
	cleaned := filepath.Clean(absolute)
	if cleaned == string(filepath.Separator) {
		return "", fmt.Errorf("%s must not be filesystem root", label)
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return filepath.Clean(resolved), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("resolve %s %q: %w", label, cleaned, err)
	}
	existing, missing, err := dataVolumeDeepestExistingRoot(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve %s %q: %w", label, cleaned, err)
	}
	if existing == "" {
		return cleaned, nil
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve %s existing prefix %q: %w", label, existing, err)
	}
	return filepath.Clean(filepath.Join(append([]string{resolved}, missing...)...)), nil
}

func dataVolumeDeepestExistingRoot(path string) (string, []string, error) {
	var missing []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
				missing[i], missing[j] = missing[j], missing[i]
			}
			return current, missing, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, nil
		}
		missing = append(missing, filepath.Base(current))
	}
}

func dataVolumeRootsOverlap(a, b string) bool {
	return dataVolumePathWithinRoot(a, b) || dataVolumePathWithinRoot(b, a)
}

func dataVolumeSafePathComponent(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || filepath.Base(value) != value || value == "." || value == ".." {
		return "", fmt.Errorf("%s %q is not a safe path component", label, value)
	}
	return value, nil
}

func dataVolumeValidateHostPath(path, root string) error {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return fmt.Errorf("data volume host path %q must not contain '..'", path)
		}
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("data volume host path %q must be absolute", path)
	}
	cleaned := filepath.Clean(path)
	if cleaned != path {
		return fmt.Errorf("data volume host path %q must be canonical", path)
	}
	if !dataVolumePathWithinRoot(cleaned, root) {
		return fmt.Errorf("data volume host path %q is outside root %q", cleaned, root)
	}
	return nil
}

func dataVolumePathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func prepareFreshDataVolumeHostDir(path, root string) error {
	if err := dataVolumeValidateHostPath(path, root); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(path, 0o755)
	}
	if err != nil {
		return fmt.Errorf("stat data volume host path %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data volume host path %q must not be a symlink", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("data volume host path %q must be a directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read data volume host path %q: %w", path, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("fresh data volume host path %q must be empty", path)
	}
	return nil
}

func buildDataVolumeMarker(static dataVolumeStatic, provisionedAt time.Time) (dataVolumeMarker, []byte, string, error) {
	identityDigest, err := store.RuntimeIdentityDigest(static.cfg.RuntimeIdentity)
	if err != nil {
		return dataVolumeMarker{}, nil, "", err
	}
	marker := dataVolumeMarker{
		MarkerVersion: 1,
		VolumeType:    string(static.kind),
		SessionID:     static.sessionID,
		Driver:        static.driver,
		HostPath:      static.hostPath,
		LayoutVersion: static.cfg.LayoutVersion,
		RuntimeIdentity: dataVolumeIdentityJSON{
			SandboxUID:              static.cfg.RuntimeIdentity.UID,
			SandboxGID:              static.cfg.RuntimeIdentity.GID,
			SandboxSupplementalGIDs: static.cfg.RuntimeIdentity.SupplementalGIDs,
		},
		RuntimeIdentityDigest: identityDigest,
		ProvisionedAt:         provisionedAt.UTC().Format(time.RFC3339Nano),
	}
	data, err := canonicaljson.Marshal(marker)
	if err != nil {
		return dataVolumeMarker{}, nil, "", err
	}
	return marker, data, store.SandboxContractDigest(data), nil
}

func writeDataVolumeMarker(path, evidenceRoot string, payload []byte, digest string) error {
	if err := ensureDataVolumeMarkerDir(evidenceRoot, filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil {
		if err := verifyRootOwnedRegularDataVolumeMarkerInfo(path, info); err != nil {
			return err
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read data volume marker %q: %w", path, err)
		}
		if bytes.Equal(existing, payload) && store.SandboxContractDigest(existing) == digest {
			return nil
		}
		return fmt.Errorf("data volume marker %q already exists with different payload", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read data volume marker %q: %w", path, err)
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("write data volume marker: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write data volume marker: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write data volume marker: %w", err)
	}
	if err := verifyRootOwnedRegularDataVolumeMarker(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename data volume marker: %w", err)
	}
	return verifyRootOwnedRegularDataVolumeMarker(path)
}

func readVerifiedDataVolumeMarker(path, digest string) ([]byte, error) {
	if err := verifyRootOwnedRegularDataVolumeMarker(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read data volume marker %q: %w", path, err)
	}
	if got := store.SandboxContractDigest(data); got != digest {
		return nil, fmt.Errorf("data volume marker digest mismatch for %q: got %s want %s", path, got, digest)
	}
	canonical, err := canonicaljson.Bytes(data)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, canonical) {
		return nil, fmt.Errorf("data volume marker %q is not canonical", path)
	}
	return data, nil
}

func ensureDataVolumeMarkerDir(evidenceRoot, markerDir string) error {
	evidenceRoot = filepath.Clean(evidenceRoot)
	markerDir = filepath.Clean(markerDir)
	if evidenceRoot == string(filepath.Separator) {
		return fmt.Errorf("data volume evidence root must not be filesystem root")
	}
	if !dataVolumePathWithinRoot(markerDir, evidenceRoot) {
		return fmt.Errorf("data volume marker dir %q is outside evidence root %q", markerDir, evidenceRoot)
	}
	if err := os.MkdirAll(evidenceRoot, 0o755); err != nil {
		return fmt.Errorf("create data volume evidence root: %w", err)
	}
	if err := verifyRootOwnedDataVolumeDir(evidenceRoot, "data volume evidence root"); err != nil {
		return err
	}
	rel, err := filepath.Rel(evidenceRoot, markerDir)
	if err != nil {
		return fmt.Errorf("resolve data volume marker dir %q: %w", markerDir, err)
	}
	if rel == "." {
		return nil
	}
	current := evidenceRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("data volume marker dir %q is not canonical under evidence root %q", markerDir, evidenceRoot)
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return fmt.Errorf("create data volume marker dir %q: %w", current, err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("stat data volume marker dir %q: %w", current, err)
		}
		if err := verifyRootOwnedDataVolumeDirInfo(current, info, "data volume marker dir"); err != nil {
			return err
		}
	}
	return nil
}

func verifyRootOwnedDataVolumeDir(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat %s %q: %w", label, path, err)
	}
	return verifyRootOwnedDataVolumeDirInfo(path, info, label)
}

func verifyRootOwnedDataVolumeDirInfo(path string, info os.FileInfo, label string) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s %q must not be a symlink", label, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %q must be a directory", label, path)
	}
	return verifyRootOwnedProtectedDataVolumePath(path, info, label)
}

func verifyRootOwnedRegularDataVolumeMarker(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat data volume marker %q: %w", path, err)
	}
	return verifyRootOwnedRegularDataVolumeMarkerInfo(path, info)
}

func verifyRootOwnedRegularDataVolumeMarkerInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data volume marker %q must not be a symlink", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("data volume marker %q must be a regular file", path)
	}
	return verifyRootOwnedProtectedDataVolumePath(path, info, "data volume marker")
}

func verifyRootOwnedProtectedDataVolumePath(path string, info os.FileInfo, label string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("stat ownership unavailable for %s %q", label, path)
	}
	if stat.Uid != 0 {
		return fmt.Errorf("%s %q must be root-owned, got uid %d", label, path, stat.Uid)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s %q must not be group/world writable", label, path)
	}
	return nil
}

func verifyDataVolumeMarker(path, digest string) error {
	_, err := readVerifiedDataVolumeMarker(path, digest)
	return err
}

func verifyDataVolumeMarkerMatches(path, digest string, expected dataVolumeMarker) error {
	data, err := readVerifiedDataVolumeMarker(path, digest)
	if err != nil {
		return err
	}
	expectedData, err := canonicaljson.Marshal(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expectedData) {
		return fmt.Errorf("data volume marker %q does not match expected provisioning evidence", path)
	}
	return nil
}

func validateExistingWorkspaceVolume(row store.SessionWorkspaceVolume, static dataVolumeStatic) error {
	if row.SessionID != static.sessionID ||
		row.HostPath != static.hostPath ||
		row.LayoutVersion != static.cfg.LayoutVersion ||
		row.SandboxUID != static.cfg.RuntimeIdentity.UID ||
		row.SandboxGID != static.cfg.RuntimeIdentity.GID ||
		!equalIntSlices(row.SandboxSupplementalGIDs, static.cfg.RuntimeIdentity.SupplementalGIDs) ||
		row.ProvisioningMarkerPath != static.markerPath {
		return fmt.Errorf("session workspace volume row does not match expected provisioning config")
	}
	identityDigest, err := store.RuntimeIdentityDigest(static.cfg.RuntimeIdentity)
	if err != nil {
		return err
	}
	if row.RuntimeIdentityDigest != identityDigest {
		return fmt.Errorf("session workspace volume row does not match expected runtime identity digest")
	}
	return verifyDataVolumeMarkerMatches(row.ProvisioningMarkerPath, row.ProvisioningMarkerDigest, dataVolumeMarker{
		MarkerVersion: 1,
		VolumeType:    string(dataVolumeWorkspace),
		SessionID:     static.sessionID,
		HostPath:      static.hostPath,
		LayoutVersion: static.cfg.LayoutVersion,
		RuntimeIdentity: dataVolumeIdentityJSON{
			SandboxUID:              static.cfg.RuntimeIdentity.UID,
			SandboxGID:              static.cfg.RuntimeIdentity.GID,
			SandboxSupplementalGIDs: static.cfg.RuntimeIdentity.SupplementalGIDs,
		},
		RuntimeIdentityDigest: identityDigest,
		ProvisionedAt:         row.ProvisionedAt.UTC().Format(time.RFC3339Nano),
	})
}

func validateExistingDriverHomeVolume(row store.SessionDriverHomeVolume, static dataVolumeStatic) error {
	if row.SessionID != static.sessionID ||
		row.Driver != static.driver ||
		row.HostPath != static.hostPath ||
		row.LayoutVersion != static.cfg.LayoutVersion ||
		row.SandboxUID != static.cfg.RuntimeIdentity.UID ||
		row.SandboxGID != static.cfg.RuntimeIdentity.GID ||
		!equalIntSlices(row.SandboxSupplementalGIDs, static.cfg.RuntimeIdentity.SupplementalGIDs) ||
		row.ProvisioningMarkerPath != static.markerPath {
		return fmt.Errorf("session driver home volume row does not match expected provisioning config")
	}
	identityDigest, err := store.RuntimeIdentityDigest(static.cfg.RuntimeIdentity)
	if err != nil {
		return err
	}
	if row.RuntimeIdentityDigest != identityDigest {
		return fmt.Errorf("session driver home volume row does not match expected runtime identity digest")
	}
	return verifyDataVolumeMarkerMatches(row.ProvisioningMarkerPath, row.ProvisioningMarkerDigest, dataVolumeMarker{
		MarkerVersion: 1,
		VolumeType:    string(dataVolumeDriverHome),
		SessionID:     static.sessionID,
		Driver:        static.driver,
		HostPath:      static.hostPath,
		LayoutVersion: static.cfg.LayoutVersion,
		RuntimeIdentity: dataVolumeIdentityJSON{
			SandboxUID:              static.cfg.RuntimeIdentity.UID,
			SandboxGID:              static.cfg.RuntimeIdentity.GID,
			SandboxSupplementalGIDs: static.cfg.RuntimeIdentity.SupplementalGIDs,
		},
		RuntimeIdentityDigest: identityDigest,
		ProvisionedAt:         row.ProvisionedAt.UTC().Format(time.RFC3339Nano),
	})
}

func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type VolumeRecords interface {
	GetSessionWorkspaceRecord(context.Context, string) (store.SessionWorkspaceVolume, error)
	GetSessionDriverHomeRecord(context.Context, string, string) (store.SessionDriverHomeVolume, error)
	InsertSessionWorkspaceRecord(context.Context, store.SessionWorkspaceVolume) error
	InsertSessionDriverHomeRecord(context.Context, store.SessionDriverHomeVolume) error
	ListSessionWorkspaceRecords(context.Context) ([]store.SessionWorkspaceVolume, error)
}

// Volumes owns legacy directory provisioning and evidence verification. Its
// persistence port only reads and writes records; filesystem I/O happens after
// each database call returns, never under a SQL transaction or open row cursor.
type Volumes struct{ Records VolumeRecords }
