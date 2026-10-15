package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"aster/contracts/volumemanifest"
	"github.com/google/uuid"
	"harness-platform/orchestrator/internal/content"
)

type VolumeImportInput struct {
	RequestID              string `json:"request_id"`
	SourceID               string `json:"source_id"`
	VersionID              string `json:"version_id"`
	ExpectedManifestDigest string `json:"expected_manifest_digest"`
	ConsumerProfile        string `json:"consumer_profile"`
}
type VolumeImport struct {
	ID               string            `json:"id"`
	SourceID         string            `json:"source_id"`
	PublisherID      string            `json:"publisher_id"`
	LibraryID        string            `json:"library_id"`
	VersionID        string            `json:"version_id"`
	ManifestDigest   string            `json:"manifest_digest"`
	Manifest         []byte            `json:"-"`
	Profile          string            `json:"content_profile"`
	StoreID          string            `json:"store_id"`
	TreeDigest       string            `json:"tree_digest"`
	Capacity         int64             `json:"capacity_bytes,string"`
	ActorID          string            `json:"actor_id"`
	PublisherActor   string            `json:"publisher_actor"`
	Artifact         ArtifactReference `json:"artifact"`
	State            string            `json:"state"`
	ResumeState      string            `json:"-"`
	Error            string            `json:"error,omitempty"`
	TransferReserved int64             `json:"transfer_reserved_bytes,string"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
}

const importColumns = "id,source_id,publisher_id,library_id,version_id,manifest_digest,manifest,content_profile,store_id,tree_digest,capacity,actor_id,publisher_actor,artifact_id,artifact_version,state,resume_state,error,transfer_reserved,created_at,updated_at"

func scanImport(row scanner) (VolumeImport, error) {
	var i VolumeImport
	err := row.Scan(&i.ID, &i.SourceID, &i.PublisherID, &i.LibraryID, &i.VersionID, &i.ManifestDigest, &i.Manifest, &i.Profile, &i.StoreID, &i.TreeDigest, &i.Capacity, &i.ActorID, &i.PublisherActor, &i.Artifact.ID, &i.Artifact.Version, &i.State, &i.ResumeState, &i.Error, &i.TransferReserved, &i.CreatedAt, &i.UpdatedAt)
	i.Artifact.Digest = i.TreeDigest
	return i, missing(err)
}
func (s *Store) VolumeImportAdmin(ctx context.Context, token string) (Account, error) {
	return s.adminSession(ctx, s.reader, token)
}

type VolumeImportLabels struct {
	LibraryName  string `json:"library_name"`
	VersionLabel string `json:"version_label"`
	VersionNote  string `json:"version_note"`
}

func (s *Store) AdmitVolumeImport(ctx context.Context, token string, input VolumeImportInput, raw []byte, store, publisherActor string, localBudget, engineBudget int64, labels ...VolumeImportLabels) (VolumeImport, error) {
	if !validID(input.SourceID) || !validID(input.VersionID) || (input.ConsumerProfile != manifest.Profile && input.ConsumerProfile != manifest.SharedAssetsProfile) || localBudget <= 0 || engineBudget <= 0 || store == "" {
		return VolumeImport{}, ErrInvalid
	}
	d, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil || d.Profile != input.ConsumerProfile {
		return VolumeImport{}, ErrInvalid
	}
	digest, err := d.Digest()
	if err != nil || digest != input.ExpectedManifestDigest || d.VersionID != input.VersionID {
		return VolumeImport{}, ErrConflict
	}
	entries := ImportEntries(d)
	profile := content.AuthoredProfile
	if input.ConsumerProfile == manifest.SharedAssetsProfile {
		profile = content.SharedAssetsProfile
	}
	capacity, err := content.MaterializationCapacityForProfile(entries, profile)
	if err != nil {
		return VolumeImport{}, ErrInvalid
	}
	var transfer int64
	for _, e := range d.Entries {
		n, _ := manifest.ParseSize(e.Size)
		transfer += n
	}
	transfer += int64(len(raw)) + 16<<20
	request, err := mutationRequest(input.RequestID, "volume.import", input.SourceID+"/"+input.VersionID, input)
	if err != nil {
		return VolumeImport{}, err
	}
	var out VolumeImport
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		previous, exists, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if exists {
			var receipt struct {
				ID string `json:"id"`
			}
			if err = json.Unmarshal(previous, &receipt); err != nil {
				return err
			}
			out, err = scanImport(tx.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE id=?", receipt.ID))
			return err
		}
		out, err = scanImport(tx.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE source_id=? AND version_id=? AND manifest_digest=? AND content_profile=? AND store_id=?", input.SourceID, input.VersionID, digest, input.ConsumerProfile, store))
		if err == nil {
			return s.recordImportReceipt(ctx, tx, actor.ID, request, out.ID)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		release, err := releaseState(ctx, tx)
		if err != nil {
			return err
		}
		if release.Phase != "open" {
			return ErrDraining
		}
		var local int64
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(sum(transfer_reserved),0) FROM volume_imports").Scan(&local); err != nil {
			return err
		}
		if local > localBudget-transfer {
			return ErrCapacity
		}
		usage, err := platformStorage(ctx, tx)
		if err != nil {
			return err
		}
		if usage.Total.Used > engineBudget-usage.Total.Reserved-capacity*2 {
			return ErrCapacity
		}
		out = VolumeImport{ID: uuid.NewString(), SourceID: input.SourceID, PublisherID: d.PublisherID, LibraryID: d.LibraryID, VersionID: d.VersionID, ManifestDigest: digest, Manifest: raw, Profile: input.ConsumerProfile, StoreID: store, TreeDigest: d.TreeDigest, Capacity: capacity, ActorID: actor.ID, PublisherActor: publisherActor, State: "acquiring", TransferReserved: transfer, CreatedAt: s.timestamp(), UpdatedAt: s.timestamp()}
		// All same-content imports converge on one publication; registry identities
		// remain version-specific so provenance is immutable and independently visible.
		out.Artifact = ArtifactReference{ID: "studio-" + out.ID, Version: d.VersionID, Digest: d.TreeDigest}
		if _, err = tx.ExecContext(ctx, `INSERT INTO volume_imports(`+importColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'acquiring','','',?,?,?)`, out.ID, out.SourceID, out.PublisherID, out.LibraryID, out.VersionID, out.ManifestDigest, raw, out.Profile, store, out.TreeDigest, capacity, actor.ID, publisherActor, out.Artifact.ID, out.Artifact.Version, transfer, out.CreatedAt, out.UpdatedAt); err != nil {
			return err
		}
		if len(labels) > 0 {
			label := labels[0]
			if len(label.LibraryName) > 1024 || len(label.VersionLabel) > 512 || len(label.VersionNote) > 8192 {
				return ErrInvalid
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO volume_import_labels(import_id,library_name,version_label,version_note) VALUES(?,?,?,?)`, out.ID, label.LibraryName, label.VersionLabel, label.VersionNote); err != nil {
				return err
			}
		}
		if err = s.recordImportReceipt(ctx, tx, actor.ID, request, out.ID); err != nil {
			return err
		}
		// Reserve durable publication intent at initial admission, while release
		// admission is open. An already-admitted transfer can then settle during
		// drain without trying to admit a new publication after the release fence.
		encodedEntries, _ := json.Marshal(entries)
		purpose := "authored"
		if input.ConsumerProfile == manifest.SharedAssetsProfile {
			purpose = "shared_assets"
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO content_publications(store_id,purpose,revision_digest,capacity,entries,stage,created_at,updated_at,generation) VALUES(?,?,?,?,?,'preparing',?,?,(SELECT coalesce(max(generation),-1)+1 FROM content_publication_attempts WHERE store_id=? AND purpose=? AND revision_digest=?))`, store, purpose, d.TreeDigest, capacity, string(encodedEntries), s.timestamp(), s.timestamp(), store, purpose, d.TreeDigest); err != nil {
			return err
		}
		publication, err := publicationWith(ctx, tx, store, purpose, d.TreeDigest)
		if err != nil {
			return err
		}
		if publication.Capacity != capacity {
			return ErrConflict
		}
		return s.auditEvent(ctx, tx, actor.ID, "volume.import", "volume_import", out.ID, input.RequestID, 1, map[string]any{"source_id": out.SourceID, "manifest_digest": digest, "publisher_actor": publisherActor})
	})
	return out, err
}
func (s *Store) recordImportReceipt(ctx context.Context, tx *sql.Tx, actor string, request adminRequest, id string) error {
	raw, _ := json.Marshal(map[string]string{"id": id})
	_, err := tx.ExecContext(ctx, "INSERT INTO admin_receipts(actor_id,request_id,action,target_id,body_digest,result,created_at) VALUES(?,?,?,?,?,?,?)", actor, request.ID, request.Action, request.Target, request.Digest, string(raw), s.timestamp())
	return err
}
func ImportEntries(d manifest.Descriptor) []content.Entry {
	out := make([]content.Entry, 0, len(d.Entries))
	for _, e := range d.Entries {
		n, _ := manifest.ParseSize(e.Size)
		out = append(out, content.Entry{Path: e.Path, Directory: e.Kind == "directory", Size: n, Digest: e.Digest})
	}
	return out
}
func (s *Store) VolumeImport(ctx context.Context, id string) (VolumeImport, error) {
	return scanImport(s.reader.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE id=?", id))
}
func (s *Store) VolumeImports(ctx context.Context, after string) ([]VolumeImport, error) {
	rows, err := s.reader.QueryContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE id>? ORDER BY id LIMIT 200", after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VolumeImport{}
	for rows.Next() {
		i, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (s *Store) NextVolumeImport(ctx context.Context) (VolumeImport, error) {
	return scanImport(s.reader.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE state IN ('acquiring','source_acquired','materializing') ORDER BY updated_at,id LIMIT 1"))
}
func (s *Store) AdvanceVolumeImport(ctx context.Context, id, expected, next, code string) error {
	allowed := map[string]bool{"acquiring/source_acquired": true, "source_acquired/materializing": true, "materializing/ready": true}
	if !allowed[expected+"/"+next] && next != "paused" && next != "failed" {
		return ErrInvalid
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		i, err := scanImport(tx.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE id=?", id))
		if err != nil {
			return err
		}
		if i.State != expected {
			return ErrConflict
		}
		if next != "paused" && next != "failed" {
			a, err := readAccount(ctx, tx, "id", i.ActorID)
			if err != nil {
				return err
			}
			if a.Status != "active" || a.Role != "admin" || a.MustChangePassword {
				return ErrForbidden
			}
		}
		resume := ""
		if next == "paused" || next == "failed" {
			resume = expected
		}
		_, err = tx.ExecContext(ctx, "UPDATE volume_imports SET state=?,resume_state=?,error=?,updated_at=? WHERE id=?", next, resume, code, s.timestamp(), id)
		return err
	})
}
func (s *Store) SetVolumeImportError(ctx context.Context, id, code string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE volume_imports SET error=?,updated_at=? WHERE id=? AND state IN ('acquiring','source_acquired','materializing')", code, s.timestamp(), id)
		return err
	})
}
func (s *Store) SettleVolumeTransfer(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE volume_imports SET transfer_reserved=0 WHERE id=? AND state IN ('ready','retired','canceled')", id)
		return err
	})
}
func (s *Store) VolumeImportAction(ctx context.Context, token, id, requestID, action string) (VolumeImport, error) {
	request, err := mutationRequest(requestID, "volume.import."+action, id, map[string]string{"action": action})
	if err != nil {
		return VolumeImport{}, err
	}
	var out VolumeImport
	err = s.write(ctx, func(tx *sql.Tx) error {
		a, err := s.adminSession(ctx, tx, token)
		if err != nil {
			return err
		}
		_, replayed, err := readAdminReceipt(ctx, tx, a.ID, request)
		if err != nil {
			return err
		}
		out, err = scanImport(tx.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE id=?", id))
		if err != nil || replayed {
			return err
		}
		switch action {
		case "retry":
			if out.State != "paused" && out.State != "failed" {
				return ErrConflict
			}
			if out.ResumeState == "" {
				return ErrConflict
			}
			out.State = out.ResumeState
			out.ResumeState = ""
			out.ActorID = a.ID
			out.Error = ""
		case "cancel":
			if out.State == "ready" || out.State == "retired" || out.State == "retiring" {
				return ErrConflict
			}
			out.State = "canceled"
		default:
			return ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, "UPDATE volume_imports SET state=?,resume_state=?,actor_id=?,error=?,updated_at=? WHERE id=?", out.State, out.ResumeState, out.ActorID, out.Error, s.timestamp(), id); err != nil {
			return err
		}
		if err = s.recordImportReceipt(ctx, tx, a.ID, request, id); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, a.ID, "volume.import."+action, "volume_import", id, requestID, 1, nil)
	})
	return out, err
}
func (i VolumeImport) Revision(reader content.Reader) (content.Revision, error) {
	d, err := manifest.Decode(bytes.NewReader(i.Manifest))
	if err != nil {
		return content.Revision{}, err
	}
	digest, _ := d.Digest()
	if digest != i.ManifestDigest || d.TreeDigest != i.TreeDigest || d.PublisherID != i.PublisherID || d.VersionID != i.VersionID || d.LibraryID != i.LibraryID || d.Profile != i.Profile {
		return content.Revision{}, fmt.Errorf("retained import identity mismatch")
	}
	n, _ := manifest.ParseSize(d.TotalBytes)
	profile := content.AuthoredProfile
	if d.Profile == manifest.SharedAssetsProfile {
		profile = content.SharedAssetsProfile
	}
	return content.Revision{Reader: reader, Profile: profile, Digest: d.TreeDigest, Entries: ImportEntries(d), TotalBytes: n}, nil
}
func (i VolumeImport) Builtin() (BuiltinVolume, error) {
	revision, err := i.Revision(nil)
	if err != nil {
		return BuiltinVolume{}, err
	}
	purpose := "authored"
	roles := []string{"data", "workspace"}
	if i.Profile == manifest.SharedAssetsProfile {
		purpose, roles = "shared_assets", []string{"shared_assets"}
	}
	return BuiltinVolume{Reference: i.Artifact, StoreID: i.StoreID, PublicationPurpose: purpose, MinimumBytes: i.Capacity, SourceCapacityBytes: i.Capacity, Roles: roles, Entries: revision.Entries}, nil
}

func (s *Store) FinalizeVolumeImport(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		i, err := scanImport(tx.QueryRowContext(ctx, "SELECT "+importColumns+" FROM volume_imports WHERE id=?", id))
		if err != nil {
			return err
		}
		if i.State == "ready" {
			return nil
		}
		if i.State != "materializing" {
			return ErrConflict
		}
		actor, err := readAccount(ctx, tx, "id", i.ActorID)
		if err != nil {
			return err
		}
		if actor.Status != "active" || actor.Role != "admin" || actor.MustChangePassword {
			return ErrForbidden
		}
		artifact, err := i.Builtin()
		if err != nil {
			return err
		}
		if err = ValidateBuiltinVolume(artifact); err != nil {
			return err
		}
		p, err := publicationWith(ctx, tx, i.StoreID, i.PublicationPurpose(), i.TreeDigest)
		if err != nil {
			return err
		}
		if p.Stage != "published" || p.Capacity != i.Capacity {
			return ErrArtifactUnavailable
		}
		exists, err := checkBuiltinRegistration(ctx, tx, artifact)
		if err != nil {
			return err
		}
		if !exists {
			roles, _ := json.Marshal(artifact.Roles)
			entries, _ := json.Marshal(artifact.Entries)
			if _, err = tx.ExecContext(ctx, `INSERT INTO builtin_volumes(id,version,digest,store_id,publication_purpose,minimum_bytes,roles,clean_runtime,entries,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, artifact.Reference.ID, artifact.Reference.Version, artifact.Reference.Digest, artifact.StoreID, artifact.PublicationPurpose, artifact.MinimumBytes, string(roles), false, string(entries), s.timestamp()); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE volume_imports SET state='ready',error='',updated_at=? WHERE id=?", s.timestamp(), id); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, i.ActorID, "volume.import.ready", "volume_import", id, "", 1, map[string]any{"artifact": i.Artifact, "publisher_id": i.PublisherID, "manifest_digest": i.ManifestDigest})
	})
}

func (s *Store) VolumeImportReceipt(ctx context.Context, token string, input VolumeImportInput) (VolumeImport, bool, error) {
	a, err := s.adminSession(ctx, s.reader, token)
	if err != nil {
		return VolumeImport{}, false, err
	}
	request, err := mutationRequest(input.RequestID, "volume.import", input.SourceID+"/"+input.VersionID, input)
	if err != nil {
		return VolumeImport{}, false, err
	}
	raw, found, err := readAdminReceipt(ctx, s.reader, a.ID, request)
	if err != nil || !found {
		return VolumeImport{}, found, err
	}
	var receipt struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return VolumeImport{}, false, err
	}
	i, err := s.VolumeImport(ctx, receipt.ID)
	return i, true, err
}
func (s *Store) RetryImportedPublication(ctx context.Context, i VolumeImport, budget int64) error {
	// Called by the application's serialized import/publication owner, following
	// current admin authorization. Exact budget admission is rechecked with state.
	if i.State != "failed" && i.State != "paused" {
		return ErrConflict
	}
	p, err := publicationWith(ctx, s.reader, i.StoreID, i.PublicationPurpose(), i.TreeDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.Stage != "quarantined" && p.Stage != "populating" && p.Stage != "failed" {
		return nil
	}
	usage, err := s.OperatorStorageUsage(ctx)
	if err != nil {
		return err
	}
	if usage.Total.Used > budget-usage.Total.Reserved-i.Capacity*2 {
		return ErrCapacity
	}
	return s.RetryContentPublication(ctx, i.StoreID, i.PublicationPurpose(), i.TreeDigest)
}

func (i VolumeImport) PublicationPurpose() string {
	if i.Profile == content.SharedAssetsProfile {
		return "shared_assets"
	}
	return "authored"
}

// AuthorizeVolumeImport rechecks the retained import actor independently of
// browser-session lifetime. Studio application access is not import authority.
func (s *Store) AuthorizeVolumeImport(ctx context.Context, subject string) error {
	if !validID(subject) {
		return ErrInvalid
	}
	var allowed int
	err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=? AND status='active' AND role='admin' AND must_change_password=0`, subject).Scan(&allowed)
	if err != nil {
		return err
	}
	if allowed != 1 {
		return ErrUnauthorized
	}
	return nil
}
