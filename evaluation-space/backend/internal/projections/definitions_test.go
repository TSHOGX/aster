package projections

import (
	"aster/contracts/evolution"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDefinitionCatalogValidatesDigestAndImmutableRevision(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	service := evolution.Service{Ref: evolution.Revision{ID: "service", Revision: 1}, Kind: "evolution", ConnectionID: "connection", RunnerDigest: evolution.Digest([]byte("runner")), Capabilities: evolution.Capabilities{ProtocolVersion: 1}}
	service.Ref.Digest, err = evolution.DefinitionDigest(service)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(service)
	if err = s.PublishDefinition(ctx, "service", raw); err != nil {
		t.Fatal(err)
	}
	if err = s.PublishDefinition(ctx, "service", raw); err != nil {
		t.Fatal("replay", err)
	}
	changed := service
	changed.ConnectionID = "changed"
	raw, _ = json.Marshal(changed)
	if err = s.PublishDefinition(ctx, "service", raw); err == nil {
		t.Fatal("payload substitution accepted")
	}
	changed.Ref.Digest, _ = evolution.DefinitionDigest(changed)
	raw, _ = json.Marshal(changed)
	if err = s.PublishDefinition(ctx, "service", raw); err != ErrConflict {
		t.Fatal("immutable revision replaced", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := readDefinition(ctx, tx, "service", service.Ref)
	tx.Rollback()
	if err != nil || value.(evolution.Service).ConnectionID != "connection" {
		t.Fatal("stored definition changed", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE catalog_definitions SET digest='changed'"); err == nil {
		t.Fatal("definition mutation allowed")
	}
}
