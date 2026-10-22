package projections

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestSealedArtifactsIdentityIntegrityAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	data := []byte("sealed corpus")
	ref := evolution.ArtifactRef{ID: "snapshot", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest(data), Size: int64(len(data)), MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	for _, bad := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 0), bytes.Repeat([]byte("x"), len(data))} {
		if err = s.SealArtifact(ctx, ref, bytes.NewReader(bad)); err == nil {
			t.Fatal("invalid bytes sealed")
		}
	}
	if err = s.SealArtifact(ctx, ref, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err = s.SealArtifact(ctx, ref, bytes.NewReader(data)); err != nil {
		t.Fatal("replay", err)
	}
	changed := ref
	changed.GrantID = "other"
	if err = s.SealArtifact(ctx, changed, bytes.NewReader(data)); err != ErrConflict {
		t.Fatal("artifact provenance changed", err)
	}
	if _, err = s.Artifact(ctx, changed); err != ErrConflict {
		t.Fatal("substituted grant read", err)
	}
	changed = ref
	changed.GroupID = "other"
	if _, err = s.Artifact(ctx, changed); err != ErrNotFound {
		t.Fatal("cross-group read", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE artifact_store_quota SET used_bytes=? WHERE id=1", MaxArtifactStoreBytes); err != nil {
		t.Fatal(err)
	}
	extra := ref
	extra.ID = "extra"
	extra.Size = 1
	extra.Digest = evolution.Digest([]byte("x"))
	if err = s.SealArtifact(ctx, extra, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("quota bypassed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := s.Artifact(ctx, ref)
	if err != nil || !bytes.Equal(retained, data) {
		t.Fatal("restart lost sealed bytes", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE artifacts SET body='corrupt'"); err == nil {
		t.Fatal("sealed artifact modified")
	}
}
