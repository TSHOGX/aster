package authoring

import (
	"bytes"
	"context"
	"strconv"

	"aster/contracts/volumemanifest"
)

// Seeding is atomic and idempotent. A retained draft or archived base is never
// reset on restart. Its immutable published version follows normal retention.
func (s *Store) seedSharedAssets(ctx context.Context) error {
	const library = "aster-shared-assets"
	const version = "aster-shared-assets-v1"
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM libraries WHERE id=?", library).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	digest, err := s.install(ctx, []byte(manifest.BaseAgents))
	if err != nil {
		return err
	}
	tree := []manifest.Entry{{Path: "AGENTS.md", Kind: "file", Size: strconv.Itoa(len(manifest.BaseAgents)), Digest: digest}, {Path: "skills", Kind: "directory", Size: "0"}}
	d, err := manifest.NewProfile(s.PublisherID(), library, version, manifest.SharedAssetsProfile, tree)
	if err != nil {
		return err
	}
	raw, err := d.Canonical()
	if err != nil {
		return err
	}
	descriptorDigest, err := d.Digest()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	if _, err = tx.ExecContext(ctx, `INSERT INTO libraries(id,name,description,profile,issuer,subject,created_at) VALUES(?,?,?,?,'platform','aster',?)`, library, "Aster shared assets", "Base Agent context. Publish a new version to extend it.", manifest.SharedAssetsProfile, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO versions(id,library_id,label,note,descriptor,descriptor_digest,state,issuer,subject,created_at,published_at) VALUES(?,?,'v1','Built-in Aster context',?,?,'published','platform','aster',?,?)`, version, library, raw, descriptorDigest, now, now); err != nil {
		return err
	}
	for _, e := range tree {
		size, _ := manifest.ParseSize(e.Size)
		var blob any = e.Digest
		if e.Kind == "directory" {
			blob = nil
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO version_entries VALUES(?,?,?,?,?)", version, e.Path, e.Kind, size, blob); err != nil {
			return err
		}
	}
	if err = initializeMergePolicy(ctx, tx, library); err != nil {
		return err
	}
	return tx.Commit()
}

// The installation seed is the only release established before human review.
// Link it only after proving its descriptor and Git snapshot have identical bytes.
func (s *Store) linkBuiltinBootstrap(ctx context.Context, library, commit string) error {
	if library != "aster-shared-assets" {
		return nil
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT descriptor FROM versions WHERE id='aster-shared-assets-v1' AND library_id=?", library).Scan(&raw); err != nil {
		return err
	}
	descriptor, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	repo, err := s.historyRepository(ctx, library)
	if err != nil {
		return err
	}
	snapshot, err := repo.Snapshot(ctx, commit)
	if err != nil {
		return err
	}
	if snapshot.Profile != descriptor.Profile || snapshot.TreeDigest() != descriptor.TreeDigest {
		return ErrInvalid
	}
	_, err = s.db.ExecContext(ctx, "UPDATE versions SET accepted_commit=? WHERE id='aster-shared-assets-v1' AND accepted_commit=''", commit)
	return err
}
