package authoring

import (
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO libraries(id,name,description,profile,base_version,issuer,subject,created_at) VALUES(?,?,?,?,?,'platform','aster',?)`, library, "Aster shared assets", "Base Agent context. Publish a new version to extend it.", manifest.SharedAssetsProfile, version, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO versions(id,library_id,label,note,source_revision,descriptor,descriptor_digest,state,issuer,subject,created_at,published_at) VALUES(?,?,'v1','Built-in Aster context',1,?,?,'published','platform','aster',?,?)`, version, library, raw, descriptorDigest, now, now); err != nil {
		return err
	}
	for _, e := range tree {
		size, _ := manifest.ParseSize(e.Size)
		var blob any = e.Digest
		if e.Kind == "directory" {
			blob = nil
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO draft_entries VALUES(?,?,?,?,?)", library, e.Path, e.Kind, size, blob); err != nil {
			return err
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
