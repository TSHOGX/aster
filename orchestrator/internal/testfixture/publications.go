package testfixture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/sqlitex"
)

type RuntimePublication struct {
	Required    runtimerelease.RequiredPublication
	Publication content.Publication
}

// RuntimePublications explicitly authors real immutable test sources through
// public engine operations. Tests install these observed identities under their
// own store transaction/offline lock; no production executable imports this.
func RuntimePublications(ctx context.Context, backend content.Backend, required []runtimerelease.RequiredPublication) ([]RuntimePublication, error) {
	identity, err := backend.Identity(ctx)
	if err != nil {
		return nil, err
	}
	out := []RuntimePublication{}
	for _, role := range required {
		purpose := "assets"
		entries := []content.Entry{}
		files := map[string][]byte{"AGENTS.md": []byte("Use only the declared Project workspace.\n")}
		if role.Role == "agent_config" {
			purpose = "config"
			entries = append(entries, content.Entry{Path: "pi", Directory: true})
			files = map[string][]byte{
				"pi/models.json":   []byte(`{"providers":{"local":{"baseUrl":"http://127.0.0.1:18889","api":"anthropic-messages","apiKey":"fixture-only","models":[{"id":"local-model","name":"Local model","reasoning":false,"input":["text"],"contextWindow":8192,"maxTokens":256,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}]}}}`),
				"pi/settings.json": []byte(`{"defaultProvider":"local","defaultModel":"local-model","compaction":{"enabled":false}}`),
			}
		}
		for name, data := range files {
			entries = append(entries, content.Entry{Path: name, Size: int64(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data))})
		}

		digest := content.Digest(entries)
		key := "fixture-publication-" + role.Role + "-" + digest
		staged, err := backend.CreateVolume(ctx, key, 1<<20)
		if err != nil {
			return nil, err
		}
		if role.Role == "agent_config" {
			if err = backend.MkdirVolume(ctx, staged.VolumeReference, "pi"); err != nil {
				return nil, err
			}
		}
		for name, data := range files {
			if err = backend.WriteVolume(ctx, staged.VolumeReference, name, data); err != nil {
				return nil, err
			}
		}

		derived, err := backend.SnapshotVolume(ctx, staged.VolumeReference, key+"-immutable", 1<<20)
		if err != nil {
			return nil, err
		}
		if err = content.VerifyVolume(ctx, backend, derived.VolumeReference, digest); err != nil {
			return nil, err
		}
		out = append(out, RuntimePublication{Required: role, Publication: content.Publication{StoreID: identity.StoreID, Purpose: purpose, RevisionDigest: digest, Capacity: 1 << 20, Entries: entries, StagingVolumeID: staged.VolumeID, DerivedVolumeID: derived.VolumeID, PublishedVolumeID: derived.VolumeID, Stage: "published"}})
	}
	return out, nil
}
func RecordRuntimePublications(ctx context.Context, tx *sql.Tx, publications []content.Publication) error {
	for _, p := range publications {
		entries, err := json.Marshal(p.Entries)
		if err != nil {
			return err
		}
		now := sqlitex.FormatTime(time.Now())
		if _, err = tx.ExecContext(ctx, `INSERT INTO content_publications(store_id,purpose,revision_digest,capacity,entries,staging_volume_id,derived_volume_id,published_volume_id,stage,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'published',?,?) ON CONFLICT(store_id,purpose,revision_digest) DO NOTHING`, p.StoreID, p.Purpose, p.RevisionDigest, p.Capacity, string(entries), p.StagingVolumeID, p.DerivedVolumeID, p.PublishedVolumeID, now, now); err != nil {
			return err
		}
	}
	return nil
}
